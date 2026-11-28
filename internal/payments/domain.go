package payments

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusAuthorized Status = "authorized"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCanceled   Status = "canceled"
)

const (
	EventAuthorized = "payment_intent.amount_capturable_updated"
	EventSucceeded  = "payment_intent.succeeded"
	EventFailed     = "payment_intent.payment_failed"
	EventCanceled   = "payment_intent.canceled"

	EventDisputeCreated = "charge.dispute.created"
	EventDisputeUpdated = "charge.dispute.updated"
	EventDisputeClosed  = "charge.dispute.closed"
)

const (
	MetadataBookingID = "booking_id"
	MetadataGuestID   = "guest_id"
)

type Config struct {
	SecretKey     string
	WebhookSecret string
	Currency      string
}

type Quote struct {
	BookingID         uuid.UUID
	ListingID         uuid.UUID
	HostID            uuid.UUID
	BookingStatus     string
	Nights            int
	NightlyPriceCents int
	StartsOn          time.Time
}

type Payment struct {
	ID                    uuid.UUID `json:"id"`
	BookingID             uuid.UUID `json:"booking_id"`
	GuestID               uuid.UUID `json:"guest_id"`
	HostID                uuid.UUID `json:"-"`
	StripePaymentIntentID string    `json:"stripe_payment_intent_id"`
	AmountCents           int       `json:"amount_cents"`
	GuestFeeCents         int       `json:"guest_fee_cents"`
	Currency              string    `json:"currency"`
	Status                Status    `json:"status"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type IntentParams struct {
	AmountCents    int
	Currency       string
	BookingID      uuid.UUID
	GuestID        uuid.UUID
	IdempotencyKey string
	ManualCapture  bool
	CustomerID     string
}

type Intent struct {
	ID           string
	ClientSecret string
}

type CheckoutResult struct {
	PaymentIntentID string `json:"payment_intent_id"`
	ClientSecret    string `json:"client_secret"`
	AmountCents     int    `json:"amount_cents"`
	Currency        string `json:"currency"`
	CustomerSession string `json:"customer_session_client_secret,omitempty"`
}

type Event struct {
	ID         string
	Type       string
	IntentID   string
	BookingID  uuid.UUID
	GuestID    uuid.UUID
	OccurredAt time.Time
	Dispute    *Dispute
}

type Dispute struct {
	ID          string
	IntentID    string
	Status      string
	Reason      string
	AmountCents int
}

type IntentCreator interface {
	CreateIntent(ctx context.Context, params IntentParams) (Intent, error)
}

type EventParser interface {
	ParseEvent(body []byte, signature string) (Event, error)
}

type Store interface {
	QuoteBooking(ctx context.Context, guestID, bookingID uuid.UUID) (Quote, error)
	ExistingPayment(ctx context.Context, guestID, bookingID uuid.UUID) (Payment, bool, error)
	CreatePayment(ctx context.Context, payment Payment) error
	ApplyOutcome(ctx context.Context, guestID uuid.UUID, intentID string, status Status, occurredAt time.Time) error
	HoldForBooking(ctx context.Context, actorID, bookingID uuid.UUID) (PaymentHold, bool, error)
	RecordDispute(ctx context.Context, dispute Dispute, occurredAt time.Time) (bool, error)
}

type PaymentHold struct {
	IntentID string
	Status   Status
}

type IntentController interface {
	Capture(ctx context.Context, intentID string) error
	Cancel(ctx context.Context, intentID string) error
}

type BillingDetails struct {
	Name       string `json:"name"`
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

type SavedCard struct {
	ID       string         `json:"id"`
	Brand    string         `json:"brand"`
	Last4    string         `json:"last4"`
	ExpMonth int            `json:"exp_month"`
	ExpYear  int            `json:"exp_year"`
	Funding  string         `json:"funding"`
	Wallet   string         `json:"wallet"`
	Billing  BillingDetails `json:"billing"`

	CardStyle

	ArtURL    string `json:"art_url"`
	ArtCardID string `json:"-"`
}

type CardManager interface {
	SavedCards(ctx context.Context, accountID uuid.UUID) ([]SavedCard, error)
	RemoveCard(ctx context.Context, accountID uuid.UUID, cardID string) error
	StyleCard(ctx context.Context, accountID uuid.UUID, cardID string, style CardStyle) error
	CardArtEnabled() bool
	StartCardArt(ctx context.Context, accountID uuid.UUID, cardID string) (CollectSession, error)
	LinkCardArt(ctx context.Context, accountID uuid.UUID, cardID, artCardID string) error
}

type Wallet interface {
	ListCards(ctx context.Context, customerID string) ([]SavedCard, error)
	DetachCard(ctx context.Context, cardID string) error
	StyleCard(ctx context.Context, cardID string, style CardStyle) error
	LinkArtCard(ctx context.Context, cardID, artCardID string) error
	CreateCustomer(ctx context.Context, accountID uuid.UUID) (string, error)
	CreateCustomerSession(ctx context.Context, customerID string) (string, error)
}

type CustomerStore interface {
	CustomerID(ctx context.Context, accountID uuid.UUID) (string, bool, error)
	SaveCustomerID(ctx context.Context, accountID uuid.UUID, customerID string) (string, error)
}

type Publisher interface {
	Publish(userID uuid.UUID, kind string, payload any)
}

type CheckoutStarter interface {
	StartCheckout(ctx context.Context, guestID, bookingID uuid.UUID) (CheckoutResult, error)
}

type WebhookProcessor interface {
	HandleWebhook(ctx context.Context, signature string, body []byte) error
}
