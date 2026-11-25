package bookings

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type overlapHarness struct {
	service *BookingService
	store   *storeStub
	details *detailStub
	holds   *holdStub
	guestID uuid.UUID
	input   CreateBookingInput
}

func newOverlapHarness(others ...Detail) overlapHarness {
	store, details, holds := &storeStub{}, &detailStub{}, &holdStub{}
	guestID := uuid.New()
	starts := time.Now().UTC().AddDate(0, 0, 20)
	for index := range others {
		others[index].ID = uuid.New()
		others[index].GuestID = guestID
	}
	details.overlapping = others
	if len(others) > 0 {
		details.detail = others[0]
	}
	return overlapHarness{
		service: newTestService(store).WithDetails(details).WithPayments(holds),
		store:   store, details: details, holds: holds, guestID: guestID,
		input: CreateBookingInput{ListingID: uuid.New(), StartsOn: starts, EndsOn: starts.AddDate(0, 0, 3)},
	}
}

func trip(status Status, payment string) Detail {
	return Detail{Booking: Booking{Status: status}, PaymentStatus: payment}
}

func TestASecondTripOnTheSameDatesIsRefusedAndNamesTheFirst(t *testing.T) {
	for name, other := range map[string]Detail{
		"waiting for a host": trip(StatusRequested, paymentAuthorized),
		"approved":           trip(StatusPending, paymentAuthorized),
		"confirmed and paid": trip(StatusConfirmed, paymentSucceeded),
	} {
		t.Run(name, func(t *testing.T) {
			h := newOverlapHarness(other)
			_, err := h.service.CreateBooking(context.Background(), h.guestID, h.input)
			var overlap *TripOverlapError
			if !errors.As(err, &overlap) || !errors.Is(err, ErrTripOverlaps) || overlap.Trip.ID != h.details.overlapping[0].ID {
				t.Fatalf("error = %v", err)
			}
			if h.store.created.GuestID != uuid.Nil || h.details.cancelled {
				t.Fatal("nothing may be created or cancelled before the guest chooses")
			}
		})
	}
}

func TestAnAbandonedCheckoutIsCancelledAndDoesNotBlock(t *testing.T) {
	for name, payment := range map[string]string{"never reached payment": "", "card form left open": "pending", "card declined": "failed"} {
		t.Run(name, func(t *testing.T) {
			h := newOverlapHarness(trip(StatusRequested, payment))
			if _, err := h.service.CreateBooking(context.Background(), h.guestID, h.input); err != nil {
				t.Fatalf("error = %v", err)
			}
			if !h.details.cancelled || h.store.created.GuestID != h.guestID {
				t.Fatalf("cancelled = %v, created = %v", h.details.cancelled, h.store.created.GuestID)
			}
		})
	}
}

func TestARealTripBlocksBeforeAnyAbandonedCheckoutIsTouched(t *testing.T) {
	h := newOverlapHarness(trip(StatusRequested, ""), trip(StatusConfirmed, paymentSucceeded))
	_, err := h.service.CreateBooking(context.Background(), h.guestID, h.input)
	if !errors.Is(err, ErrTripOverlaps) || h.details.cancelled {
		t.Fatalf("error = %v, cancelled = %v", err, h.details.cancelled)
	}
}

func TestNothingIsCreatedWhenTheOldCardHoldCannotBeReleased(t *testing.T) {
	h := newOverlapHarness(trip(StatusRequested, "pending"))
	h.holds.releaseErr = errors.New("stripe is down")
	_, err := h.service.CreateBooking(context.Background(), h.guestID, h.input)
	if !errors.Is(err, ErrReleaseFailed) || h.store.created.GuestID != uuid.Nil {
		t.Fatalf("error = %v", err)
	}
}

func TestTripOverlapIsAConflictTheWebCanRecognise(t *testing.T) {
	handler := NewHTTPHandler(overlapCreator{})
	body := `{"listing_id":"` + uuid.NewString() + `","starts_on":"2027-01-01","ends_on":"2027-01-03"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/bookings", strings.NewReader(body))
	request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
	response := httptest.NewRecorder()
	handler.CreateBooking(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"trip_overlap"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

type overlapCreator struct{}

func (overlapCreator) CreateBooking(context.Context, uuid.UUID, CreateBookingInput) (Booking, error) {
	return Booking{}, &TripOverlapError{}
}
