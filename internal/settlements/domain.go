package settlements

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusProcessing   Status = "processing"
	StatusAwaitingHost Status = "awaiting_host"
	StatusBlocked      Status = "blocked"
	StatusSettled      Status = "settled"
)

const MetadataBookingID = "settlement_booking_id"

const maxListedBlocked = 100

type Config struct {
	Enabled   bool
	StartsOn  time.Time
	Interval  time.Duration
	BatchSize int
	WebURL    string
}

type Settlement struct {
	ID             uuid.UUID
	BookingID      uuid.UUID
	GuestID        uuid.UUID
	HostID         uuid.UUID
	FeeCents       int
	CashbackCents  int
	RefundDueCents int
	PayoutCents    int
	RefundID       string
	TransferID     string
	Status         Status
	Reason         string
}

type Due struct {
	BookingID          uuid.UUID
	GuestID            uuid.UUID
	HostID             uuid.UUID
	PaymentIntentID    string
	AmountCents        int
	GuestFeeCents      int
	Currency           string
	AcceptedTaskKeys   []string
	ChorePlan          []byte
	EarlierRefundCents int
	AdjustmentCents    int
	HostAccountID      string
	Saved              *Settlement
}

type Charge struct {
	ID                    string
	CapturedCents         int
	RefundedCents         int
	Disputed              bool
	SettlementRefundID    string
	SettlementRefundCents int
	SettlementRefundDead  bool
}

type RefundRequest struct {
	BookingID       uuid.UUID
	PaymentIntentID string
	AmountCents     int
}

type TransferRequest struct {
	BookingID   uuid.UUID
	ChargeID    string
	Destination string
	AmountCents int
	Currency    string
}

type Report struct {
	Due     int
	Settled int
	Waiting int
	Blocked int
	Failed  int
}

type PayoutStatus struct {
	HasAccount    bool `json:"has_account"`
	Ready         bool `json:"ready"`
	FeePercent    int  `json:"fee_percent"`
	FeeFixedCents int  `json:"fee_fixed_cents"`
}

type BlockedSettlement struct {
	BookingID     uuid.UUID `json:"booking_id"`
	ListingTitle  string    `json:"listing_title"`
	HostName      string    `json:"host_name"`
	EndsOn        string    `json:"ends_on"`
	PayoutCents   int       `json:"payout_cents"`
	CashbackCents int       `json:"cashback_cents"`
	Currency      string    `json:"currency"`
	Reason        string    `json:"reason"`
	BlockedAt     time.Time `json:"blocked_at"`
	CanRetry      bool      `json:"can_retry"`
}

type PaymentDispute struct {
	ID              string `json:"id"`
	ListingTitle    string `json:"listing_title"`
	HostName        string `json:"host_name"`
	Status          string `json:"status"`
	Reason          string `json:"reason"`
	AmountCents     int    `json:"amount_cents"`
	Currency        string `json:"currency"`
	TransferID      string `json:"-"`
	PaidOutCents    int    `json:"paid_out_cents"`
	ReversibleCents int    `json:"reversible_cents"`
	ReversedCents   int    `json:"reversed_cents"`
	CanReverse      bool   `json:"can_reverse"`
}

type ReversalRequest struct {
	DisputeID   string
	TransferID  string
	AmountCents int
}

type Reverser interface {
	Reverse(ctx context.Context, request ReversalRequest) (string, error)
}

type ReviewStore interface {
	ListDisputes(ctx context.Context, adminID uuid.UUID, onlyDispute string, limit int) ([]PaymentDispute, error)
	RecordReversal(ctx context.Context, adminID uuid.UUID, disputeID, reversalID string, cents int) (bool, error)
	IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error)
	ListBlocked(ctx context.Context, adminID uuid.UUID, limit int) ([]BlockedSettlement, error)
	Retry(ctx context.Context, adminID, bookingID uuid.UUID) (bool, error)
}

type BlockedReviewer interface {
	Disputes(ctx context.Context, adminID uuid.UUID) ([]PaymentDispute, error)
	Reverse(ctx context.Context, adminID uuid.UUID, disputeID string) error
	Blocked(ctx context.Context, adminID uuid.UUID) ([]BlockedSettlement, error)
	Retry(ctx context.Context, adminID, bookingID uuid.UUID) error
}

type Store interface {
	ListDue(ctx context.Context, earliestEndsOn, latestEndsOn time.Time, batchSize int) ([]Due, error)
	Save(ctx context.Context, settlement Settlement) error
}

type Gateway interface {
	Charge(ctx context.Context, paymentIntentID string, bookingID uuid.UUID) (Charge, error)
	Refund(ctx context.Context, request RefundRequest) (string, error)
	FindTransfer(ctx context.Context, bookingID uuid.UUID) (string, bool, error)
	Transfer(ctx context.Context, request TransferRequest) (string, error)
	TransfersActive(ctx context.Context, accountID string) (bool, error)
}

type AccountStore interface {
	HostAccount(ctx context.Context, hostID uuid.UUID) (string, bool, error)
	SaveHostAccount(ctx context.Context, hostID uuid.UUID, accountID string) (string, error)
}

type Onboarder interface {
	CreateAccount(ctx context.Context, hostID uuid.UUID, contactEmail string) (string, error)
	OnboardingLink(ctx context.Context, accountID, returnURL, refreshURL string) (string, error)
	TransfersActive(ctx context.Context, accountID string) (bool, error)
}

type ContactEmails interface {
	VerifiedEmail(ctx context.Context, accountID uuid.UUID) (string, error)
}

type PayoutReader interface {
	Status(ctx context.Context, hostID uuid.UUID) (PayoutStatus, error)
}

type PayoutStarter interface {
	StartOnboarding(ctx context.Context, hostID uuid.UUID) (string, error)
}
