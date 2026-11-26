package messages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type inboxStub struct {
	summaries   []Summary
	summary     Summary
	page        MessagePage
	err         error
	askedBefore *time.Time
	askedLimit  int
	markedRead  bool
}

func (stub *inboxStub) List(context.Context, uuid.UUID) ([]Summary, error) {
	return stub.summaries, stub.err
}

func (stub *inboxStub) Get(context.Context, uuid.UUID, uuid.UUID) (Summary, error) {
	return stub.summary, stub.err
}

func (stub *inboxStub) Messages(_ context.Context, _, _ uuid.UUID, before *time.Time, limit int) (MessagePage, error) {
	stub.askedBefore = before
	stub.askedLimit = limit
	return stub.page, stub.err
}

func (stub *inboxStub) MarkRead(context.Context, uuid.UUID, uuid.UUID) error {
	stub.markedRead = stub.err == nil
	return stub.err
}

type openerStub struct {
	summary Summary
	err     error
	target  OpenTarget
}

func (stub *openerStub) Open(_ context.Context, _ uuid.UUID, target OpenTarget) (Summary, error) {
	stub.target = target
	return stub.summary, stub.err
}

type senderStub struct {
	message            Message
	err                error
	body               string
	draft              Draft
	deleted            uuid.UUID
	emoji              string
	reactions          []Reaction
	typed              bool
	typingConversation uuid.UUID
	typingUpdate       TypingUpdate
}

func (stub *senderStub) Send(_ context.Context, _, _ uuid.UUID, draft Draft) (Message, error) {
	stub.body = draft.Body
	stub.draft = draft
	return stub.message, stub.err
}

func (stub *senderStub) Delete(_ context.Context, _, _, messageID uuid.UUID) error {
	stub.deleted = messageID
	return stub.err
}

func (stub *senderStub) React(_ context.Context, _, _, _ uuid.UUID, emoji string) ([]Reaction, error) {
	stub.emoji = emoji
	return stub.reactions, stub.err
}

func (stub *senderStub) Typing(_ context.Context, _, conversationID uuid.UUID, update TypingUpdate) error {
	stub.typed = stub.err == nil
	stub.typingConversation = conversationID
	stub.typingUpdate = update
	return stub.err
}

func newHandler(inbox *inboxStub, opener *openerStub, sender *senderStub) *HTTPHandler {
	return NewHTTPHandler(inbox, opener, sender)
}

func request(method, target, body string, authenticated bool) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.SetPathValue("conversation_id", uuid.NewString())
	if authenticated {
		r = r.WithContext(accounts.WithUserID(r.Context(), uuid.New()))
	}
	return r
}

func TestEveryRouteRequiresAnAuthenticatedUser(t *testing.T) {
	handler := newHandler(&inboxStub{}, &openerStub{}, &senderStub{})
	routes := map[string]http.HandlerFunc{
		"list":     handler.ListConversations,
		"open":     handler.OpenConversation,
		"get":      handler.GetConversation,
		"messages": handler.ListMessages,
		"send":     handler.SendMessage,
		"read":     handler.MarkRead,
		"delete":   handler.DeleteMessage,
		"react":    handler.ReactToMessage,
		"typing":   handler.Typing,
	}
	for name, serve := range routes {
		response := httptest.NewRecorder()
		serve(response, request(http.MethodGet, "/", `{}`, false))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s returned %d without a user, want 401", name, response.Code)
		}
	}
}

func TestConversationIDMustBeAUUID(t *testing.T) {
	handler := newHandler(&inboxStub{}, &openerStub{}, &senderStub{})
	r := request(http.MethodGet, "/", "", true)
	r.SetPathValue("conversation_id", "not-a-uuid")
	response := httptest.NewRecorder()

	handler.GetConversation(response, r)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestSendMessageReturnsCreated(t *testing.T) {
	sender := &senderStub{message: Message{ID: uuid.New(), Body: "hi", SenderRole: RoleGuest}}
	handler := newHandler(&inboxStub{}, &openerStub{}, sender)
	response := httptest.NewRecorder()

	handler.SendMessage(response, request(http.MethodPost, "/", `{"body":"hi"}`, true))

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if sender.body != "hi" {
		t.Fatalf("service received %q", sender.body)
	}
	if strings.Contains(response.Body.String(), "sender_id") {
		t.Fatal("sender account ids must not leak into the response")
	}
}

func TestSendMessageRejectsMalformedBodies(t *testing.T) {
	tests := map[string]string{
		"not json":      `nope`,
		"unknown field": `{"body":"hi","sender_id":"` + uuid.NewString() + `"}`,
		"two objects":   `{"body":"hi"}{"body":"again"}`,
		"oversized":     `{"body":"` + strings.Repeat("a", maxRequestBytes) + `"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			sender := &senderStub{}
			handler := newHandler(&inboxStub{}, &openerStub{}, sender)
			response := httptest.NewRecorder()

			handler.SendMessage(response, request(http.MethodPost, "/", body, true))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.Code)
			}
			if sender.body != "" {
				t.Fatal("a malformed request must not reach the service")
			}
		})
	}
}

func TestServiceErrorsMapToStatusCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not found", ErrConversationNotFound, http.StatusNotFound},
		{"invalid message", ErrInvalidMessage, http.StatusBadRequest},
		{"invalid conversation", ErrInvalidConversation, http.StatusBadRequest},
		{"unexpected", errors.New("database down"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newHandler(&inboxStub{}, &openerStub{}, &senderStub{err: test.err})
			response := httptest.NewRecorder()

			handler.SendMessage(response, request(http.MethodPost, "/", `{"body":"hi"}`, true))

			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			if test.want == http.StatusInternalServerError && strings.Contains(response.Body.String(), "database") {
				t.Fatal("internal error detail must not reach the client")
			}
		})
	}
}

func TestListMessagesParsesTheCursorAndLimit(t *testing.T) {
	inbox := &inboxStub{page: MessagePage{Messages: []Message{}}}
	handler := newHandler(inbox, &openerStub{}, &senderStub{})
	response := httptest.NewRecorder()

	handler.ListMessages(response, request(http.MethodGet, "/?before=2026-06-01T12:00:00.5Z&limit=20", "", true))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if inbox.askedLimit != 20 || inbox.askedBefore == nil || inbox.askedBefore.Nanosecond() != 500_000_000 {
		t.Fatalf("limit = %d, before = %v", inbox.askedLimit, inbox.askedBefore)
	}
}

func TestListMessagesRejectsBadPagingParameters(t *testing.T) {
	for _, query := range []string{"before=yesterday", "limit=abc", "limit=0", "limit=-3"} {
		handler := newHandler(&inboxStub{}, &openerStub{}, &senderStub{})
		response := httptest.NewRecorder()

		handler.ListMessages(response, request(http.MethodGet, "/?"+query, "", true))

		if response.Code != http.StatusBadRequest {
			t.Fatalf("%q returned %d, want 400", query, response.Code)
		}
	}
}

func TestOpenConversationPassesTheTarget(t *testing.T) {
	opener := &openerStub{summary: Summary{ID: uuid.New()}}
	handler := newHandler(&inboxStub{}, opener, &senderStub{})
	listingID := uuid.New()
	response := httptest.NewRecorder()

	handler.OpenConversation(response, request(http.MethodPost, "/", `{"listing_id":"`+listingID.String()+`"}`, true))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if opener.target.ListingID != listingID || opener.target.BookingID != uuid.Nil {
		t.Fatalf("target = %+v", opener.target)
	}
}

func TestSummaryJSONHidesAccountIDs(t *testing.T) {
	inbox := &inboxStub{summaries: []Summary{{
		ID: uuid.New(), Role: RoleGuest, CounterpartyName: "Host",
		Participants: Participants{GuestID: uuid.New(), HostID: uuid.New()},
	}}}
	handler := newHandler(inbox, &openerStub{}, &senderStub{})
	response := httptest.NewRecorder()

	handler.ListConversations(response, request(http.MethodGet, "/", "", true))

	body := response.Body.String()
	if strings.Contains(body, "GuestID") || strings.Contains(body, "guest_id") || strings.Contains(body, "host_id") {
		t.Fatalf("account ids leaked: %s", body)
	}
	if !strings.Contains(body, `"unread_count":0`) {
		t.Fatalf("unread_count missing: %s", body)
	}
}

func TestMarkReadReturnsNoContent(t *testing.T) {
	inbox := &inboxStub{}
	handler := newHandler(inbox, &openerStub{}, &senderStub{})
	response := httptest.NewRecorder()

	handler.MarkRead(response, request(http.MethodPost, "/", "", true))

	if response.Code != http.StatusNoContent || !inbox.markedRead {
		t.Fatalf("status = %d, marked = %v", response.Code, inbox.markedRead)
	}
}

func messageRequest(method, body, messageID string) *http.Request {
	r := request(method, "/", body, true)
	r.SetPathValue("message_id", messageID)
	return r
}

func TestSendMessagePassesTheClientIDAndReplyTarget(t *testing.T) {
	sender := &senderStub{}
	handler := newHandler(&inboxStub{}, &openerStub{}, sender)
	id, replyTo := uuid.New(), uuid.New()
	response := httptest.NewRecorder()

	handler.SendMessage(response, request(http.MethodPost, "/", `{"id":"`+id.String()+`","body":"hi","reply_to_id":"`+replyTo.String()+`"}`, true))

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if sender.draft.ID != id || sender.draft.ReplyToID != replyTo {
		t.Fatalf("draft = %+v", sender.draft)
	}
}

func TestDeleteMessageReturnsNoContent(t *testing.T) {
	sender := &senderStub{}
	handler := newHandler(&inboxStub{}, &openerStub{}, sender)
	messageID := uuid.New()
	response := httptest.NewRecorder()

	handler.DeleteMessage(response, messageRequest(http.MethodDelete, "", messageID.String()))

	if response.Code != http.StatusNoContent || sender.deleted != messageID {
		t.Fatalf("status = %d, deleted = %s", response.Code, sender.deleted)
	}
}

func TestDeleteMessageThatIsNotYoursIsNotFound(t *testing.T) {
	handler := newHandler(&inboxStub{}, &openerStub{}, &senderStub{err: ErrMessageNotFound})
	response := httptest.NewRecorder()

	handler.DeleteMessage(response, messageRequest(http.MethodDelete, "", uuid.NewString()))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestMessageIDMustBeAUUID(t *testing.T) {
	handler := newHandler(&inboxStub{}, &openerStub{}, &senderStub{})
	response := httptest.NewRecorder()

	handler.ReactToMessage(response, messageRequest(http.MethodPost, `{"emoji":"👍"}`, "nope"))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestReactToMessageReturnsTheReactions(t *testing.T) {
	sender := &senderStub{reactions: []Reaction{{Emoji: "👍", Role: RoleHost}}}
	handler := newHandler(&inboxStub{}, &openerStub{}, sender)
	response := httptest.NewRecorder()

	handler.ReactToMessage(response, messageRequest(http.MethodPost, `{"emoji":"👍"}`, uuid.NewString()))

	if response.Code != http.StatusOK || sender.emoji != "👍" {
		t.Fatalf("status = %d, emoji = %q", response.Code, sender.emoji)
	}
	if !strings.Contains(response.Body.String(), `"role":"host"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestUnknownReactionIsABadRequest(t *testing.T) {
	handler := newHandler(&inboxStub{}, &openerStub{}, &senderStub{err: ErrInvalidReaction})
	response := httptest.NewRecorder()

	handler.ReactToMessage(response, messageRequest(http.MethodPost, `{"emoji":"🦄"}`, uuid.NewString()))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestTypingReturnsNoContent(t *testing.T) {
	sender := &senderStub{}
	handler := newHandler(&inboxStub{}, &openerStub{}, sender)
	response := httptest.NewRecorder()

	handler.Typing(response, request(http.MethodPost, "/", "", true))

	if response.Code != http.StatusNoContent || !sender.typed {
		t.Fatalf("status = %d, typed = %v", response.Code, sender.typed)
	}
}

func TestTypingFrameFromTheSocketCarriesTheDraft(t *testing.T) {
	typist := &senderStub{}
	conversationID := uuid.New()

	err := NewTypingReceiver(typist).Receive(context.Background(), uuid.New(),
		[]byte(`{"conversation_id":"`+conversationID.String()+`","draft":"See you at 4"}`))

	if err != nil {
		t.Fatal(err)
	}
	if typist.typingConversation != conversationID || typist.typingUpdate.Draft != "See you at 4" || typist.typingUpdate.Idle {
		t.Fatalf("typing = %s %+v", typist.typingConversation, typist.typingUpdate)
	}
}

func TestTypingFrameWithoutAConversationIsRefused(t *testing.T) {
	typist := &senderStub{}

	err := NewTypingReceiver(typist).Receive(context.Background(), uuid.New(), []byte(`{"draft":"hi"}`))

	if !errors.Is(err, ErrInvalidConversation) || typist.typed {
		t.Fatalf("err = %v, typed = %v", err, typist.typed)
	}
}

func TestHTTPTypingPingCarriesNoDraft(t *testing.T) {
	typist := &senderStub{}
	handler := newHandler(&inboxStub{}, &openerStub{}, typist)

	handler.Typing(httptest.NewRecorder(), request(http.MethodPost, "/", "", true))

	if typist.typingUpdate != (TypingUpdate{}) {
		t.Fatalf("update = %+v", typist.typingUpdate)
	}
}
