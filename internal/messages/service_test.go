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
	openID       uuid.UUID
	openErr      error
	openedTarget OpenTarget
	summary      Summary
	getErr       error
	page         []Message
	pageErr      error
	askedLimit   int
	askedBefore  *time.Time
	inserted     Message
	participants Participants
	insertErr    error
	insertCalls  int
	readErr      error
	readCalls    int
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

func (stub *storeStub) Insert(_ context.Context, actorID, conversationID, messageID uuid.UUID, body string, now time.Time) (Message, Participants, error) {
	stub.insertCalls++
	if stub.insertErr != nil {
		return Message{}, Participants{}, stub.insertErr
	}
	stub.inserted = Message{ID: messageID, ConversationID: conversationID, SenderID: actorID, SenderRole: RoleGuest, Body: body, CreatedAt: now}
	return stub.inserted, stub.participants, nil
}

func (stub *storeStub) MarkRead(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	stub.readCalls++
	return stub.readErr
}

type publishedEvent struct {
	userID  uuid.UUID
	kind    string
	payload any
}

type publisherStub struct{ events []publishedEvent }

func (stub *publisherStub) Publish(userID uuid.UUID, kind string, payload any) {
	stub.events = append(stub.events, publishedEvent{userID: userID, kind: kind, payload: payload})
}

func TestSendStoresTrimmedBodyAndTellsBothParticipants(t *testing.T) {
	guestID, hostID := uuid.New(), uuid.New()
	store := &storeStub{participants: Participants{GuestID: guestID, HostID: hostID}}
	publisher := &publisherStub{}
	service := NewMessageService(store).WithPublisher(publisher)
	conversationID := uuid.New()

	message, err := service.Send(context.Background(), guestID, conversationID, "  Is early check-in possible?  \n")
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
		"too long":        strings.Repeat("a", maxBodyRunes+1),
		"invalid utf-8":   "hello \xff",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			store := &storeStub{}
			publisher := &publisherStub{}
			service := NewMessageService(store).WithPublisher(publisher)

			_, err := service.Send(context.Background(), uuid.New(), uuid.New(), body)

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

	if _, err := service.Send(context.Background(), uuid.New(), uuid.New(), strings.Repeat("é", maxBodyRunes)); err != nil {
		t.Fatalf("a body of %d characters must be accepted: %v", maxBodyRunes, err)
	}
}

func TestSendToAConversationThePersonIsNotIn(t *testing.T) {
	publisher := &publisherStub{}
	service := NewMessageService(&storeStub{insertErr: ErrConversationNotFound}).WithPublisher(publisher)

	_, err := service.Send(context.Background(), uuid.New(), uuid.New(), "hello")

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
