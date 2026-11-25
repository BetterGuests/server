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

type Config struct {
	Enabled    bool
	FeePercent int
	StartsOn   time.Time
	Interval   time.Duration
	BatchSize  int
	WebURL     string
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
	Currency           string
	AcceptedTaskKeys   []string
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
	HasAccount bool `json:"has_account"`
	Ready      bool `json:"ready"`
	FeePercent int  `json:"fee_percent"`
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
