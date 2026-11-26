package teamevents

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Kind string

const (
	BookingRequested Kind = "booking_requested"
	PaymentHeld      Kind = "payment_held"
	PaymentSucceeded Kind = "payment_succeeded"
	PaymentFailed    Kind = "payment_failed"
	BookingCancelled Kind = "booking_cancelled"
	StayReported     Kind = "stay_reported"
	ReviewPosted     Kind = "review_posted"
	ListingPublished Kind = "listing_published"
	ListingPaused    Kind = "listing_paused"
	DocumentSent     Kind = "document_sent"
)

type Event struct {
	Kind           Kind       `json:"kind"`
	At             time.Time  `json:"at"`
	RefID          uuid.UUID  `json:"ref_id"`
	ListingID      *uuid.UUID `json:"listing_id"`
	ListingTitle   string     `json:"listing_title"`
	City           string     `json:"city"`
	Latitude       *float64   `json:"latitude"`
	Longitude      *float64   `json:"longitude"`
	AmountCents    int        `json:"amount_cents"`
	Currency       string     `json:"currency"`
	GuestAccountID *uuid.UUID `json:"guest_account_id"`
	HostAccountID  *uuid.UUID `json:"host_account_id"`
	Detail         string     `json:"detail"`
}

type Query struct {
	Since time.Time
	Kinds []Kind
	Limit int
}

type Store interface {
	Events(ctx context.Context, adminID uuid.UUID, query Query) ([]Event, error)
}

type AdminChecker interface {
	IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error)
}

type Reader interface {
	Feed(ctx context.Context, adminID uuid.UUID, since time.Time, kinds []Kind) ([]Event, error)
}
