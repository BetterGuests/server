package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestOptionalEmailsCarryAWayOutAndWaitUntilThereIsOne(t *testing.T) {
	conversation, token := uuid.New(), uuid.New()
	pending := []PendingEmail{
		{NotificationID: uuid.New(), Kind: Kind(kindNewMessage), Title: "New message", ConversationID: conversation, Category: categoryMessages, Unsubscribe: token, AsHost: true},
		{NotificationID: uuid.New(), Kind: KindBookingRequested, Title: "New booking request"},
	}
	for _, apiURL := range []string{"", "https://api.example.com/"} {
		queue := &mailQueueStub{pending: pending, attempts: map[uuid.UUID]bool{}}
		sender := &senderStub{}
		service := NewMailService(queue, sender, addressStub{}, "https://example.com").WithUnsubscribeLinks(apiURL)

		if _, err := service.SendDue(context.Background()); err != nil {
			t.Fatal(err)
		}

		if apiURL == "" {
			if len(sender.sent) != 1 || sender.sent[0].UnsubscribeURL != "" || len(queue.attempts) != 1 {
				t.Fatalf("without a link only the booking email goes: %+v %v", sender.sent, queue.attempts)
			}
			if !strings.HasSuffix(sender.sent[0].Text, "https://example.com/hosting/requests") {
				t.Fatalf("text = %q", sender.sent[0].Text)
			}
			continue
		}
		message := sender.sent[0]
		wanted := "https://api.example.com/api/v1/email/unsubscribe?category=messages&token=" + token.String()
		if message.UnsubscribeURL != wanted || !strings.Contains(message.Text, wanted) {
			t.Fatalf("unsubscribe = %q", message.UnsubscribeURL)
		}
		if !strings.Contains(message.Text, "https://example.com/hosting/messages/"+conversation.String()) {
			t.Fatalf("text = %q", message.Text)
		}
	}
}

type unsubscriberStub struct{ calls []string }

func (stub *unsubscriberStub) Unsubscribe(_ context.Context, _ uuid.UUID, category string) (bool, error) {
	stub.calls = append(stub.calls, category)
	return true, nil
}

func TestOpeningTheLinkAsksFirstAndOnlyPostingUnsubscribes(t *testing.T) {
	stub := &unsubscriberStub{}
	handler := NewHTTPHandler(nil).WithUnsubscriber(stub)
	target := unsubscribePath + "?category=reminders&token=" + uuid.NewString()

	opened := httptest.NewRecorder()
	handler.UnsubscribePage(opened, httptest.NewRequest(http.MethodGet, target, nil))
	if opened.Code != http.StatusOK || len(stub.calls) != 0 {
		t.Fatalf("a link scanner must not unsubscribe: %d %v", opened.Code, stub.calls)
	}

	posted := httptest.NewRecorder()
	handler.Unsubscribe(posted, httptest.NewRequest(http.MethodPost, target, nil))
	if posted.Code != http.StatusOK || len(stub.calls) != 1 {
		t.Fatalf("post: %d %v", posted.Code, stub.calls)
	}

	for _, bad := range []string{"?category=booking&token=" + uuid.NewString(), "?category=reminders&token=nope"} {
		response := httptest.NewRecorder()
		handler.Unsubscribe(response, httptest.NewRequest(http.MethodPost, unsubscribePath+bad, nil))
		if response.Code != http.StatusNotFound || len(stub.calls) != 1 {
			t.Fatalf("%s: %d %v", bad, response.Code, stub.calls)
		}
	}
}
