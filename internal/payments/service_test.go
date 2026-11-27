package payments

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	quote      Quote
	quoteErr   error
	existing   Payment
	hasPayment bool
	created    []Payment
	applied    []appliedOutcome
	applyErr   error
	hold       PaymentHold
	hasHold    bool
}

type appliedOutcome struct {
	guestID  uuid.UUID
	intentID string
	status   Status
}

func (store *fakeStore) QuoteBooking(context.Context, uuid.UUID, uuid.UUID) (Quote, error) {
	return store.quote, store.quoteErr
}

func (store *fakeStore) ExistingPayment(context.Context, uuid.UUID, uuid.UUID) (Payment, bool, error) {
	return store.existing, store.hasPayment, nil
}

func (store *fakeStore) CreatePayment(_ context.Context, payment Payment) error {
	store.created = append(store.created, payment)
	return nil
}

func (store *fakeStore) ApplyOutcome(_ context.Context, guestID uuid.UUID, intentID string, status Status, _ time.Time) error {
	store.applied = append(store.applied, appliedOutcome{guestID, intentID, status})
	return store.applyErr
}

func (store *fakeStore) HoldForBooking(context.Context, uuid.UUID, uuid.UUID) (PaymentHold, bool, error) {
	return store.hold, store.hasHold, nil
}

type fakeIntents struct {
	params []IntentParams
	err    error
}

func (intents *fakeIntents) CreateIntent(_ context.Context, params IntentParams) (Intent, error) {
	intents.params = append(intents.params, params)
	return Intent{ID: "pi_test_1", ClientSecret: "pi_test_1_secret"}, intents.err
}

type fakeParser struct {
	event Event
	err   error
}

func (parser fakeParser) ParseEvent([]byte, string) (Event, error) {
	return parser.event, parser.err
}

func pricedQuote() Quote {
	return Quote{BookingID: uuid.New(), BookingStatus: "pending", Nights: 3, NightlyPriceCents: 12000, StartsOn: time.Now().AddDate(0, 0, 7)}
}

func newService(store *fakeStore, intents *fakeIntents, parser fakeParser) *PaymentService {
	return NewPaymentService(store, intents, parser, Config{})
}

func TestStartCheckoutChargesOnlyTheStayBecauseTheHostPaysTheFee(t *testing.T) {
	store := &fakeStore{quote: pricedQuote()}
	intents := &fakeIntents{}
	result, err := newService(store, intents, fakeParser{}).StartCheckout(context.Background(), uuid.New(), store.quote.BookingID)
	if err != nil {
		t.Fatalf("StartCheckout() error = %v", err)
	}
	if result.AmountCents != 36000 {
		t.Fatalf("result = %+v, want 36000 with no guest fee", result)
	}
	if result.Currency != "usd" || result.ClientSecret == "" {
		t.Fatalf("result = %+v", result)
	}
	if len(intents.params) != 1 || intents.params[0].AmountCents != 36000 {
		t.Fatalf("intent params = %+v, want one intent for 36000", intents.params)
	}
	if len(store.created) != 1 || store.created[0].Status != StatusPending || store.created[0].GuestFeeCents != 0 || store.created[0].AmountCents != 36000 {
		t.Fatalf("created = %+v", store.created)
	}
}

func TestStartCheckoutRejectsUnpricedListing(t *testing.T) {
	quote := pricedQuote()
	quote.NightlyPriceCents = 0
	store := &fakeStore{quote: quote}
	_, err := newService(store, &fakeIntents{}, fakeParser{}).StartCheckout(context.Background(), uuid.New(), quote.BookingID)
	if !errors.Is(err, ErrListingUnpriced) {
		t.Fatalf("error = %v", err)
	}
}

func TestStartCheckoutRejectsBookingThatIsNotPending(t *testing.T) {
	for _, status := range []string{"confirmed", "cancelled", "completed"} {
		quote := pricedQuote()
		quote.BookingStatus = status
		store := &fakeStore{quote: quote}
		_, err := newService(store, &fakeIntents{}, fakeParser{}).StartCheckout(context.Background(), uuid.New(), quote.BookingID)
		if !errors.Is(err, ErrBookingNotPayable) {
			t.Fatalf("status %s: error = %v", status, err)
		}
	}
}

func TestStartCheckoutRejectsAlreadyPaidBooking(t *testing.T) {
	store := &fakeStore{quote: pricedQuote(), hasPayment: true, existing: Payment{Status: StatusSucceeded}}
	intents := &fakeIntents{}
	_, err := newService(store, intents, fakeParser{}).StartCheckout(context.Background(), uuid.New(), store.quote.BookingID)
	if !errors.Is(err, ErrAlreadyPaid) || len(intents.params) != 0 {
		t.Fatalf("error = %v, intents = %d", err, len(intents.params))
	}
}

func TestStartCheckoutPropagatesMissingBooking(t *testing.T) {
	store := &fakeStore{quoteErr: ErrBookingNotFound}
	_, err := newService(store, &fakeIntents{}, fakeParser{}).StartCheckout(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, ErrBookingNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestStartCheckoutDoesNotRecordPaymentWhenStripeFails(t *testing.T) {
	store := &fakeStore{quote: pricedQuote()}
	intents := &fakeIntents{err: errors.New("stripe down")}
	_, err := newService(store, intents, fakeParser{}).StartCheckout(context.Background(), uuid.New(), store.quote.BookingID)
	if err == nil || len(store.created) != 0 {
		t.Fatalf("error = %v, created = %d", err, len(store.created))
	}
}

func TestStartCheckoutUsesStableIdempotencyKey(t *testing.T) {
	store := &fakeStore{quote: pricedQuote()}
	intents := &fakeIntents{}
	service := newService(store, intents, fakeParser{})
	guestID := uuid.New()
	for range 2 {
		if _, err := service.StartCheckout(context.Background(), guestID, store.quote.BookingID); err != nil {
			t.Fatal(err)
		}
	}
	if intents.params[0].IdempotencyKey != intents.params[1].IdempotencyKey {
		t.Fatalf("keys differ: %q %q", intents.params[0].IdempotencyKey, intents.params[1].IdempotencyKey)
	}
}

func TestHandleWebhookMapsEventsToStatuses(t *testing.T) {
	cases := map[string]Status{
		EventSucceeded: StatusSucceeded,
		EventFailed:    StatusFailed,
		EventCanceled:  StatusCanceled,
	}
	for eventType, want := range cases {
		guestID := uuid.New()
		store := &fakeStore{}
		parser := fakeParser{event: Event{Type: eventType, IntentID: "pi_1", GuestID: guestID}}
		if err := newService(store, &fakeIntents{}, parser).HandleWebhook(context.Background(), "sig", []byte("{}")); err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
		if len(store.applied) != 1 || store.applied[0].status != want || store.applied[0].guestID != guestID {
			t.Fatalf("%s: applied = %+v", eventType, store.applied)
		}
	}
}

func TestHandleWebhookIgnoresUnrelatedEvents(t *testing.T) {
	store := &fakeStore{}
	parser := fakeParser{event: Event{Type: "charge.refunded"}}
	if err := newService(store, &fakeIntents{}, parser).HandleWebhook(context.Background(), "sig", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if len(store.applied) != 0 {
		t.Fatalf("applied = %+v", store.applied)
	}
}

func TestHandleWebhookIgnoresPaymentsThisAppDidNotCreate(t *testing.T) {
	store := &fakeStore{}
	parser := fakeParser{event: Event{Type: EventSucceeded, IntentID: "pi_1"}}
	err := newService(store, &fakeIntents{}, parser).HandleWebhook(context.Background(), "sig", []byte("{}"))
	if err != nil || len(store.applied) != 0 {
		t.Fatalf("error = %v, applied = %+v", err, store.applied)
	}
}

func TestHandleWebhookRejectsEventWithoutAnIntent(t *testing.T) {
	parser := fakeParser{event: Event{Type: EventSucceeded, GuestID: uuid.New()}}
	err := newService(&fakeStore{}, &fakeIntents{}, parser).HandleWebhook(context.Background(), "sig", []byte("{}"))
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("error = %v", err)
	}
}

func TestHandleWebhookPropagatesBadSignature(t *testing.T) {
	parser := fakeParser{err: ErrInvalidSignature}
	err := newService(&fakeStore{}, &fakeIntents{}, parser).HandleWebhook(context.Background(), "bad", []byte("{}"))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v", err)
	}
}

func TestHandleWebhookAcknowledgesUnmatchedPaymentsWithoutAnnouncing(t *testing.T) {
	for _, eventType := range []string{EventAuthorized, EventSucceeded, EventFailed, EventCanceled} {
		store := &fakeStore{applyErr: ErrPaymentNotFound}
		publisher := &publisherStub{}
		parser := fakeParser{event: Event{Type: eventType, IntentID: "pi_x", GuestID: uuid.New(), BookingID: uuid.New()}}

		err := newService(store, &fakeIntents{}, parser).WithPublisher(publisher).HandleWebhook(context.Background(), "sig", []byte("{}"))

		if err != nil || len(publisher.events) != 0 {
			t.Fatalf("%s: error = %v, events = %+v", eventType, err, publisher.events)
		}
	}
}

func TestHandleWebhookStillFailsWhenTheStoreFails(t *testing.T) {
	failure := errors.New("database down")
	store := &fakeStore{applyErr: failure}
	parser := fakeParser{event: Event{Type: EventSucceeded, IntentID: "pi_x", GuestID: uuid.New()}}
	err := newService(store, &fakeIntents{}, parser).HandleWebhook(context.Background(), "sig", []byte("{}"))
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigFromLookupRequiresBothSecrets(t *testing.T) {
	values := map[string]string{"STRIPE_SECRET_KEY": "sk_test_x"}
	if _, err := configFromLookup(func(key string) string { return values[key] }); !errors.Is(err, ErrMissingCredentials) {
		t.Fatalf("error = %v", err)
	}
	values["STRIPE_WEBHOOK_SECRET"] = "whsec_x"
	config, err := configFromLookup(func(key string) string { return values[key] })
	if err != nil || config.Currency != "usd" {
		t.Fatalf("config = %+v, err = %v", config, err)
	}
}
