package payments

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/paymentintent"
)

type testCardState struct {
	name          string
	paymentMethod string
	wantStatus    stripe.PaymentIntentStatus
	wantDecline   string
}

var testCardStates = []testCardState{
	{"success", "pm_card_visa", stripe.PaymentIntentStatusSucceeded, ""},
	{"generic decline", "pm_card_chargeDeclined", stripe.PaymentIntentStatusRequiresPaymentMethod, "generic_decline"},
	{"insufficient funds", "pm_card_chargeDeclinedInsufficientFunds", stripe.PaymentIntentStatusRequiresPaymentMethod, "insufficient_funds"},
	{"expired card", "pm_card_chargeDeclinedExpiredCard", stripe.PaymentIntentStatusRequiresPaymentMethod, "expired_card"},
	{"incorrect cvc", "pm_card_chargeDeclinedIncorrectCvc", stripe.PaymentIntentStatusRequiresPaymentMethod, "incorrect_cvc"},
	{"fraudulent", "pm_card_chargeDeclinedFraudulent", stripe.PaymentIntentStatusRequiresPaymentMethod, "fraudulent"},
	{"3ds required", "pm_card_threeDSecure2Required", stripe.PaymentIntentStatusRequiresAction, ""},
}

func TestStripeTestModeCardStates(t *testing.T) {
	key := os.Getenv("STRIPE_SECRET_KEY")
	if !strings.HasPrefix(key, "sk_test_") {
		t.Skip("STRIPE_SECRET_KEY must be a sk_test_ key to run live Stripe test-mode checks")
	}
	gateway := NewStripeGateway(Config{SecretKey: key, WebhookSecret: "whsec_unused"})
	client := paymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: key}

	for _, state := range testCardStates {
		t.Run(state.name, func(t *testing.T) {
			bookingID, guestID := uuid.New(), uuid.New()
			intent, err := gateway.CreateIntent(context.Background(), IntentParams{
				AmountCents:    36000,
				Currency:       "usd",
				BookingID:      bookingID,
				GuestID:        guestID,
				IdempotencyKey: "live-test-" + uuid.NewString(),
			})
			if err != nil {
				t.Fatalf("CreateIntent() error = %v", err)
			}

			confirmed, err := client.Confirm(intent.ID, &stripe.PaymentIntentConfirmParams{
				PaymentMethod: stripe.String(state.paymentMethod),
				ReturnURL:     stripe.String("https://betterguests.com/book/return"),
			})
			if state.wantDecline == "" && err != nil {
				t.Fatalf("Confirm() error = %v", err)
			}
			if state.wantDecline != "" {
				stripeErr, ok := err.(*stripe.Error)
				if !ok || string(stripeErr.DeclineCode) != state.wantDecline && string(stripeErr.Code) != state.wantDecline {
					t.Fatalf("Confirm() error = %v, want decline %q", err, state.wantDecline)
				}
				confirmed, err = client.Get(intent.ID, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			if confirmed.Status != state.wantStatus {
				t.Fatalf("status = %s, want %s", confirmed.Status, state.wantStatus)
			}
			if confirmed.Metadata[MetadataBookingID] != bookingID.String() || confirmed.Metadata[MetadataGuestID] != guestID.String() {
				t.Fatalf("metadata = %v", confirmed.Metadata)
			}
		})
	}
}

func TestStripeCreateIntentIsIdempotent(t *testing.T) {
	key := os.Getenv("STRIPE_SECRET_KEY")
	if !strings.HasPrefix(key, "sk_test_") {
		t.Skip("STRIPE_SECRET_KEY must be a sk_test_ key to run live Stripe test-mode checks")
	}
	gateway := NewStripeGateway(Config{SecretKey: key, WebhookSecret: "whsec_unused"})
	params := IntentParams{AmountCents: 5000, Currency: "usd", BookingID: uuid.New(), GuestID: uuid.New(), IdempotencyKey: "live-idem-" + uuid.NewString()}
	first, err := gateway.CreateIntent(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	second, err := gateway.CreateIntent(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("intent ids differ: %s %s", first.ID, second.ID)
	}
}

func TestStripeReleasingAHoldTwiceSucceeds(t *testing.T) {
	key := os.Getenv("STRIPE_SECRET_KEY")
	if !strings.HasPrefix(key, "sk_test_") {
		t.Skip("STRIPE_SECRET_KEY must be a sk_test_ key to run live Stripe test-mode checks")
	}
	gateway := NewStripeGateway(Config{SecretKey: key, WebhookSecret: "whsec_unused"})
	intent, err := gateway.CreateIntent(context.Background(), IntentParams{AmountCents: 5000, Currency: "usd", BookingID: uuid.New(), GuestID: uuid.New(), IdempotencyKey: "live-release-" + uuid.NewString(), ManualCapture: true})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if err := gateway.Cancel(context.Background(), intent.ID); err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
	}
}
