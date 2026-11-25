package payments

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type controllerStub struct {
	captured []string
	canceled []string
	err      error
}

func (stub *controllerStub) Capture(_ context.Context, intentID string) error {
	stub.captured = append(stub.captured, intentID)
	return stub.err
}

func (stub *controllerStub) Cancel(_ context.Context, intentID string) error {
	stub.canceled = append(stub.canceled, intentID)
	return stub.err
}

type publishedEvent struct {
	userID  uuid.UUID
	kind    string
	payload any
}

type publisherStub struct{ events []publishedEvent }

func (stub *publisherStub) Publish(userID uuid.UUID, kind string, payload any) {
	stub.events = append(stub.events, publishedEvent{userID, kind, payload})
}

func TestRequestedBookingCheckoutOnlyHoldsTheCard(t *testing.T) {
	quote := pricedQuote()
	quote.BookingStatus = "requested"
	store := &fakeStore{quote: quote}
	intents := &fakeIntents{}

	if _, err := newService(store, intents, fakeParser{}).StartCheckout(context.Background(), uuid.New(), quote.BookingID); err != nil {
		t.Fatal(err)
	}
	if len(intents.params) != 1 || !intents.params[0].ManualCapture {
		t.Fatalf("a requested booking must be authorized now and captured later: %+v", intents.params)
	}
}

func TestApprovedBookingCheckoutChargesImmediately(t *testing.T) {
	store := &fakeStore{quote: pricedQuote()}
	intents := &fakeIntents{}

	if _, err := newService(store, intents, fakeParser{}).StartCheckout(context.Background(), uuid.New(), store.quote.BookingID); err != nil {
		t.Fatal(err)
	}
	if intents.params[0].ManualCapture {
		t.Fatal("an already approved booking must be charged straight away")
	}
}

func TestCheckoutRefusesABookingWhoseCardIsAlreadyOnHold(t *testing.T) {
	quote := pricedQuote()
	quote.BookingStatus = "requested"
	store := &fakeStore{quote: quote, hasPayment: true, existing: Payment{Status: StatusAuthorized}}
	intents := &fakeIntents{}

	_, err := newService(store, intents, fakeParser{}).StartCheckout(context.Background(), uuid.New(), quote.BookingID)

	if !errors.Is(err, ErrAlreadyPaid) || len(intents.params) != 0 {
		t.Fatalf("error = %v, intents = %d", err, len(intents.params))
	}
}

func TestCaptureOnlyTouchesAnAuthorizedHold(t *testing.T) {
	tests := []struct {
		name    string
		store   *fakeStore
		wantErr bool
	}{
		{"authorized", &fakeStore{hasHold: true, hold: PaymentHold{IntentID: "pi_1", Status: StatusAuthorized}}, false},
		{"still pending", &fakeStore{hasHold: true, hold: PaymentHold{IntentID: "pi_1", Status: StatusPending}}, true},
		{"already charged", &fakeStore{hasHold: true, hold: PaymentHold{IntentID: "pi_1", Status: StatusSucceeded}}, true},
		{"no payment", &fakeStore{}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := &controllerStub{}
			service := newService(test.store, &fakeIntents{}, fakeParser{}).WithController(controller)

			err := service.Capture(context.Background(), uuid.New(), uuid.New())

			if test.wantErr != errors.Is(err, ErrNoAuthorizedPayment) {
				t.Fatalf("error = %v", err)
			}
			if test.wantErr && len(controller.captured) != 0 {
				t.Fatal("nothing may be captured without an authorized hold")
			}
			if !test.wantErr && (len(controller.captured) != 1 || controller.captured[0] != "pi_1") {
				t.Fatalf("captured = %v", controller.captured)
			}
		})
	}
}

func TestReleaseCancelsOnlyLiveHolds(t *testing.T) {
	tests := []struct {
		name       string
		store      *fakeStore
		wantCancel bool
	}{
		{"authorized", &fakeStore{hasHold: true, hold: PaymentHold{IntentID: "pi_1", Status: StatusAuthorized}}, true},
		{"pending", &fakeStore{hasHold: true, hold: PaymentHold{IntentID: "pi_1", Status: StatusPending}}, true},
		{"already charged", &fakeStore{hasHold: true, hold: PaymentHold{IntentID: "pi_1", Status: StatusSucceeded}}, false},
		{"already canceled", &fakeStore{hasHold: true, hold: PaymentHold{IntentID: "pi_1", Status: StatusCanceled}}, false},
		{"no payment", &fakeStore{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := &controllerStub{}
			service := newService(test.store, &fakeIntents{}, fakeParser{}).WithController(controller)

			if err := service.Release(context.Background(), uuid.New(), uuid.New()); err != nil {
				t.Fatal(err)
			}
			if (len(controller.canceled) == 1) != test.wantCancel {
				t.Fatalf("canceled = %v, want cancel = %v", controller.canceled, test.wantCancel)
			}
		})
	}
}

func TestAuthorizedEventTellsTheHostAboutTheRequest(t *testing.T) {
	hostID, guestID, bookingID := uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{quote: Quote{HostID: hostID}}
	publisher := &publisherStub{}
	parser := fakeParser{event: Event{Type: EventAuthorized, IntentID: "pi_1", GuestID: guestID, BookingID: bookingID}}
	service := newService(store, &fakeIntents{}, parser).WithPublisher(publisher)

	if err := service.HandleWebhook(context.Background(), "sig", nil); err != nil {
		t.Fatal(err)
	}
	if len(store.applied) != 1 || store.applied[0].status != StatusAuthorized {
		t.Fatalf("applied = %+v", store.applied)
	}
	if len(publisher.events) != 1 || publisher.events[0].userID != hostID || publisher.events[0].kind != "booking" {
		t.Fatalf("events = %+v", publisher.events)
	}
}

func TestSucceededEventDoesNotRepeatTheHostAnnouncement(t *testing.T) {
	store := &fakeStore{}
	publisher := &publisherStub{}
	parser := fakeParser{event: Event{Type: EventSucceeded, IntentID: "pi_1", GuestID: uuid.New(), BookingID: uuid.New()}}

	if err := newService(store, &fakeIntents{}, parser).WithPublisher(publisher).HandleWebhook(context.Background(), "sig", nil); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("events = %+v", publisher.events)
	}
}
