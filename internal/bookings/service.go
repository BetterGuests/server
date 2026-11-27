package bookings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidBooking       = errors.New("invalid booking")
	ErrBookingOverlaps      = errors.New("listing is already booked for part of that date range")
	ErrVerificationRequired = errors.New("a current ID verification is required to book")
	ErrHostIDRequired       = errors.New("verify your ID again to approve bookings")
	ErrBookingNotFound      = errors.New("booking not found")
	ErrNotAwaitingApproval  = errors.New("booking is not awaiting approval")
	ErrNotCancellable       = errors.New("booking can no longer be cancelled")
	ErrPaymentNotAuthorized = errors.New("the guest has not put a card on hold for this booking")
	ErrCaptureFailed        = errors.New("the payment could not be completed")
	ErrStayAlreadyStarted   = errors.New("the check-in date has passed, so this booking can no longer be approved")
	ErrGuestBanned          = errors.New("this guest is suspended, so the booking cannot be approved")
	ErrReleaseFailed        = errors.New("we could not release the card, so the booking was not cancelled")
	ErrListingPaused        = errors.New("this listing is not taking bookings right now")
	ErrTripOverlaps         = errors.New("you already have a trip on these dates")
)

type VerificationChecker interface {
	CurrentVerificationID(ctx context.Context, accountID uuid.UUID) (uuid.UUID, bool, error)
}

const (
	maxStayNights    = 90
	westmostUSOffset = 10 * time.Hour
)

type BookingService struct {
	store     BookingStore
	details   DetailStore
	verified  VerificationChecker
	publisher Publisher
	payments  PaymentHold
	bans      BanChecker
	now       func() time.Time
	newID     func() uuid.UUID
}

func NewBookingService(store BookingStore) *BookingService {
	return &BookingService{store: store, now: time.Now, newID: uuid.New}
}

func (service *BookingService) WithVerification(checker VerificationChecker) *BookingService {
	service.verified = checker
	return service
}

func (service *BookingService) WithBans(bans BanChecker) *BookingService {
	service.bans = bans
	return service
}

func (service *BookingService) WithDetails(details DetailStore) *BookingService {
	service.details = details
	return service
}

func (service *BookingService) WithPayments(payments PaymentHold) *BookingService {
	service.payments = payments
	return service
}

func (service *BookingService) WithPublisher(publisher Publisher) *BookingService {
	service.publisher = publisher
	return service
}

type CreateBookingInput struct {
	ListingID uuid.UUID
	StartsOn  time.Time
	EndsOn    time.Time
}

func (service *BookingService) CreateBooking(
	ctx context.Context,
	guestID uuid.UUID,
	input CreateBookingInput,
) (Booking, error) {
	input, err := validateCreateInput(input, service.now())
	if err != nil {
		return Booking{}, err
	}

	var verificationID *uuid.UUID
	if service.verified != nil {
		id, current, err := service.verified.CurrentVerificationID(ctx, guestID)
		if err != nil {
			return Booking{}, fmt.Errorf("check verification: %w", err)
		}
		if !current {
			return Booking{}, ErrVerificationRequired
		}
		verificationID = &id
	}

	booking := Booking{
		VerificationID: verificationID,
		ID:             service.newID(),
		ListingID:      input.ListingID,
		GuestID:        guestID,
		Status:         StatusRequested,
		StartsOn:       input.StartsOn,
		EndsOn:         input.EndsOn,
		CreatedAt:      service.now().UTC(),
	}

	if err := service.clearAbandonedCheckouts(ctx, guestID, input); err != nil {
		return Booking{}, err
	}

	if err := service.store.Create(ctx, guestID.String(), booking); err != nil {
		return Booking{}, err
	}
	return booking, nil
}

func (overlap *TripOverlapError) Error() string {
	return ErrTripOverlaps.Error()
}

func (overlap *TripOverlapError) Unwrap() error {
	return ErrTripOverlaps
}

func (service *BookingService) requireHostID(ctx context.Context, hostID uuid.UUID) error {
	if service.verified == nil {
		return nil
	}
	_, current, err := service.verified.CurrentVerificationID(ctx, hostID)
	if err != nil {
		return fmt.Errorf("check host verification: %w", err)
	}
	if !current {
		return ErrHostIDRequired
	}
	return nil
}

func (service *BookingService) clearAbandonedCheckouts(ctx context.Context, guestID uuid.UUID, input CreateBookingInput) error {
	if service.details == nil {
		return nil
	}
	others, err := service.details.ListOverlapping(ctx, guestID, input.StartsOn, input.EndsOn)
	if err != nil {
		return fmt.Errorf("find overlapping trips: %w", err)
	}
	for _, other := range others {
		if !abandonedCheckout(other) {
			return &TripOverlapError{Trip: other}
		}
	}
	for _, other := range others {
		if _, err := service.Cancel(ctx, guestID, other.ID); err != nil {
			return fmt.Errorf("cancel abandoned checkout: %w", err)
		}
	}
	return nil
}

func abandonedCheckout(detail Detail) bool {
	return detail.Status == StatusRequested &&
		detail.PaymentStatus != paymentAuthorized &&
		detail.PaymentStatus != paymentSucceeded
}

func (service *BookingService) Get(ctx context.Context, actorID, bookingID uuid.UUID) (Detail, error) {
	return service.details.Get(ctx, actorID, bookingID)
}

func (service *BookingService) ListForGuest(ctx context.Context, guestID uuid.UUID) ([]Detail, error) {
	return service.details.ListForGuest(ctx, guestID)
}

func (service *BookingService) ListForHost(ctx context.Context, hostID uuid.UUID) ([]Detail, error) {
	return service.details.ListForHost(ctx, hostID)
}

func (service *BookingService) ListRequestsForHost(ctx context.Context, hostID uuid.UUID) ([]Detail, error) {
	return service.details.ListRequestsForHost(ctx, hostID)
}

func (service *BookingService) Approve(ctx context.Context, hostID, bookingID uuid.UUID) (Detail, error) {
	detail, err := service.details.Get(ctx, hostID, bookingID)
	if err != nil {
		return Detail{}, err
	}
	if detail.Role != RoleHost {
		return Detail{}, ErrBookingNotFound
	}
	if service.payments != nil && detail.PaymentStatus != paymentAuthorized {
		if detail.Status == StatusPending && detail.PaymentStatus == paymentSucceeded {
			return detail, nil
		}
		return Detail{}, ErrPaymentNotAuthorized
	}
	if detail.StartsOn.Before(truncateToDate(service.now().UTC().Add(-westmostUSOffset))) {
		return Detail{}, ErrStayAlreadyStarted
	}
	if err := service.requireHostID(ctx, hostID); err != nil {
		return Detail{}, err
	}
	if err := service.refuseBannedGuest(ctx, detail.GuestID); err != nil {
		return Detail{}, err
	}
	switch detail.Status {
	case StatusRequested:
		if err := service.details.Decide(ctx, hostID, bookingID, StatusPending, service.now().UTC()); err != nil {
			return Detail{}, err
		}
	case StatusPending:
	default:
		return Detail{}, ErrNotAwaitingApproval
	}
	if service.payments != nil {
		if err := service.payments.Capture(ctx, hostID, bookingID); err != nil {
			slog.Error("Capture held payment", "error", err)
			return Detail{}, ErrCaptureFailed
		}
	}
	return service.finish(ctx, hostID, bookingID, detail.GuestID)
}

func (service *BookingService) refuseBannedGuest(ctx context.Context, guestID uuid.UUID) error {
	if service.bans == nil {
		return nil
	}
	banned, err := service.bans.IsBanned(ctx, guestID)
	if err != nil {
		return fmt.Errorf("check guest ban: %w", err)
	}
	if banned {
		return ErrGuestBanned
	}
	return nil
}

func (service *BookingService) Decline(ctx context.Context, hostID, bookingID uuid.UUID) (Detail, error) {
	if err := service.details.Decide(ctx, hostID, bookingID, StatusDeclined, service.now().UTC()); err != nil {
		return Detail{}, err
	}
	service.release(ctx, hostID, bookingID)
	detail, err := service.details.Get(ctx, hostID, bookingID)
	if err != nil {
		return Detail{}, err
	}
	service.publish(detail.GuestID, detail)
	return detail, nil
}

func (service *BookingService) Cancel(ctx context.Context, guestID, bookingID uuid.UUID) (Detail, error) {
	current, err := service.details.Get(ctx, guestID, bookingID)
	if err != nil {
		return Detail{}, err
	}
	if current.GuestID != guestID {
		return Detail{}, ErrBookingNotFound
	}
	if !cancellable(current) {
		return Detail{}, ErrNotCancellable
	}
	if service.payments != nil {
		if err := service.payments.Release(ctx, guestID, bookingID); err != nil {
			slog.Error("Release held payment", "error", err, "booking_id", bookingID)
			return Detail{}, ErrReleaseFailed
		}
	}
	if err := service.details.CancelByGuest(ctx, guestID, bookingID); err != nil {
		return Detail{}, err
	}
	detail, err := service.details.Get(ctx, guestID, bookingID)
	if err != nil {
		return Detail{}, err
	}
	service.publish(detail.HostID, detail)
	return detail, nil
}

func cancellable(detail Detail) bool {
	open := detail.Status == StatusRequested || detail.Status == StatusPending
	return open && detail.PaymentStatus != paymentSucceeded
}

func (service *BookingService) finish(ctx context.Context, actorID, bookingID, notify uuid.UUID) (Detail, error) {
	detail, err := service.details.Get(ctx, actorID, bookingID)
	if err != nil {
		return Detail{}, err
	}
	service.publish(notify, detail)
	return detail, nil
}

func (service *BookingService) release(ctx context.Context, actorID, bookingID uuid.UUID) {
	if service.payments == nil {
		return
	}
	if err := service.payments.Release(ctx, actorID, bookingID); err != nil {
		slog.Error("Release held payment", "error", err, "booking_id", bookingID)
	}
}

func (service *BookingService) publish(userID uuid.UUID, detail Detail) {
	if service.publisher == nil {
		return
	}
	service.publisher.Publish(userID, BookingEventKind, BookingEvent{BookingID: detail.ID, Status: detail.Status})
}

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

func truncateToDate(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}
