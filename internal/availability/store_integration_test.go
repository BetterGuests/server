package availability

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBookedDatesShowOtherGuestsHoldsButNotCancelledStays(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	hostID := uuid.New()
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Booked Dates House", Description: "A complete listing used by the booked dates test.",
		Address: listings.Address{Line1: "3 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewAvailabilityService(NewPostgresAvailabilityStore(pool))

	if _, err := service.BookedRanges(ctx, listing.ID); !errors.Is(err, ErrListingNotFound) {
		t.Fatalf("expected a draft listing to be hidden, got %v", err)
	}

	setStatus := func(actorID uuid.UUID, statement string, id uuid.UUID) {
		t.Helper()
		err := db.WithUserTx(ctx, pool, actorID.String(), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, statement, id)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	setStatus(hostID, `UPDATE app.listings SET status = 'published' WHERE id = $1`, listing.ID)

	bookingService := bookings.NewBookingService(bookings.NewPostgresBookingStore(pool))
	firstDay := time.Now().UTC().AddDate(0, 0, 20)
	book := func(guestID uuid.UUID, offset int) bookings.Booking {
		t.Helper()
		booking, err := bookingService.CreateBooking(ctx, guestID, bookings.CreateBookingInput{
			ListingID: listing.ID,
			StartsOn:  firstDay.AddDate(0, 0, offset),
			EndsOn:    firstDay.AddDate(0, 0, offset+3),
		})
		if err != nil {
			t.Fatal(err)
		}
		return booking
	}
	book(uuid.New(), 0)
	cancelledGuest := uuid.New()
	cancelled := book(cancelledGuest, 10)
	setStatus(cancelledGuest, `UPDATE app.bookings SET status = 'cancelled' WHERE id = $1`, cancelled.ID)

	ranges, err := service.BookedRanges(ctx, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := BookedRange{
		StartsOn: firstDay.Format(time.DateOnly),
		EndsOn:   firstDay.AddDate(0, 0, 3).Format(time.DateOnly),
	}
	if len(ranges) != 1 || ranges[0] != want {
		t.Fatalf("expected only %+v, got %+v", want, ranges)
	}
}
