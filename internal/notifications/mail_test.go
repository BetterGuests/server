package notifications

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type mailQueueStub struct {
	pending  []PendingEmail
	attempts map[uuid.UUID]bool
}

func (stub *mailQueueStub) AwaitingEmail(context.Context, int) ([]PendingEmail, error) {
	return stub.pending, nil
}

func (stub *mailQueueStub) RecordEmailAttempt(_ context.Context, id uuid.UUID, delivered bool) error {
	stub.attempts[id] = delivered
	return nil
}

type senderStub struct {
	sent []Email
	fail string
}

func (stub *senderStub) Send(_ context.Context, email Email) error {
	if email.To == stub.fail {
		return errors.New("rejected")
	}
	stub.sent = append(stub.sent, email)
	return nil
}

type addressStub struct{}

func (addressStub) VerifiedEmail(context.Context, uuid.UUID) (string, error) {
	return "host@example.com", nil
}

func TestTeamMailGoesToTheAdminAddressAndOneFailureDoesNotStopTheRest(t *testing.T) {
	team := PendingEmail{NotificationID: uuid.New(), Kind: kindReportFiled, Title: "Report", Body: "Look", ForTeam: true, TeamEmail: "team@example.com"}
	host := PendingEmail{NotificationID: uuid.New(), Kind: kindListingPaused, Title: "Paused", Body: "Why"}
	queue := &mailQueueStub{pending: []PendingEmail{team, host}, attempts: map[uuid.UUID]bool{}}
	sender := &senderStub{fail: "team@example.com"}
	sent, err := NewMailService(queue, sender, addressStub{}, "https://app.example.com/").SendDue(context.Background())

	if sent != 1 || err == nil || queue.attempts[team.NotificationID] || !queue.attempts[host.NotificationID] {
		t.Fatalf("sent=%d err=%v attempts=%v", sent, err, queue.attempts)
	}
	mail := sender.sent[0]
	if mail.To != "host@example.com" || !strings.HasSuffix(mail.Text, "https://app.example.com/hosting") ||
		mail.IdempotencyKey != "notification/"+host.NotificationID.String() {
		t.Fatalf("mail = %+v", mail)
	}
}
