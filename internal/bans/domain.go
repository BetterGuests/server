package bans

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	MaxReasonRunes      = 500
	listLimit           = 100
	maxRequestBodyBytes = 8 << 10
	pauseReason         = "Account suspended"
)

type Ban struct {
	ID        uuid.UUID  `json:"id"`
	AccountID uuid.UUID  `json:"account_id"`
	Name      string     `json:"name"`
	Reason    string     `json:"reason"`
	BannedAt  time.Time  `json:"banned_at"`
	LiftedAt  *time.Time `json:"lifted_at,omitempty"`
	Bookings  []Booking  `json:"bookings"`
}

type Booking struct {
	ID           uuid.UUID `json:"id"`
	ListingTitle string    `json:"listing_title"`
	BannedIsHost bool      `json:"banned_is_host"`
	StartsOn     string    `json:"starts_on"`
	EndsOn       string    `json:"ends_on"`
	Status       string    `json:"status"`
	PaidCents    int       `json:"paid_cents"`
}

type Request struct {
	AccountID uuid.UUID `json:"account_id"`
	Reason    string    `json:"reason"`
}

type Store interface {
	Ban(ctx context.Context, adminID, banID uuid.UUID, request Request, at time.Time) error
	Lift(ctx context.Context, adminID, banID uuid.UUID, at time.Time) error
	List(ctx context.Context, adminID uuid.UUID, limit int) ([]Ban, error)
}

type AdminChecker interface {
	IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error)
}

type Disconnector interface {
	Disconnect(accountID uuid.UUID)
}

type Desk interface {
	Ban(ctx context.Context, adminID uuid.UUID, request Request) (uuid.UUID, error)
	Lift(ctx context.Context, adminID, banID uuid.UUID) error
	List(ctx context.Context, adminID uuid.UUID) ([]Ban, error)
}
