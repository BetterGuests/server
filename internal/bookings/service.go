package bookings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrInvalidBooking is returned when a create-booking request fails
	// validation.
	ErrInvalidBooking = errors.New("invalid booking")

	// ErrBookingOverlaps is returned when the requested date range conflicts
	// with another pending or confirmed booking for the same listing. The
	// database's exclusion constraint is the actual enforcement point (see
	// db/migrations/00008_booking_overlap_prevention.sql); this is what that
	// conflict looks like by the time it reaches the service layer.
	ErrBookingOverlaps = errors.New("listing is already booked for part of that date range")
)

// maxStayNights caps how long a single booking can run, so a malformed or
// abusive date range can't hold a listing's calendar indefinitely.
const maxStayNights = 90

// BookingService owns the booking use cases. The clock and ID generator are
// injected so CreateBooking stays a deterministic, pure function of its
// inputs in tests.
type BookingService struct {
	store BookingStore
	now   func() time.Time
	newID func() uuid.UUID
}

// NewBookingService returns a booking service backed by store.
func NewBookingService(store BookingStore) *BookingService {
	return &BookingService{store: store, now: time.Now, newID: uuid.New}
}

// CreateBookingInput contains only values a guest may provide. GuestID comes
// from authentication; ID and status are assigned by BookingService.
type CreateBookingInput struct {
	ListingID uuid.UUID
	StartsOn  time.Time
	EndsOn    time.Time
}

// CreateBooking validates input and persists a new, pending booking owned by
// guestID. It does not check for an overlapping booking itself — that read
// would race with a concurrent request doing the same read — and instead
// relies on the database exclusion constraint to reject the insert
// atomically, translating that into ErrBookingOverlaps (see store.go).
func (service *BookingService) CreateBooking(
	ctx context.Context,
	guestID uuid.UUID,
	input CreateBookingInput,
) (Booking, error) {
	input, err := validateCreateInput(input, service.now())
	if err != nil {
		return Booking{}, err
	}

	booking := Booking{
		ID:        service.newID(),
		ListingID: input.ListingID,
		GuestID:   guestID,
		Status:    StatusPending,
		StartsOn:  input.StartsOn,
		EndsOn:    input.EndsOn,
		CreatedAt: service.now().UTC(),
	}

	if err := service.store.Create(ctx, guestID.String(), booking); err != nil {
		return Booking{}, err
	}
	return booking, nil
}

// validateCreateInput normalizes and checks input, returning a new value
// rather than mutating the caller's copy.
func validateCreateInput(input CreateBookingInput, now time.Time) (CreateBookingInput, error) {
	if input.ListingID == uuid.Nil {
		return input, fmt.Errorf("%w: listing_id is required", ErrInvalidBooking)
	}

	input.StartsOn = truncateToDate(input.StartsOn)
	input.EndsOn = truncateToDate(input.EndsOn)

	if !input.EndsOn.After(input.StartsOn) {
		return input, fmt.Errorf("%w: ends_on must be after starts_on", ErrInvalidBooking)
	}
	if input.StartsOn.Before(truncateToDate(now)) {
		return input, fmt.Errorf("%w: starts_on cannot be in the past", ErrInvalidBooking)
	}
	if input.EndsOn.Sub(input.StartsOn) > maxStayNights*24*time.Hour {
		return input, fmt.Errorf("%w: a booking cannot exceed %d nights", ErrInvalidBooking, maxStayNights)
	}
	return input, nil
}

// truncateToDate drops the time-of-day component, matching app.bookings'
// starts_on/ends_on "date" columns.
func truncateToDate(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}
