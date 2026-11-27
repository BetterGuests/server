package messages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type storeStub struct {
	openID           uuid.UUID
	openErr          error
	openedTarget     OpenTarget
	summary          Summary
	getErr           error
	page             []Message
	pageErr          error
	askedLimit       int
	askedBefore      *time.Time
	inserted         Message
	participants     Participants
	insertErr        error
	insertCalls      int
	readErr          error
	readCalls        int
	insertExists     bool
	insertedDraft    Draft
	deleteErr        error
	deleted          []uuid.UUID
	reactions        []Reaction
	reactErr         error
	reacted          string
	participantsErr  error
	participantCalls int
	hideErr          error
	hiddenAt         time.Time
}

func (stub *storeStub) Open(_ context.Context, _, _ uuid.UUID, target OpenTarget, _ time.Time) (uuid.UUID, error) {
	stub.openedTarget = target
	return stub.openID, stub.openErr
}

func (stub *storeStub) Get(context.Context, uuid.UUID, uuid.UUID) (Summary, error) {
	return stub.summary, stub.getErr
}

func (stub *storeStub) List(context.Context, uuid.UUID) ([]Summary, error) {
	return []Summary{stub.summary}, nil
}

func (stub *storeStub) Messages(_ context.Context, _, _ uuid.UUID, before *time.Time, limit int) ([]Message, error) {
	stub.askedLimit = limit
	stub.askedBefore = before
	return stub.page, stub.pageErr
}

func (stub *storeStub) Insert(_ context.Context, actorID, conversationID uuid.UUID, draft Draft, now time.Time) (Message, Participants, bool, error) {
	stub.insertCalls++
	stub.insertedDraft = draft
	if stub.insertErr != nil {
		return Message{}, Participants{}, false, stub.insertErr
	}
	stub.inserted = Message{ID: draft.ID, ConversationID: conversationID, SenderID: actorID, SenderRole: RoleGuest, Body: draft.Body, CreatedAt: now}
	return stub.inserted, stub.participants, !stub.insertExists, nil
}

func (stub *storeStub) Delete(_ context.Context, _, _, messageID uuid.UUID) (Participants, error) {
	if stub.deleteErr != nil {
		return Participants{}, stub.deleteErr
	}
	stub.deleted = append(stub.deleted, messageID)
	return stub.participants, nil
}

func (stub *storeStub) ToggleReaction(_ context.Context, _, _, _, _ uuid.UUID, emoji string, _ time.Time) ([]Reaction, Participants, error) {
	stub.reacted = emoji
	return stub.reactions, stub.participants, stub.reactErr
}

func (stub *storeStub) Participants(context.Context, uuid.UUID, uuid.UUID) (Participants, error) {
	stub.participantCalls++
	return stub.participants, stub.participantsErr
}

func (stub *storeStub) MarkRead(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	stub.readCalls++
	return stub.readErr
}

func (stub *storeStub) Hide(_ context.Context, _, _ uuid.UUID, now time.Time) error {
	stub.hiddenAt = now
	return stub.hideErr
}

func (stub *storeStub) SetPinned(context.Context, uuid.UUID, uuid.UUID, bool, time.Time) error {
	return nil
}

type publishedEvent struct {
	userID  uuid.UUID
	kind    string
	payload any
	topic   string
}

type publisherStub struct {
	events  []publishedEvent
	focused map[uuid.UUID]string
}

func (stub *publisherStub) Publish(userID uuid.UUID, kind string, payload any) {
	stub.events = append(stub.events, publishedEvent{userID: userID, kind: kind, payload: payload})
}

func (stub *publisherStub) PublishFocused(userID uuid.UUID, topic, kind string, payload any) {
	stub.events = append(stub.events, publishedEvent{userID: userID, kind: kind, payload: payload, topic: topic})
}

func (stub *publisherStub) Focused(userID uuid.UUID, topic string) bool {
	return stub.focused[userID] == topic
}

func TestSendStoresTrimmedBodyAndTellsBothParticipants(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)
	conversationID := uuid.New()

	message, err := service.Send(context.Background(), guestID, conversationID, Draft{Body: "  Is early check-in possible?  \n"})
	if err != nil {
		t.Fatal(err)
	}
	if message.Body != "Is early check-in possible?" {
		t.Fatalf("body = %q", message.Body)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("expected an event for each participant, got %d", len(publisher.events))
	}
	recipients := map[uuid.UUID]bool{}
	for _, event := range publisher.events {
		recipients[event.userID] = true
		payload, ok := event.payload.(MessageEvent)
		if event.kind != MessageEventKind || !ok || payload.ConversationID != conversationID || payload.Message.ID != message.ID {
			t.Fatalf("unexpected event %+v", event)
		}
	}
	if !recipients[guestID] || !recipients[hostID] {
		t.Fatalf("both participants must be told, got %v", recipients)
	}
}

func TestSendRejectsBadBodiesWithoutTouchingTheStore(t *testing.T) {
	tests := map[string]string{
		"empty":           "",
		"whitespace only": " \n\t ",
		"too long":        strings.Repeat("a", MaxBodyRunes+1),
		"invalid utf-8":   "hello \xff",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			store := &storeStub{}
			publisher := &publisherStub{}
			service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

			_, err := service.Send(context.Background(), uuid.New(), uuid.New(), Draft{Body: body})

			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("expected ErrInvalidMessage, got %v", err)
			}
			if store.insertCalls != 0 || len(publisher.events) != 0 {
				t.Fatal("a rejected message must not be stored or published")
			}
		})
	}
}

func TestSendAllowsAMessageOfExactlyTheLimit(t *testing.T) {
	service := NewMessageService(&storeStub{})

	if _, err := service.Send(context.Background(), uuid.New(), uuid.New(), Draft{Body: strings.Repeat("é", MaxBodyRunes)}); err != nil {
		t.Fatalf("a body of %d characters must be accepted: %v", MaxBodyRunes, err)
	}
}

func TestSendToAConversationThePersonIsNotIn(t *testing.T) {
	publisher := &publisherStub{}
	service := NewMessageService(&storeStub{insertErr: ErrConversationNotFound}).WithPublisher(publisher)

	_, err := service.Send(context.Background(), uuid.New(), uuid.New(), Draft{Body: "hello"})

	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("expected ErrConversationNotFound, got %v", err)
	}
	if len(publisher.events) != 0 {
		t.Fatal("nothing may be published when the store refuses the message")
	}
}

func message(offsetMinutes int) Message {
	return Message{ID: uuid.New(), CreatedAt: time.Date(2026, 6, 1, 12, offsetMinutes, 0, 0, time.UTC)}
}

func TestMessagesReturnsOldestFirstAndFlagsMoreHistory(t *testing.T) {
	newest, middle, oldest := message(30), message(20), message(10)
	store := &storeStub{page: []Message{newest, middle, oldest}}
	service := NewMessageService(store)

	page, err := service.Messages(context.Background(), uuid.New(), uuid.New(), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if store.askedLimit != 3 {
		t.Fatalf("store must be asked for one extra row to detect more history, asked for %d", store.askedLimit)
	}
	if !page.HasMore || len(page.Messages) != 2 {
		t.Fatalf("page = %+v", page)
	}
	if page.Messages[0].ID != middle.ID || page.Messages[1].ID != newest.ID {
		t.Fatal("messages must be ordered oldest first")
	}
}

func TestMessagesWithoutMoreHistory(t *testing.T) {
	service := NewMessageService(&storeStub{page: []Message{message(20), message(10)}})

	page, err := service.Messages(context.Background(), uuid.New(), uuid.New(), nil, 5)
	if err != nil || page.HasMore || len(page.Messages) != 2 {
		t.Fatalf("page = %+v, err = %v", page, err)
	}
}

func TestMessagesClampsThePageSize(t *testing.T) {
	tests := []struct{ asked, want int }{
		{0, defaultPageSize + 1},
		{-4, defaultPageSize + 1},
		{maxPageSize * 5, maxPageSize + 1},
		{7, 8},
	}
	for _, test := range tests {
		store := &storeStub{}
		service := NewMessageService(store)
		if _, err := service.Messages(context.Background(), uuid.New(), uuid.New(), nil, test.asked); err != nil {
			t.Fatal(err)
		}
		if store.askedLimit != test.want {
			t.Fatalf("limit %d reached the store as %d, want %d", test.asked, store.askedLimit, test.want)
		}
	}
}

func TestMessagesEmptyPageIsAnEmptySliceNotNull(t *testing.T) {
	service := NewMessageService(&storeStub{})

	page, err := service.Messages(context.Background(), uuid.New(), uuid.New(), nil, 10)
	if err != nil || page.Messages == nil {
		t.Fatalf("page = %+v, err = %v", page, err)
	}
}

func TestMessagesPassesTheCursorThrough(t *testing.T) {
	store := &storeStub{}
	service := NewMessageService(store)
	cursor := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	if _, err := service.Messages(context.Background(), uuid.New(), uuid.New(), &cursor, 10); err != nil {
		t.Fatal(err)
	}
	if store.askedBefore == nil || !store.askedBefore.Equal(cursor) {
		t.Fatalf("cursor = %v", store.askedBefore)
	}
}

func TestOpenNeedsABookingOrAListing(t *testing.T) {
	store := &storeStub{}
	service := NewMessageService(store)

	_, err := service.Open(context.Background(), uuid.New(), OpenTarget{})

	if !errors.Is(err, ErrInvalidConversation) {
		t.Fatalf("expected ErrInvalidConversation, got %v", err)
	}
}

func TestOpenReturnsTheSummaryOfTheOpenedConversation(t *testing.T) {
	want := Summary{ID: uuid.New(), CounterpartyName: "Coast to Coast Getaways"}
	store := &storeStub{openID: want.ID, summary: want}
	service := NewMessageService(store)
	listingID := uuid.New()

	got, err := service.Open(context.Background(), uuid.New(), OpenTarget{ListingID: listingID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || store.openedTarget.ListingID != listingID {
		t.Fatalf("got %+v, target %+v", got, store.openedTarget)
	}
}

func TestOpenSurfacesStoreRefusals(t *testing.T) {
	service := NewMessageService(&storeStub{openErr: ErrConversationNotFound})

	_, err := service.Open(context.Background(), uuid.New(), OpenTarget{BookingID: uuid.New()})

	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("expected ErrConversationNotFound, got %v", err)
	}
}

func TestPreviewCutsLongBodiesOnACharacterBoundary(t *testing.T) {
	long := strings.Repeat("é", previewRunes+50)

	got := preview(long)

	if len([]rune(got)) != previewRunes {
		t.Fatalf("preview has %d characters, want %d", len([]rune(got)), previewRunes)
	}
	if preview("short") != "short" {
		t.Fatal("short bodies must pass through unchanged")
	}
}

func TestSendKeepsTheClientChosenIDSoRetriesCannotDuplicate(t *testing.T) {
	store := &storeStub{}
	service := NewMessageService(store)
	chosen := uuid.New()

	message, err := service.Send(context.Background(), uuid.New(), uuid.New(), Draft{ID: chosen, Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if message.ID != chosen || store.insertedDraft.ID != chosen {
		t.Fatalf("id = %s, want the client id %s", message.ID, chosen)
	}
}

func TestSendRetryOfAStoredMessageDoesNotNotifyAgain(t *testing.T) {
	store := &storeStub{insertExists: true, participants: Participants{GuestID: uuid.New(), HostID: uuid.New()}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	if _, err := service.Send(context.Background(), uuid.New(), uuid.New(), Draft{ID: uuid.New(), Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("a retried send must not broadcast twice, got %d events", len(publisher.events))
	}
}

func TestSendPassesTheReplyTarget(t *testing.T) {
	store := &storeStub{}
	replyTo := uuid.New()

	if _, err := NewMessageService(store).Send(context.Background(), uuid.New(), uuid.New(), Draft{Body: "yes", ReplyToID: replyTo}); err != nil {
		t.Fatal(err)
	}
	if store.insertedDraft.ReplyToID != replyTo {
		t.Fatalf("reply target = %s, want %s", store.insertedDraft.ReplyToID, replyTo)
	}
}

func TestDeleteTellsBothParticipantsWhichMessageWent(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)
	conversationID, messageID := uuid.New(), uuid.New()

	if err := service.Delete(context.Background(), guestID, conversationID, messageID); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("expected two events, got %d", len(publisher.events))
	}
	for _, event := range publisher.events {
		payload, ok := event.payload.(MessageDeletedEvent)
		if event.kind != MessageDeletedEventKind || !ok || payload.MessageID != messageID || payload.ConversationID != conversationID {
			t.Fatalf("unexpected event %+v", event)
		}
	}
}

func TestDeleteOfSomeoneElsesMessageIsRefusedQuietly(t *testing.T) {
	store := &storeStub{deleteErr: ErrMessageNotFound}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	err := service.Delete(context.Background(), uuid.New(), uuid.New(), uuid.New())
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("err = %v, want ErrMessageNotFound", err)
	}
	if len(publisher.events) != 0 {
		t.Fatal("a refused delete must not broadcast")
	}
}

func TestReactAcceptsOnlyTheOfferedEmoji(t *testing.T) {
	store := &storeStub{}
	service := NewMessageService(store)

	_, err := service.React(context.Background(), uuid.New(), uuid.New(), uuid.New(), "🦄")
	if !errors.Is(err, ErrInvalidReaction) {
		t.Fatalf("err = %v, want ErrInvalidReaction", err)
	}
	if store.reacted != "" {
		t.Fatal("an unknown emoji must not reach the store")
	}
}

func TestReactBroadcastsTheMessagesReactions(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	want := []Reaction{{Emoji: "❤️", Role: RoleGuest}}
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}, reactions: want}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	got, err := service.React(context.Background(), guestID, uuid.New(), uuid.New(), "❤️")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("reactions = %+v", got)
	}
	if len(publisher.events) != 2 || publisher.events[0].kind != ReactionsEventKind {
		t.Fatalf("events = %+v", publisher.events)
	}
}

func TestTypingTellsOnlyTheOtherPerson(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)
	conversationID := uuid.New()

	if err := service.Typing(context.Background(), hostID, conversationID, TypingUpdate{}); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected one event, got %d", len(publisher.events))
	}
	event := publisher.events[0]
	payload, ok := event.payload.(TypingEvent)
	if event.userID != guestID || event.kind != TypingEventKind || !ok || payload.Role != RoleHost || payload.ConversationID != conversationID {
		t.Fatalf("unexpected event %+v", event)
	}
}

func TestTypingIsThrottledPerPersonAndConversation(t *testing.T) {
	store := &storeStub{participants: Participants{GuestID: uuid.New(), HostID: uuid.New()}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	actor, conversation := store.participants.GuestID, uuid.New()

	for range 5 {
		if err := service.Typing(context.Background(), actor, conversation, TypingUpdate{}); err != nil {
			t.Fatal(err)
		}
	}
	if store.participantCalls != 1 || len(publisher.events) != 1 {
		t.Fatalf("burst reached the store %d times and published %d events, want 1 and 1", store.participantCalls, len(publisher.events))
	}

	clock = clock.Add(typingThrottle)
	if err := service.Typing(context.Background(), actor, conversation, TypingUpdate{}); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("typing after the throttle window must publish again, got %d events", len(publisher.events))
	}
}

func TestSendingClearsTheTypingThrottleSoTheNextDraftShowsAtOnce(t *testing.T) {
	store := &storeStub{participants: Participants{GuestID: uuid.New(), HostID: uuid.New()}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)
	actor, conversation := store.participants.GuestID, uuid.New()

	_ = service.Typing(context.Background(), actor, conversation, TypingUpdate{})
	if _, err := service.Send(context.Background(), actor, conversation, Draft{Body: "done"}); err != nil {
		t.Fatal(err)
	}
	_ = service.Typing(context.Background(), actor, conversation, TypingUpdate{})

	if store.participantCalls != 2 {
		t.Fatalf("typing after a send must not be throttled, store calls = %d", store.participantCalls)
	}
}

func TestTypingInAConversationThePersonIsNotInFails(t *testing.T) {
	store := &storeStub{participantsErr: ErrConversationNotFound}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	err := service.Typing(context.Background(), uuid.New(), uuid.New(), TypingUpdate{})
	if !errors.Is(err, ErrConversationNotFound) || len(publisher.events) != 0 {
		t.Fatalf("err = %v, events = %d", err, len(publisher.events))
	}
}

func TestTypingForwardsTheLiveDraftToTheOtherPerson(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	if err := service.Typing(context.Background(), guestID, uuid.New(), TypingUpdate{Draft: "Can we check in at"}); err != nil {
		t.Fatal(err)
	}
	payload, _ := publisher.events[0].payload.(TypingEvent)
	if publisher.events[0].userID != hostID || payload.Draft != "Can we check in at" {
		t.Fatalf("event = %+v", publisher.events[0])
	}
}

func TestStoppingIsNeverThrottledSoTheBubbleClearsAtOnce(t *testing.T) {
	store := &storeStub{participants: Participants{GuestID: uuid.New(), HostID: uuid.New()}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)
	service.now = func() time.Time { return time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC) }
	actor, conversation := store.participants.GuestID, uuid.New()

	_ = service.Typing(context.Background(), actor, conversation, TypingUpdate{Draft: "hel"})
	_ = service.Typing(context.Background(), actor, conversation, TypingUpdate{Idle: true})

	if len(publisher.events) != 2 {
		t.Fatalf("events = %d, want the idle update to go through", len(publisher.events))
	}
	if payload, _ := publisher.events[1].payload.(TypingEvent); !payload.Idle {
		t.Fatalf("second event = %+v", publisher.events[1])
	}
}

func TestLongDraftsAreCutToTheMessageLimit(t *testing.T) {
	store := &storeStub{participants: Participants{GuestID: uuid.New(), HostID: uuid.New()}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	_ = service.Typing(context.Background(), store.participants.GuestID, uuid.New(), TypingUpdate{Draft: strings.Repeat("a", MaxBodyRunes+50)})

	payload, _ := publisher.events[0].payload.(TypingEvent)
	if len([]rune(payload.Draft)) != MaxBodyRunes {
		t.Fatalf("draft length = %d", len([]rune(payload.Draft)))
	}
}

func TestDraftsThatAreNotValidTextAreRefused(t *testing.T) {
	store := &storeStub{}
	err := NewMessageService(store).Typing(context.Background(), uuid.New(), uuid.New(), TypingUpdate{Draft: "\xff"})
	if !errors.Is(err, ErrInvalidMessage) || store.participantCalls != 0 {
		t.Fatalf("err = %v, store calls = %d", err, store.participantCalls)
	}
}

func TestMembershipIsCheckedOnceAndRecheckedAfterItExpires(t *testing.T) {
	store := &storeStub{participants: Participants{GuestID: uuid.New(), HostID: uuid.New()}}
	service := NewMessageService(store).WithPublisher(&publisherStub{}).WithFocus(&publisherStub{})
	clock := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	actor, conversation := store.participants.GuestID, uuid.New()

	for range 20 {
		clock = clock.Add(typingThrottle)
		_ = service.Typing(context.Background(), actor, conversation, TypingUpdate{Draft: "x"})
	}
	if store.participantCalls != 1 {
		t.Fatalf("keystrokes hit the database %d times, want 1", store.participantCalls)
	}
	clock = clock.Add(typingMembershipTTL)
	_ = service.Typing(context.Background(), actor, conversation, TypingUpdate{Draft: "xy"})
	if store.participantCalls != 2 {
		t.Fatalf("membership was not rechecked after it expired, calls = %d", store.participantCalls)
	}
}

func TestTypingOnlyGoesToTheOtherPersonsOpenChat(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)
	conversationID := uuid.New()

	_ = service.Typing(context.Background(), guestID, conversationID, TypingUpdate{Draft: "hi"})

	if len(publisher.events) != 1 || publisher.events[0].topic != ConversationTopic(conversationID) {
		t.Fatalf("typing must be scoped to the conversation topic, got %+v", publisher.events)
	}
}

func TestArrivingTellsTheOtherPersonAndHearsBackWhenTheyAreAlreadyThere(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	conversationID := uuid.New()
	topic := ConversationTopic(conversationID)
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{focused: map[uuid.UUID]string{hostID: topic}}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	service.FocusChanged(context.Background(), guestID, topic, true)

	if len(publisher.events) != 2 {
		t.Fatalf("events = %+v", publisher.events)
	}
	toHost, _ := publisher.events[0].payload.(PresenceEvent)
	toGuest, _ := publisher.events[1].payload.(PresenceEvent)
	if publisher.events[0].userID != hostID || toHost.Role != RoleGuest || !toHost.Here {
		t.Fatalf("host was told %+v", publisher.events[0])
	}
	if publisher.events[1].userID != guestID || toGuest.Role != RoleHost || !toGuest.Here {
		t.Fatalf("guest was told %+v", publisher.events[1])
	}
}

func TestArrivingAloneOnlyTellsTheOtherPerson(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	service.FocusChanged(context.Background(), guestID, ConversationTopic(uuid.New()), true)

	if len(publisher.events) != 1 || publisher.events[0].userID != hostID {
		t.Fatalf("events = %+v", publisher.events)
	}
}

func TestLeavingTellsTheOtherPerson(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	service.FocusChanged(context.Background(), hostID, ConversationTopic(uuid.New()), false)

	payload, _ := publisher.events[0].payload.(PresenceEvent)
	if publisher.events[0].userID != guestID || payload.Here || payload.Role != RoleHost {
		t.Fatalf("event = %+v", publisher.events[0])
	}
}

func TestOpeningSomeoneElsesChatAnnouncesNothing(t *testing.T) {
	store := &storeStub{participantsErr: ErrConversationNotFound}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher).WithFocus(publisher)

	service.FocusChanged(context.Background(), uuid.New(), ConversationTopic(uuid.New()), true)
	service.FocusChanged(context.Background(), uuid.New(), "listing:abc", true)

	if len(publisher.events) != 0 {
		t.Fatalf("events = %+v", publisher.events)
	}
}
