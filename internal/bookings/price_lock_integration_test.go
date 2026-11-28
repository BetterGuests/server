package bookings

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPriceChangeAfterTheRequestDoesNotChangeWhatTheGuestPays(t *testing.T) {
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
	listingStore := listings.NewPostgresListingStore(pool)
	listing, err := listings.NewListingService(listingStore).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Locked Price House", Description: "A complete listing used by the price lock test.",
		Address: listings.Address{Line1: "9 Price Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
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
	if err := listingStore.SetPrice(ctx, hostID.String(), listing.ID, 20000); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresBookingStore(pool)
	guestID := dbtest.SignedUpAccount(t, ctx, pool)
	checkIn := time.Now().UTC().AddDate(0, 0, 40)
	booking, err := NewBookingService(store).CreateBooking(ctx, guestID, CreateBookingInput{ListingID: listing.ID, StartsOn: checkIn, EndsOn: checkIn.AddDate(0, 0, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if err := listingStore.SetPrice(ctx, hostID.String(), listing.ID, 35000); err != nil {
		t.Fatal(err)
	}

	quote, err := payments.NewPostgresPaymentStore(pool).QuoteBooking(ctx, guestID, booking.ID)
	if err != nil {
		t.Fatal(err)
	}
	if quote.NightlyPriceCents != 20000 {
		t.Fatalf("quoted nightly price = %d, want the 20000 seen at request time", quote.NightlyPriceCents)
	}
	detail, err := store.Get(ctx, guestID, booking.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TotalCents != 40000 {
		t.Fatalf("total = %d, want 40000", detail.TotalCents)
	}
}
