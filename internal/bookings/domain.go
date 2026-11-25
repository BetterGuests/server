package bookings

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusRequested Status = "requested"
	StatusDeclined  Status = "declined"
	StatusPending   Status = "pending"
	StatusConfirmed Status = "confirmed"
	StatusCancelled Status = "cancelled"
	StatusCompleted Status = "completed"
)

type Role string

const (
	RoleGuest Role = "guest"
	RoleHost  Role = "host"
)

const (
	BookingEventKind  = "booking"
	paymentAuthorized = "authorized"
	paymentSucceeded  = "succeeded"
)

type Booking struct {
	ID             uuid.UUID  `json:"id"`
	ListingID      uuid.UUID  `json:"listing_id"`
	GuestID        uuid.UUID  `json:"guest_id"`
	Status         Status     `json:"status"`
	StartsOn       time.Time  `json:"starts_on"`
	EndsOn         time.Time  `json:"ends_on"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	VerificationID *uuid.UUID `json:"verification_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type Detail struct {
	Booking
	HostID            uuid.UUID `json:"host_id"`
	Role              Role      `json:"role"`
	GuestName         string    `json:"guest_name,omitempty"`
	GuestVerified     bool      `json:"guest_verified"`
	ListingTitle      string    `json:"listing_title"`
	ListingCity       string    `json:"listing_city,omitempty"`
	ListingRegion     string    `json:"listing_region,omitempty"`
	HostName          string    `json:"host_name,omitempty"`
	HostAvatarURL     string    `json:"host_avatar_url,omitempty"`
	Nights            int       `json:"nights"`
	NightlyPriceCents int       `json:"nightly_price_cents"`
	TotalCents        int       `json:"total_cents"`
	Currency          string    `json:"currency"`
	PaymentStatus     string    `json:"payment_status,omitempty"`
}

type BookingEvent struct {
	BookingID uuid.UUID `json:"booking_id"`
	Status    Status    `json:"status"`
}

type BookingStore interface {
	Create(ctx context.Context, actingUserID string, booking Booking) error
}

type DetailStore interface {
	Get(ctx context.Context, actorID, bookingID uuid.UUID) (Detail, error)
	ListForGuest(ctx context.Context, guestID uuid.UUID) ([]Detail, error)
	ListRequestsForHost(ctx context.Context, hostID uuid.UUID) ([]Detail, error)
	Decide(ctx context.Context, hostID, bookingID uuid.UUID, next Status, decidedAt time.Time) error
	CancelByGuest(ctx context.Context, guestID, bookingID uuid.UUID) error
}

type Publisher interface {
	Publish(userID uuid.UUID, kind string, payload any)
}

type PaymentHold interface {
	Capture(ctx context.Context, actorID, bookingID uuid.UUID) error
	Release(ctx context.Context, actorID, bookingID uuid.UUID) error
}
