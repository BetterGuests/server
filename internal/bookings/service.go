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
	ErrBookingNotFound      = errors.New("booking not found")
	ErrNotAwaitingApproval  = errors.New("booking is not awaiting approval")
	ErrNotCancellable       = errors.New("booking can no longer be cancelled")
	ErrPaymentNotAuthorized = errors.New("the guest has not put a card on hold for this booking")
	ErrCaptureFailed        = errors.New("the payment could not be completed")
)

type VerificationChecker interface {
	CurrentVerificationID(ctx context.Context, accountID uuid.UUID) (uuid.UUID, bool, error)
}

const maxStayNights = 90

type BookingService struct {
	store     BookingStore
	details   DetailStore
	verified  VerificationChecker
	publisher Publisher
	payments  PaymentHold
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

	if err := service.store.Create(ctx, guestID.String(), booking); err != nil {
		return Booking{}, err
	}
	return booking, nil
}

func (service *BookingService) Get(ctx context.Context, actorID, bookingID uuid.UUID) (Detail, error) {
	return service.details.Get(ctx, actorID, bookingID)
}

func (service *BookingService) ListForGuest(ctx context.Context, guestID uuid.UUID) ([]Detail, error) {
	return service.details.ListForGuest(ctx, guestID)
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
	if err := service.details.CancelByGuest(ctx, guestID, bookingID); err != nil {
		return Detail{}, err
	}
	service.release(ctx, guestID, bookingID)
	detail, err := service.details.Get(ctx, guestID, bookingID)
	if err != nil {
		return Detail{}, err
	}
	service.publish(detail.HostID, detail)
	return detail, nil
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
