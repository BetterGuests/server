package payments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrBookingNotFound    = errors.New("booking not found")
	ErrBookingNotPayable  = errors.New("booking is not awaiting payment")
	ErrListingUnpriced    = errors.New("listing has no nightly price")
	ErrAlreadyPaid        = errors.New("booking is already paid")
	ErrInvalidSignature   = errors.New("invalid webhook signature")
	ErrInvalidPayload     = errors.New("invalid webhook payload")
	ErrPaymentNotFound    = errors.New("payment not found")
	ErrMissingCredentials = errors.New("stripe credentials are required")
)

const defaultCurrency = "usd"

type PaymentService struct {
	store    Store
	intents  IntentCreator
	parser   EventParser
	currency string
	now      func() time.Time
	newID    func() uuid.UUID
}

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

func (service *PaymentService) StartCheckout(ctx context.Context, guestID, bookingID uuid.UUID) (CheckoutResult, error) {
	quote, err := service.store.QuoteBooking(ctx, guestID, bookingID)
	if err != nil {
		return CheckoutResult{}, err
	}
	if quote.BookingStatus != "pending" {
		return CheckoutResult{}, ErrBookingNotPayable
	}
	if quote.NightlyPriceCents <= 0 {
		return CheckoutResult{}, ErrListingUnpriced
	}

	existing, found, err := service.store.ExistingPayment(ctx, guestID, bookingID)
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("look up payment: %w", err)
	}
	if found && existing.Status == StatusSucceeded {
		return CheckoutResult{}, ErrAlreadyPaid
	}

	amount := quote.Nights * quote.NightlyPriceCents
	intent, err := service.intents.CreateIntent(ctx, IntentParams{
		AmountCents:    amount,
		Currency:       service.currency,
		BookingID:      bookingID,
		GuestID:        guestID,
		IdempotencyKey: "booking-" + bookingID.String() + "-" + fmt.Sprint(amount),
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
	}, nil
}

func (service *PaymentService) HandleWebhook(ctx context.Context, signature string, body []byte) error {
	event, err := service.parser.ParseEvent(body, signature)
	if err != nil {
		return err
	}
	status, relevant := statusForEvent(event.Type)
	if !relevant {
		return nil
	}
	if event.IntentID == "" || event.GuestID == uuid.Nil {
		return ErrInvalidPayload
	}
	return service.store.ApplyOutcome(ctx, event.GuestID, event.IntentID, status, event.OccurredAt)
}

func statusForEvent(eventType string) (Status, bool) {
	switch eventType {
	case EventSucceeded:
		return StatusSucceeded, true
	case EventFailed:
		return StatusFailed, true
	case EventCanceled:
		return StatusCanceled, true
	}
	return "", false
}
