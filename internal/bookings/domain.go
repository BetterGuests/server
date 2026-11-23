package bookings

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// T Y P E S --------------------------------------- //

// Status models a booking's lifecycle as a type instead of a bare string, so
// an invalid value can never reach the database. Values match the
// app.bookings status CHECK constraint exactly.
type Status string

const (
	StatusPending   Status = "pending"
	StatusConfirmed Status = "confirmed"
	StatusCancelled Status = "cancelled"
	StatusCompleted Status = "completed"
)

// Booking is a guest's reservation of a listing for a date range. Overlap
// with another active (pending/confirmed) booking for the same listing is
// prevented by a database exclusion constraint — see
// db/migrations/00008_booking_overlap_prevention.sql — not by application
// code, since a check-then-insert in Go would race under concurrent
// requests.
type Booking struct {
	ID             uuid.UUID  `json:"id"`
	ListingID      uuid.UUID  `json:"listing_id"`
	GuestID        uuid.UUID  `json:"guest_id"`
	Status         Status     `json:"status"`
	StartsOn       time.Time  `json:"starts_on"`
	EndsOn         time.Time  `json:"ends_on"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	VerificationID *uuid.UUID `json:"verification_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// BookingStore persists bookings.
type BookingStore interface {
	Create(ctx context.Context, actingUserID string, booking Booking) error
}
