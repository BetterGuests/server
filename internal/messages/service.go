package messages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrInvalidConversation  = errors.New("invalid conversation")
	ErrInvalidMessage       = errors.New("invalid message")
)

type MessageService struct {
	store     Store
	publisher Publisher
	now       func() time.Time
	newID     func() uuid.UUID
}

func NewMessageService(store Store) *MessageService {
	return &MessageService{store: store, now: time.Now, newID: uuid.New}
}

func (service *MessageService) WithPublisher(publisher Publisher) *MessageService {
	service.publisher = publisher
	return service
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

func (service *MessageService) Send(ctx context.Context, actorID, conversationID uuid.UUID, body string) (Message, error) {
	body, err := cleanBody(body)
	if err != nil {
		return Message{}, err
	}
	message, participants, err := service.store.Insert(ctx, actorID, conversationID, service.newID(), body, service.now().UTC())
	if err != nil {
		return Message{}, err
	}
	service.broadcast(participants, message)
	return message, nil
}

func (service *MessageService) MarkRead(ctx context.Context, actorID, conversationID uuid.UUID) error {
	return service.store.MarkRead(ctx, actorID, conversationID, service.now().UTC())
}

func (service *MessageService) broadcast(participants Participants, message Message) {
	if service.publisher == nil {
		return
	}
	event := MessageEvent{ConversationID: message.ConversationID, Message: message}
	service.publisher.Publish(participants.GuestID, MessageEventKind, event)
	service.publisher.Publish(participants.HostID, MessageEventKind, event)
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
