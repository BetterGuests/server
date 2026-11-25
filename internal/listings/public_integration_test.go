package listings

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublicReadsReturnOnlyPublishedListingsWithoutAUser(t *testing.T) {
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
	store := NewPostgresListingStore(pool)
	service := NewListingService(store)
	input := CreateListingInput{
		Title: "Public Read House", Description: "A complete listing used by the public read test.",
		Address: Address{Line1: "1 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    EntirePlace, PropertyType: Cabin, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []Amenity{WiFi},
		Photos:    []CreateListingPhotoInput{{ObjectKey: "listings/test/0.jpg", Position: 0}},
	}
	draft, err := service.CreateListing(ctx, hostID, input)
	if err != nil {
		t.Fatal(err)
	}
	published, err := service.CreateListing(ctx, hostID, input)
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published', nightly_price_cents = 25000 WHERE id = $1`, published.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetPublished(ctx, draft.ID); !errors.Is(err, ErrListingNotFound) {
		t.Fatalf("draft listing must not be public, got %v", err)
	}
	got, err := store.GetPublished(ctx, published.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PropertyType != Cabin || got.Price == nil || got.Price.NightlyCents != 25000 {
		t.Fatalf("unexpected listing: %+v", got)
	}
	if len(got.Photos) != 1 || got.Photos[0].ObjectKey != "listings/test/0.jpg" || len(got.Amenities) != 1 {
		t.Fatalf("children not attached: %+v", got)
	}
}
