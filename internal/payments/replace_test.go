package payments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestASecondCheckoutNeverLeavesAnotherPaymentOpen(t *testing.T) {
	quote := pricedQuote()
	quote.BookingStatus = bookingRequested
	amount := quote.Nights * quote.NightlyPriceCents
	earlier := func(status Status, cents int) IntentState {
		return IntentState{Intent: Intent{ID: "pi_old", ClientSecret: "pi_old_secret"}, Status: status, AmountCents: cents, ManualCapture: true}
	}
	tests := []struct {
		name         string
		controller   *controllerStub
		wantErr      error
		wantIntent   string
		wantCanceled bool
		wantRecorded Status
	}{
		{name: "an open payment that still fits is reused", controller: &controllerStub{earlier: earlier(StatusPending, amount)}, wantIntent: "pi_old"},
		{name: "an open payment for another amount is canceled first", controller: &controllerStub{earlier: earlier(StatusPending, amount-100)}, wantIntent: "pi_test_1", wantCanceled: true},
		{name: "a voided hold gets a new payment", controller: &controllerStub{earlier: earlier(StatusCanceled, amount)}, wantIntent: "pi_test_1"},
		{name: "a payment Stripe no longer has is replaced", controller: &controllerStub{missing: true}, wantIntent: "pi_test_1"},
		{name: "a hold the webhook never reported is recorded", controller: &controllerStub{earlier: earlier(StatusAuthorized, amount)}, wantErr: ErrAlreadyPaid, wantRecorded: StatusAuthorized},
		{name: "a payment still processing is not doubled", controller: &controllerStub{earlier: earlier(StatusProcessing, amount)}, wantErr: ErrAlreadyPaid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{quote: quote, hasPayment: true, existing: Payment{StripePaymentIntentID: "pi_old", Status: StatusPending}}
			intents := &fakeIntents{}
			service := newService(store, intents, fakeParser{}).WithController(test.controller)

			result, err := service.StartCheckout(context.Background(), uuid.New(), quote.BookingID)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v", err)
			}
			if result.PaymentIntentID != test.wantIntent {
				t.Fatalf("intent = %q", result.PaymentIntentID)
			}
			if (len(test.controller.canceled) == 1) != test.wantCanceled {
				t.Fatalf("canceled = %v", test.controller.canceled)
			}
			if test.wantRecorded != "" && (len(store.applied) != 1 || store.applied[0].status != test.wantRecorded) {
				t.Fatalf("applied = %+v", store.applied)
			}
			if test.wantIntent == "pi_old" && len(intents.params) != 0 {
				t.Fatalf("created = %+v", intents.params)
			}
		})
	}
}

func TestAReplacementIsNotAnsweredWithTheOldCachedPayment(t *testing.T) {
	quote := pricedQuote()
	quote.BookingStatus = bookingRequested
	store := &fakeStore{quote: quote, hasPayment: true, existing: Payment{StripePaymentIntentID: "pi_old", Status: StatusCanceled}}
	intents := &fakeIntents{}
	controller := &controllerStub{earlier: IntentState{Intent: Intent{ID: "pi_old"}, Status: StatusCanceled}}

	if _, err := newService(store, intents, fakeParser{}).WithController(controller).StartCheckout(context.Background(), uuid.New(), quote.BookingID); err != nil {
		t.Fatal(err)
	}
	if len(intents.params) != 1 || !strings.HasSuffix(intents.params[0].IdempotencyKey, "-after-pi_old") {
		t.Fatalf("params = %+v", intents.params)
	}
}

func TestAHoldOnAReplacedPaymentIsReleased(t *testing.T) {
	tests := []struct {
		name         string
		current      string
		hasPayment   bool
		wantCanceled bool
	}{
		{name: "the booking moved to another payment", current: "pi_new", hasPayment: true, wantCanceled: true},
		{name: "the booking is unknown here, as with a shared sandbox", wantCanceled: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{applyErr: ErrPaymentNotFound, hasPayment: test.hasPayment, existing: Payment{StripePaymentIntentID: test.current}}
			event := Event{ID: "evt_1", Type: EventAuthorized, IntentID: "pi_old", GuestID: uuid.New(), BookingID: uuid.New()}
			controller := &controllerStub{}
			service := newService(store, &fakeIntents{}, fakeParser{event: event}).WithController(controller)

			if err := service.HandleWebhook(context.Background(), "sig", nil); err != nil {
				t.Fatal(err)
			}
			if (len(controller.canceled) == 1) != test.wantCanceled {
				t.Fatalf("canceled = %v", controller.canceled)
			}
		})
	}
}
