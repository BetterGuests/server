package messages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrInvalidConversation  = errors.New("invalid conversation")
	ErrInvalidMessage       = errors.New("invalid message")
	ErrMessageNotFound      = errors.New("message not found")
	ErrInvalidReaction      = errors.New("invalid reaction")
	ErrStayStillOpen        = errors.New("stay still open")
)

var reactionEmoji = []string{"👍", "❤️", "😂", "😮", "😢", "🙏"}

type MessageService struct {
	store     Store
	publisher Publisher
	focus     FocusTracker
	now       func() time.Time
	newID     func() uuid.UUID

	typingMu sync.Mutex
	typing   map[typist]typingSeen
}

func NewMessageService(store Store) *MessageService {
	return &MessageService{store: store, now: time.Now, newID: uuid.New, typing: make(map[typist]typingSeen)}
}

func (service *MessageService) WithPublisher(publisher Publisher) *MessageService {
	service.publisher = publisher
	return service
}

func (service *MessageService) WithFocus(focus FocusTracker) *MessageService {
	service.focus = focus
	return service
}

func ConversationTopic(conversationID uuid.UUID) string {
	return conversationTopicPrefix + conversationID.String()
}

func (service *MessageService) FocusChanged(ctx context.Context, userID uuid.UUID, topic string, present bool) {
	conversationID, ok := conversationFromTopic(topic)
	if !ok || service.focus == nil {
		return
	}
	participants, err := service.membership(ctx, userID, conversationID)
	if err != nil {
		return
	}
	role, counterparty, counterpartyRole := sides(participants, userID)
	service.focus.PublishFocused(counterparty, topic, PresenceEventKind, PresenceEvent{
		ConversationID: conversationID, Role: role, Here: present,
	})
	if present && service.focus.Focused(counterparty, topic) {
		service.focus.PublishFocused(userID, topic, PresenceEventKind, PresenceEvent{
			ConversationID: conversationID, Role: counterpartyRole, Here: true,
		})
	}
}

func conversationFromTopic(topic string) (uuid.UUID, bool) {
	raw, found := strings.CutPrefix(topic, conversationTopicPrefix)
	if !found {
		return uuid.Nil, false
	}
	conversationID, err := uuid.Parse(raw)
	return conversationID, err == nil
}

func sides(participants Participants, actorID uuid.UUID) (Role, uuid.UUID, Role) {
	if participants.HostID == actorID {
		return RoleHost, participants.GuestID, RoleGuest
	}
	return RoleGuest, participants.HostID, RoleHost
}

func (service *MessageService) Open(ctx context.Context, actorID uuid.UUID, target OpenTarget) (Summary, error) {
	if target.BookingID == uuid.Nil && target.ListingID == uuid.Nil {
		return Summary{}, fmt.Errorf("%w: %s", ErrInvalidConversation, missingTargetMessage)
	}
	conversationID, err := service.store.Open(ctx, actorID, service.newID(), target, service.now().UTC())
	if err != nil {
		return Summary{}, err
	}
	return service.store.Get(ctx, actorID, conversationID)
}

func (service *MessageService) List(ctx context.Context, actorID uuid.UUID) ([]Summary, error) {
	return service.store.List(ctx, actorID)
}

func (service *MessageService) Get(ctx context.Context, actorID, conversationID uuid.UUID) (Summary, error) {
	return service.store.Get(ctx, actorID, conversationID)
}

func (service *MessageService) Messages(
	ctx context.Context,
	actorID, conversationID uuid.UUID,
	before *time.Time,
	limit int,
) (MessagePage, error) {
	limit = clampPageSize(limit)
	fetched, err := service.store.Messages(ctx, actorID, conversationID, before, limit+1)
	if err != nil {
		return MessagePage{}, err
	}
	hasMore := len(fetched) > limit
	if hasMore {
		fetched = fetched[:limit]
	}
	return MessagePage{Messages: oldestFirst(fetched), HasMore: hasMore}, nil
}

func (service *MessageService) Send(ctx context.Context, actorID, conversationID uuid.UUID, draft Draft) (Message, error) {
	body, err := cleanBody(draft.Body)
	if err != nil {
		return Message{}, err
	}
	draft.Body = body
	if draft.ID == uuid.Nil {
		draft.ID = service.newID()
	}
	message, participants, created, err := service.store.Insert(ctx, actorID, conversationID, draft, service.now().UTC())
	if err != nil {
		return Message{}, err
	}
	if created {
		service.forgetTyping(actorID, conversationID)
		service.publishToBoth(participants, MessageEventKind, MessageEvent{ConversationID: message.ConversationID, Message: message})
	}
	return message, nil
}

func (service *MessageService) Delete(ctx context.Context, actorID, conversationID, messageID uuid.UUID) error {
	participants, err := service.store.Delete(ctx, actorID, conversationID, messageID)
	if err != nil {
		return err
	}
	service.publishToBoth(participants, MessageDeletedEventKind, MessageDeletedEvent{ConversationID: conversationID, MessageID: messageID})
	return nil
}

func (service *MessageService) React(ctx context.Context, actorID, conversationID, messageID uuid.UUID, emoji string) ([]Reaction, error) {
	if !slices.Contains(reactionEmoji, emoji) {
		return nil, fmt.Errorf("%w: pick one of %s", ErrInvalidReaction, strings.Join(reactionEmoji, " "))
	}
	reactions, participants, err := service.store.ToggleReaction(ctx, actorID, conversationID, messageID, service.newID(), emoji, service.now().UTC())
	if err != nil {
		return nil, err
	}
	service.publishToBoth(participants, ReactionsEventKind, ReactionsEvent{ConversationID: conversationID, MessageID: messageID, Reactions: reactions})
	return reactions, nil
}

func (service *MessageService) Typing(ctx context.Context, actorID, conversationID uuid.UUID, update TypingUpdate) error {
	draft, err := cleanDraft(update.Draft)
	if err != nil {
		return err
	}
	key := typist{actorID: actorID, conversationID: conversationID}
	now := service.now()
	seen, send := service.claimTypingSlot(key, now, update.Idle)
	if !send {
		return nil
	}
	participants := seen.participants
	if now.Sub(seen.checkedAt) >= typingMembershipTTL {
		participants, err = service.store.Participants(ctx, actorID, conversationID)
		if err != nil {
			service.forgetTyping(actorID, conversationID)
			return err
		}
		service.rememberMembership(key, participants, now)
	}
	if service.focus == nil {
		return nil
	}
	role, counterparty, _ := sides(participants, actorID)
	service.focus.PublishFocused(counterparty, ConversationTopic(conversationID), TypingEventKind, TypingEvent{
		ConversationID: conversationID, Role: role, Draft: draft, Idle: update.Idle,
	})
	return nil
}

func (service *MessageService) membership(ctx context.Context, actorID, conversationID uuid.UUID) (Participants, error) {
	key := typist{actorID: actorID, conversationID: conversationID}
	now := service.now()
	service.typingMu.Lock()
	seen, known := service.typing[key]
	service.typingMu.Unlock()
	if known && now.Sub(seen.checkedAt) < typingMembershipTTL {
		return seen.participants, nil
	}
	participants, err := service.store.Participants(ctx, actorID, conversationID)
	if err != nil {
		return Participants{}, err
	}
	service.rememberMembership(key, participants, now)
	return participants, nil
}

func (service *MessageService) claimTypingSlot(key typist, now time.Time, idle bool) (typingSeen, bool) {
	service.typingMu.Lock()
	defer service.typingMu.Unlock()
	seen, known := service.typing[key]
	if known && !idle && now.Sub(seen.lastSent) < typingThrottle {
		return seen, false
	}
	if !known && len(service.typing) >= maxTrackedTypists {
		clear(service.typing)
	}
	seen.lastSent = now
	service.typing[key] = seen
	return seen, true
}

func (service *MessageService) rememberMembership(key typist, participants Participants, now time.Time) {
	service.typingMu.Lock()
	defer service.typingMu.Unlock()
	seen := service.typing[key]
	seen.participants = participants
	seen.checkedAt = now
	service.typing[key] = seen
}

func (service *MessageService) forgetTyping(actorID, conversationID uuid.UUID) {
	service.typingMu.Lock()
	defer service.typingMu.Unlock()
	delete(service.typing, typist{actorID: actorID, conversationID: conversationID})
}

func (service *MessageService) MarkRead(ctx context.Context, actorID, conversationID uuid.UUID) error {
	return service.store.MarkRead(ctx, actorID, conversationID, service.now().UTC())
}

func (service *MessageService) Hide(ctx context.Context, actorID, conversationID uuid.UUID) error {
	return service.store.Hide(ctx, actorID, conversationID, service.now().UTC())
}

func cleanDraft(draft string) (string, error) {
	if !utf8.ValidString(draft) {
		return "", fmt.Errorf("%w: a draft must be valid text", ErrInvalidMessage)
	}
	if utf8.RuneCountInString(draft) > maxBodyRunes {
		return string([]rune(draft)[:maxBodyRunes]), nil
	}
	return draft, nil
}

func (service *MessageService) publishToBoth(participants Participants, kind string, event any) {
	if service.publisher == nil {
		return
	}
	service.publisher.Publish(participants.GuestID, kind, event)
	service.publisher.Publish(participants.HostID, kind, event)
}

func cleanBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("%w: a message cannot be empty", ErrInvalidMessage)
	}
	if !utf8.ValidString(body) {
		return "", fmt.Errorf("%w: a message must be valid text", ErrInvalidMessage)
	}
	if utf8.RuneCountInString(body) > maxBodyRunes {
		return "", fmt.Errorf("%w: a message cannot exceed %d characters", ErrInvalidMessage, maxBodyRunes)
	}
	return body, nil
}

func clampPageSize(limit int) int {
	switch {
	case limit <= 0:
		return defaultPageSize
	case limit > maxPageSize:
		return maxPageSize
	}
	return limit
}

func oldestFirst(newestFirst []Message) []Message {
	ordered := make([]Message, len(newestFirst))
	for index, message := range newestFirst {
		ordered[len(newestFirst)-1-index] = message
	}
	return ordered
}

func preview(body string) string {
	if utf8.RuneCountInString(body) <= previewRunes {
		return body
	}
	return string([]rune(body)[:previewRunes])
}
