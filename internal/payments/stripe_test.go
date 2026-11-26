package payments

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

const testWebhookSecret = "whsec_unit_test_secret"

func signedPayload(t *testing.T, eventType string, metadata map[string]string, signedAt time.Time) ([]byte, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":          "evt_1",
		"object":      "event",
		"type":        eventType,
		"created":     signedAt.Unix(),
		"api_version": "2025-01-27.acacia",
		"data": map[string]any{
			"object": map[string]any{"id": "pi_1", "object": "payment_intent", "metadata": metadata},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   body,
		Secret:    testWebhookSecret,
		Timestamp: signedAt,
	})
	return body, signed.Header
}

func TestParseEventReadsIntentAndMetadata(t *testing.T) {
	guestID, bookingID := uuid.New(), uuid.New()
	body, header := signedPayload(t, EventSucceeded, map[string]string{
		MetadataGuestID:   guestID.String(),
		MetadataBookingID: bookingID.String(),
	}, time.Now())

	event, err := NewStripeGateway(Config{WebhookSecret: testWebhookSecret}).ParseEvent(body, header)
	if err != nil {
		t.Fatalf("ParseEvent() error = %v", err)
	}
	if event.IntentID != "pi_1" || event.GuestID != guestID || event.BookingID != bookingID {
		t.Fatalf("event = %+v", event)
	}
}

func TestParseEventRejectsTamperedBody(t *testing.T) {
	body, header := signedPayload(t, EventSucceeded, map[string]string{}, time.Now())
	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] = 'X'
	_, err := NewStripeGateway(Config{WebhookSecret: testWebhookSecret}).ParseEvent(tampered, header)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseEventRejectsWrongSecret(t *testing.T) {
	body, header := signedPayload(t, EventSucceeded, map[string]string{}, time.Now())
	_, err := NewStripeGateway(Config{WebhookSecret: "whsec_other"}).ParseEvent(body, header)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseEventRejectsMissingSignature(t *testing.T) {
	body, _ := signedPayload(t, EventSucceeded, map[string]string{}, time.Now())
	_, err := NewStripeGateway(Config{WebhookSecret: testWebhookSecret}).ParseEvent(body, "")
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseEventRejectsReplayedOldTimestamp(t *testing.T) {
	body, header := signedPayload(t, EventSucceeded, map[string]string{}, time.Now().Add(-time.Hour))
	_, err := NewStripeGateway(Config{WebhookSecret: testWebhookSecret}).ParseEvent(body, header)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v", err)
	}
}

func TestCancellingAnAlreadyCanceledIntentIsNotAFailure(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"already canceled": {&stripe.Error{Code: stripe.ErrorCodePaymentIntentUnexpectedState, PaymentIntent: &stripe.PaymentIntent{Status: stripe.PaymentIntentStatusCanceled}}, true},
		"already charged":  {&stripe.Error{Code: stripe.ErrorCodePaymentIntentUnexpectedState, PaymentIntent: &stripe.PaymentIntent{Status: stripe.PaymentIntentStatusSucceeded}}, false},
		"no intent":        {&stripe.Error{Code: stripe.ErrorCodePaymentIntentUnexpectedState}, false},
		"network":          {errors.New("timeout"), false},
	}
	for name, test := range cases {
		if got := alreadyCanceled(test.err); got != test.want {
			t.Fatalf("%s: alreadyCanceled = %v", name, got)
		}
	}
}
