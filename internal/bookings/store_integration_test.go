package bookings

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConcurrentBookingAttemptsOnlyOneWins(t *testing.T) {
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

	hostID := dbtest.SignedUpAccount(t, ctx, pool)
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Concurrency Test House", Description: "A complete listing used by the booking concurrency test.",
		Address: listings.Address{Line1: "1 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := publishListing(ctx, pool, hostID, listing.ID); err != nil {
		t.Fatal(err)
	}

	service := NewBookingService(NewPostgresBookingStore(pool))
	input := CreateBookingInput{
		ListingID: listing.ID,
		StartsOn:  time.Now().UTC().AddDate(0, 0, 7),
		EndsOn:    time.Now().UTC().AddDate(0, 0, 10),
	}

	const attempts = 5
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		overlaps  int
		other     []error
	)

	start := make(chan struct{})
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(guestID uuid.UUID) {
			defer wg.Done()
			<-start
			_, err := service.CreateBooking(ctx, guestID, input)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, ErrBookingOverlaps):
				overlaps++
			default:
				other = append(other, err)
			}
		}(dbtest.SignedUpAccount(t, ctx, pool))
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors from concurrent bookings: %v", other)
	}
	if succeeded != 1 {
		t.Fatalf("expected exactly 1 of %d concurrent overlapping bookings to succeed, got %d", attempts, succeeded)
	}
	if overlaps != attempts-1 {
		t.Fatalf("expected %d bookings to be rejected as overlapping, got %d", attempts-1, overlaps)
	}
}

func TestNonOverlappingBookingsBothSucceed(t *testing.T) {
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

	hostID := dbtest.SignedUpAccount(t, ctx, pool)
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Back To Back House", Description: "A complete listing used by the non-overlap test case.",
		Address: listings.Address{Line1: "2 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := publishListing(ctx, pool, hostID, listing.ID); err != nil {
		t.Fatal(err)
	}

	service := NewBookingService(NewPostgresBookingStore(pool))
	checkIn := time.Now().UTC().AddDate(0, 0, 30)
	checkOut := checkIn.AddDate(0, 0, 3)
	nextCheckOut := checkOut.AddDate(0, 0, 3)

	if _, err := service.CreateBooking(ctx, dbtest.SignedUpAccount(t, ctx, pool), CreateBookingInput{ListingID: listing.ID, StartsOn: checkIn, EndsOn: checkOut}); err != nil {
		t.Fatalf("first booking should succeed: %v", err)
	}
	if _, err := service.CreateBooking(ctx, dbtest.SignedUpAccount(t, ctx, pool), CreateBookingInput{ListingID: listing.ID, StartsOn: checkOut, EndsOn: nextCheckOut}); err != nil {
		t.Fatalf("back-to-back booking should succeed: %v", err)
	}
}

func publishListing(ctx context.Context, pool *pgxpool.Pool, hostID, listingID uuid.UUID) error {
	return db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published' WHERE id = $1`, listingID)
		return err
	})
}

func TestOneGuestCannotRaceTwoTripsOntoTheSameDates(t *testing.T) {
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

	const attempts = 4
	var places []uuid.UUID
	for range attempts {
		hostID := dbtest.SignedUpAccount(t, ctx, pool)
		listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
			Title: "One Trip House", Description: "A complete listing used by the one trip at a time test.",
			Address: listings.Address{Line1: "3 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
			Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
			MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
			Amenities: []listings.Amenity{listings.WiFi},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := publishListing(ctx, pool, hostID, listing.ID); err != nil {
			t.Fatal(err)
		}
		places = append(places, listing.ID)
	}

	store := NewPostgresBookingStore(pool)
	service := NewBookingService(store)
	guestID := dbtest.SignedUpAccount(t, ctx, pool)
	checkIn := time.Now().UTC().AddDate(0, 0, 40)
	results := make([]error, attempts)
	var group sync.WaitGroup
	for index, place := range places {
		group.Add(1)
		go func() {
			defer group.Done()
			_, results[index] = service.CreateBooking(ctx, guestID, CreateBookingInput{ListingID: place, StartsOn: checkIn, EndsOn: checkIn.AddDate(0, 0, 3)})
		}()
	}
	group.Wait()

	created := 0
	for _, err := range results {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, ErrTripOverlaps):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("expected exactly one trip, got %d", created)
	}

	if _, err := service.CreateBooking(ctx, guestID, CreateBookingInput{ListingID: places[0], StartsOn: checkIn.AddDate(0, 0, 3), EndsOn: checkIn.AddDate(0, 0, 5)}); err != nil {
		t.Fatalf("checking in on the checkout day must work: %v", err)
	}

	listed, err := store.ListForGuest(ctx, guestID)
	if err != nil || len(listed) != 0 {
		t.Fatalf("requests without a card hold must not show as trips: %d, err = %v", len(listed), err)
	}
	overlapping, err := store.ListOverlapping(ctx, guestID, checkIn, checkIn.AddDate(0, 0, 3))
	if err != nil || len(overlapping) != 1 {
		t.Fatalf("overlapping = %d, err = %v", len(overlapping), err)
	}
}
