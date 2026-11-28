package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrBookingNotFound     = errors.New("booking not found")
	ErrBookingNotPayable   = errors.New("booking is not awaiting payment")
	ErrListingUnpriced     = errors.New("listing has no nightly price")
	ErrAlreadyPaid         = errors.New("booking is already paid")
	ErrInvalidSignature    = errors.New("invalid webhook signature")
	ErrInvalidPayload      = errors.New("invalid webhook payload")
	ErrPaymentNotFound     = errors.New("payment not found")
	ErrMissingCredentials  = errors.New("stripe credentials are required")
	ErrNoAuthorizedPayment = errors.New("no authorized payment for this booking")
	ErrCardNotFound        = errors.New("card not found")
)

const (
	defaultCurrency  = "usd"
	bookingRequested = "requested"
	bookingApproved  = "pending"
	bookingEventKind = "booking"
)

type bookingEvent struct {
	BookingID uuid.UUID `json:"booking_id"`
	Status    string    `json:"status"`
}

type PaymentService struct {
	store      Store
	intents    IntentCreator
	parser     EventParser
	controller IntentController
	publisher  Publisher
	wallet     Wallet
	customers  CustomerStore
	art        CardArtVault
	currency   string
	now        func() time.Time
	newID      func() uuid.UUID
}

const westmostUSOffset = 10 * time.Hour

func ConfigFromEnv() (Config, error) {
	return configFromLookup(os.Getenv)
}

func configFromLookup(lookup func(string) string) (Config, error) {
	config := Config{
		SecretKey:     strings.TrimSpace(lookup("STRIPE_SECRET_KEY")),
		WebhookSecret: strings.TrimSpace(lookup("STRIPE_WEBHOOK_SECRET")),
		Currency:      defaultCurrency,
	}
	if config.SecretKey == "" || config.WebhookSecret == "" {
		return Config{}, ErrMissingCredentials
	}
	return config, nil
}

func NewPaymentService(store Store, intents IntentCreator, parser EventParser, config Config) *PaymentService {
	currency := config.Currency
	if currency == "" {
		currency = defaultCurrency
	}
	return &PaymentService{
		store:    store,
		intents:  intents,
		parser:   parser,
		currency: currency,
		now:      time.Now,
		newID:    uuid.New,
	}
}

func (service *PaymentService) WithController(controller IntentController) *PaymentService {
	service.controller = controller
	return service
}

func (service *PaymentService) WithPublisher(publisher Publisher) *PaymentService {
	service.publisher = publisher
	return service
}

func (service *PaymentService) WithSavedCards(wallet Wallet, customers CustomerStore) *PaymentService {
	service.wallet = wallet
	service.customers = customers
	return service
}

func (service *PaymentService) SavedCards(ctx context.Context, accountID uuid.UUID) ([]SavedCard, error) {
	cards, err := service.listCards(ctx, accountID)
	if err != nil {
		return nil, err
	}
	service.addArt(ctx, cards)
	return cards, nil
}

func (service *PaymentService) listCards(ctx context.Context, accountID uuid.UUID) ([]SavedCard, error) {
	if service.wallet == nil || service.customers == nil {
		return []SavedCard{}, nil
	}
	customerID, found, err := service.customers.CustomerID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !found {
		return []SavedCard{}, nil
	}
	return service.wallet.ListCards(ctx, customerID)
}

func (service *PaymentService) RemoveCard(ctx context.Context, accountID uuid.UUID, cardID string) error {
	card, err := service.ownedCard(ctx, accountID, cardID)
	if err != nil {
		return err
	}
	if err := service.wallet.DetachCard(ctx, cardID); err != nil {
		return err
	}
	service.forgetArt(ctx, card.ArtCardID)
	return nil
}

func (service *PaymentService) savedCards(ctx context.Context, accountID uuid.UUID) (customerID, session string) {
	if service.wallet == nil || service.customers == nil {
		return "", ""
	}
	customerID, err := service.customerFor(ctx, accountID)
	if err != nil {
		slog.Warn("Saved cards are unavailable", "error", err)
		return "", ""
	}
	session, err = service.wallet.CreateCustomerSession(ctx, customerID)
	if err != nil {
		slog.Warn("Saved cards are unavailable", "error", err)
		return customerID, ""
	}
	return customerID, session
}

func (service *PaymentService) customerFor(ctx context.Context, accountID uuid.UUID) (string, error) {
	existing, found, err := service.customers.CustomerID(ctx, accountID)
	if err != nil || found {
		return existing, err
	}
	created, err := service.wallet.CreateCustomer(ctx, accountID)
	if err != nil {
		return "", err
	}
	return service.customers.SaveCustomerID(ctx, accountID, created)
}

func (service *PaymentService) Capture(ctx context.Context, actorID, bookingID uuid.UUID) error {
	hold, found, err := service.store.HoldForBooking(ctx, actorID, bookingID)
	if err != nil {
		return err
	}
	if !found || hold.Status != StatusAuthorized {
		return ErrNoAuthorizedPayment
	}
	return service.controller.Capture(ctx, hold.IntentID)
}

func (service *PaymentService) Release(ctx context.Context, actorID, bookingID uuid.UUID) error {
	hold, found, err := service.store.HoldForBooking(ctx, actorID, bookingID)
	if err != nil || !found {
		return err
	}
	if hold.Status != StatusAuthorized && hold.Status != StatusPending {
		return nil
	}
	return service.controller.Cancel(ctx, hold.IntentID)
}

func (service *PaymentService) StartCheckout(ctx context.Context, guestID, bookingID uuid.UUID) (CheckoutResult, error) {
	quote, err := service.store.QuoteBooking(ctx, guestID, bookingID)
	if err != nil {
		return CheckoutResult{}, err
	}
	holdOnly := quote.BookingStatus == bookingRequested
	if !holdOnly && quote.BookingStatus != bookingApproved {
		return CheckoutResult{}, ErrBookingNotPayable
	}
	if tooLateToPay(quote, holdOnly, service.now()) {
		return CheckoutResult{}, ErrBookingNotPayable
	}
	if quote.NightlyPriceCents <= 0 {
		return CheckoutResult{}, ErrListingUnpriced
	}

	existing, found, err := service.store.ExistingPayment(ctx, guestID, bookingID)
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("look up payment: %w", err)
	}
	if found && (existing.Status == StatusSucceeded || existing.Status == StatusAuthorized) {
		return CheckoutResult{}, ErrAlreadyPaid
	}

	amount := quote.Nights * quote.NightlyPriceCents
	customerID, customerSession := service.savedCards(ctx, guestID)
	intent, err := service.intents.CreateIntent(ctx, IntentParams{
		AmountCents:    amount,
		Currency:       service.currency,
		BookingID:      bookingID,
		GuestID:        guestID,
		IdempotencyKey: "booking-" + bookingID.String() + "-" + fmt.Sprint(amount) + "-" + fmt.Sprint(holdOnly) + customerID,
		ManualCapture:  holdOnly,
		CustomerID:     customerID,
	})
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("create payment intent: %w", err)
	}

	if !found || existing.StripePaymentIntentID != intent.ID {
		now := service.now().UTC()
		err = service.store.CreatePayment(ctx, Payment{
			ID:                    service.newID(),
			BookingID:             bookingID,
			GuestID:               guestID,
			HostID:                quote.HostID,
			StripePaymentIntentID: intent.ID,
			AmountCents:           amount,
			Currency:              service.currency,
			Status:                StatusPending,
			CreatedAt:             now,
			UpdatedAt:             now,
		})
		if err != nil {
			return CheckoutResult{}, fmt.Errorf("record payment: %w", err)
		}
	}

	return CheckoutResult{
		PaymentIntentID: intent.ID,
		ClientSecret:    intent.ClientSecret,
		AmountCents:     amount,
		Currency:        service.currency,
		CustomerSession: customerSession,
	}, nil
}

func (service *PaymentService) HandleWebhook(ctx context.Context, signature string, body []byte) error {
	event, err := service.parser.ParseEvent(body, signature)
	if err != nil {
		return err
	}
	if isDisputeEvent(event.Type) {
		return service.recordDispute(ctx, event)
	}
	status, relevant := statusForEvent(event.Type)
	if !relevant {
		return nil
	}
	if event.IntentID == "" {
		return ErrInvalidPayload
	}
	if event.GuestID == uuid.Nil {
		slog.Info("Ignoring a Stripe event for a payment without booking metadata", "event_id", event.ID, "type", event.Type)
		return nil
	}
	err = service.store.ApplyOutcome(ctx, event.GuestID, event.IntentID, status, event.OccurredAt)
	if errors.Is(err, ErrPaymentNotFound) {
		reportUnmatched(ctx, event, status)
		return nil
	}
	if err != nil {
		return err
	}
	service.announce(ctx, event, status)
	return nil
}

func (service *PaymentService) recordDispute(ctx context.Context, event Event) error {
	if event.Dispute == nil || event.Dispute.ID == "" {
		return ErrInvalidPayload
	}
	if event.Dispute.IntentID == "" {
		slog.Warn("Ignoring a Stripe dispute without a payment intent", "event_id", event.ID, "dispute_id", event.Dispute.ID)
		return nil
	}
	matched, err := service.store.RecordDispute(ctx, *event.Dispute, event.OccurredAt)
	if err != nil {
		return err
	}
	if !matched {
		slog.Error("Stripe dispute matches no recorded payment", "event_id", event.ID, "dispute_id", event.Dispute.ID, "intent_id", event.Dispute.IntentID)
	}
	return nil
}

func isDisputeEvent(eventType string) bool {
	return eventType == EventDisputeCreated || eventType == EventDisputeUpdated || eventType == EventDisputeClosed
}

func reportUnmatched(ctx context.Context, event Event, status Status) {
	level := slog.LevelWarn
	if status == StatusAuthorized || status == StatusSucceeded {
		level = slog.LevelError
	}
	slog.Log(ctx, level, "Stripe event matches no recorded payment",
		"event_id", event.ID, "type", event.Type, "intent_id", event.IntentID, "booking_id", event.BookingID)
}

func (service *PaymentService) announce(ctx context.Context, event Event, status Status) {
	if status != StatusAuthorized && status != StatusSucceeded {
		return
	}
	if service.publisher == nil || event.BookingID == uuid.Nil {
		return
	}
	quote, err := service.store.QuoteBooking(ctx, event.GuestID, event.BookingID)
	if err != nil {
		slog.Warn("Announce payment change", "error", err, "booking_id", event.BookingID)
		return
	}
	if status == StatusAuthorized {
		service.publisher.Publish(quote.HostID, bookingEventKind, bookingEvent{BookingID: event.BookingID, Status: bookingRequested})
		return
	}
	changed := bookingEvent{BookingID: event.BookingID, Status: quote.BookingStatus}
	service.publisher.Publish(event.GuestID, bookingEventKind, changed)
	service.publisher.Publish(quote.HostID, bookingEventKind, changed)
}

func tooLateToPay(quote Quote, holdOnly bool, now time.Time) bool {
	year, month, day := now.UTC().Add(-westmostUSOffset).Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if holdOnly {
		return quote.StartsOn.Before(today)
	}
	return quote.StartsOn.AddDate(0, 0, quote.Nights).Before(today)
}

func statusForEvent(eventType string) (Status, bool) {
	switch eventType {
	case EventAuthorized:
		return StatusAuthorized, true
	case EventSucceeded:
		return StatusSucceeded, true
	case EventFailed:
		return StatusFailed, true
	case EventCanceled:
		return StatusCanceled, true
	}
	return "", false
}
