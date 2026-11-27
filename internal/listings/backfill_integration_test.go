package listings

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBackfillStoreSeesOnlyPublishedPhotosAndReplacesAKeyOnce(t *testing.T) {
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
	store := NewPostgresListingStore(pool)
	listing, err := NewListingService(store).CreateListing(ctx, hostID, CreateListingInput{
		Title: "Backfill House", Description: "A complete listing used by the photo backfill test.",
		Address: Address{Line1: "2 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    EntirePlace, PropertyType: Cabin, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []Amenity{WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	photoID, oldKey := uuid.New(), "listings/"+listing.ID.String()+"/"+uuid.NewString()+".jpg"
	if err := store.AddPhoto(ctx, hostID.String(), listing.ID, ListingPhoto{ID: photoID, ObjectKey: oldKey, Screening: ScreeningPassed}); err != nil {
		t.Fatal(err)
	}
	wanted := PhotoToResize{HostID: hostID, ListingID: listing.ID, PhotoID: photoID, ObjectKey: oldKey}

	drafts, err := store.PublishedPhotos(ctx)
	if err != nil || slices.Contains(drafts, wanted) {
		t.Fatalf("draft photo listed: %v, err %v", drafts, err)
	}
	err = db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published', nightly_price_cents = 25000 WHERE id = $1`, listing.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := store.PublishedPhotos(ctx)
	if err != nil || !slices.Contains(published, wanted) {
		t.Fatalf("published photo missing: %v, err %v", published, err)
	}

	first, err := store.ReplacePhotoKey(ctx, wanted, oldKey+"x")
	if err != nil || !first {
		t.Fatalf("replace = %v, err %v", first, err)
	}
	again, err := store.ReplacePhotoKey(ctx, wanted, oldKey+"y")
	if err != nil || again {
		t.Fatalf("stale replace = %v, err %v", again, err)
	}
}
