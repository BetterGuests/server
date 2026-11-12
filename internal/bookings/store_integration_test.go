package bookings

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestConcurrentBookingAttemptsOnlyOneWins is the test for the security
// checklist's "prevent overlapping property bookings" / "test concurrent
// booking attempts" items. It fires several CreateBooking calls for the
// same listing and the exact same date range from separate goroutines
// (separate database connections and transactions, like real concurrent
// HTTP requests) and asserts PostgreSQL's exclusion constraint - not a
// Go-level mutex or a check-then-insert - lets exactly one of them succeed.
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

	hostID := uuid.New()
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Concurrency Test House", Description: "A complete listing used by the booking concurrency test.",
		Address: listings.Address{Line1: "1 Test Way", City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
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

	// A WaitGroup-gated start (every goroutine blocks on the same channel
	// close) makes the attempts land on the database at close to the same
	// instant, instead of one finishing before the next even begins.
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
		}(uuid.New())
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

// TestNonOverlappingBookingsBothSucceed guards against the overlap
// constraint being too strict (e.g. rejecting back-to-back stays that don't
// actually share a night).
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

	hostID := uuid.New()
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Back To Back House", Description: "A complete listing used by the non-overlap test case.",
		Address: listings.Address{Line1: "2 Test Way", City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
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

	if _, err := service.CreateBooking(ctx, uuid.New(), CreateBookingInput{ListingID: listing.ID, StartsOn: checkIn, EndsOn: checkOut}); err != nil {
		t.Fatalf("first booking should succeed: %v", err)
	}
	// The second guest checks in the same day the first checks out - no
	// shared night, so this must not be treated as an overlap.
	if _, err := service.CreateBooking(ctx, uuid.New(), CreateBookingInput{ListingID: listing.ID, StartsOn: checkOut, EndsOn: nextCheckOut}); err != nil {
		t.Fatalf("back-to-back booking should succeed: %v", err)
	}
}

func publishListing(ctx context.Context, pool *pgxpool.Pool, hostID, listingID uuid.UUID) error {
	return db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published' WHERE id = $1`, listingID)
		return err
	})
}
