package bookings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type storeStub struct {
	created Booking
	err     error
}

func (stub *storeStub) Create(_ context.Context, _ string, booking Booking) error {
	stub.created = booking
	return stub.err
}

func newTestService(store BookingStore) *BookingService {
	service := NewBookingService(store)
	service.now = func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }
	service.newID = func() uuid.UUID { return uuid.Nil }
	return service
}

func TestCreateBookingValidation(t *testing.T) {
	tests := []struct {
		name      string
		listingID uuid.UUID
		startsOn  time.Time
		endsOn    time.Time
		wantErr   bool
	}{
		{
			name: "valid range", listingID: uuid.New(),
			startsOn: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			endsOn:   time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "missing listing", listingID: uuid.Nil,
			startsOn: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			endsOn:   time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
			wantErr:  true,
		},
		{
			name: "ends before starts", listingID: uuid.New(),
			startsOn: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
			endsOn:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			wantErr:  true,
		},
		{
			name: "same day", listingID: uuid.New(),
			startsOn: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			endsOn:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			wantErr:  true,
		},
		{
			name: "starts in the past", listingID: uuid.New(),
			startsOn: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			endsOn:   time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
			wantErr:  true,
		},
		{
			name: "exceeds max stay", listingID: uuid.New(),
			startsOn: time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
			endsOn:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC).Add(maxStayNights * 25 * time.Hour),
			wantErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &storeStub{}
			service := newTestService(store)

			_, err := service.CreateBooking(context.Background(), uuid.New(), CreateBookingInput{
				ListingID: test.listingID, StartsOn: test.startsOn, EndsOn: test.endsOn,
			})
			if test.wantErr {
				if !errors.Is(err, ErrInvalidBooking) {
					t.Fatalf("expected ErrInvalidBooking, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCreateBookingSetsGuestAndRequestedStatus(t *testing.T) {
	store := &storeStub{}
	service := newTestService(store)
	guestID := uuid.New()
	listingID := uuid.New()

	created, err := service.CreateBooking(context.Background(), guestID, CreateBookingInput{
		ListingID: listingID,
		StartsOn:  time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		EndsOn:    time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GuestID != guestID || created.ListingID != listingID {
		t.Fatalf("unexpected booking: %+v", created)
	}
	if created.Status != StatusRequested {
		t.Fatalf("expected status %q, got %q", StatusRequested, created.Status)
	}
	if store.created.ID != created.ID {
		t.Fatalf("store received unexpected booking: %+v", store.created)
	}
}

func TestCreateBookingReturnsStoreError(t *testing.T) {
	want := errors.New("database unavailable")
	service := newTestService(&storeStub{err: want})

	_, err := service.CreateBooking(context.Background(), uuid.New(), CreateBookingInput{
		ListingID: uuid.New(),
		StartsOn:  time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		EndsOn:    time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected store error, got %v", err)
	}
}

func TestCreateBookingPropagatesOverlapError(t *testing.T) {
	service := newTestService(&storeStub{err: ErrBookingOverlaps})

	_, err := service.CreateBooking(context.Background(), uuid.New(), CreateBookingInput{
		ListingID: uuid.New(),
		StartsOn:  time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		EndsOn:    time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrBookingOverlaps) {
		t.Fatalf("expected ErrBookingOverlaps, got %v", err)
	}
}

type verificationStub struct {
	id      uuid.UUID
	current bool
	err     error
}

func (stub verificationStub) CurrentVerificationID(context.Context, uuid.UUID) (uuid.UUID, bool, error) {
	return stub.id, stub.current, stub.err
}

func futureStay() CreateBookingInput {
	return CreateBookingInput{
		ListingID: uuid.New(),
		StartsOn:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		EndsOn:    time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC),
	}
}

func TestCreateBookingAttachesCurrentVerification(t *testing.T) {
	store := &storeStub{}
	verificationID := uuid.New()
	service := newTestService(store).WithVerification(verificationStub{id: verificationID, current: true})

	created, err := service.CreateBooking(context.Background(), uuid.New(), futureStay())
	if err != nil {
		t.Fatal(err)
	}
	if created.VerificationID == nil || *created.VerificationID != verificationID {
		t.Fatalf("verification id = %v, want %s", created.VerificationID, verificationID)
	}
	if store.created.VerificationID == nil || *store.created.VerificationID != verificationID {
		t.Fatal("stored booking is missing the verification id")
	}
}

func TestCreateBookingRequiresCurrentVerification(t *testing.T) {
	store := &storeStub{}
	service := newTestService(store).WithVerification(verificationStub{current: false})

	_, err := service.CreateBooking(context.Background(), uuid.New(), futureStay())
	if !errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("expected ErrVerificationRequired, got %v", err)
	}
	if store.created.ID != uuid.Nil || store.created.GuestID != uuid.Nil {
		t.Fatal("nothing may be stored without a current verification")
	}
}

func TestCreateBookingSurfacesVerificationLookupFailure(t *testing.T) {
	lookupErr := errors.New("boom")
	service := newTestService(&storeStub{}).WithVerification(verificationStub{err: lookupErr})

	_, err := service.CreateBooking(context.Background(), uuid.New(), futureStay())
	if !errors.Is(err, lookupErr) || errors.Is(err, ErrVerificationRequired) {
		t.Fatalf("expected the lookup error, got %v", err)
	}
}
