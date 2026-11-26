package listings

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBatchedChildInsertsWriteEveryRowAndRejectUnknownAmenities(t *testing.T) {
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
	listing, err := NewListingService(NewPostgresListingStore(pool)).CreateListing(ctx, hostID, CreateListingInput{
		Title: "Batch House", Description: "A complete listing used by the batching integration test.",
		Address: Address{Line1: "1 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    EntirePlace, PropertyType: House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []Amenity{WiFi, KitchenAmenity},
	})
	if err != nil {
		t.Fatal(err)
	}

	count := func(table string) int {
		var total int
		err := db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM app.`+table+` WHERE listing_id = $1`, listing.ID).Scan(&total)
		})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}

	t.Run("every amenity is linked", func(t *testing.T) {
		if got := count("listing_amenities"); got != 2 {
			t.Fatalf("amenities = %d", got)
		}
	})

	t.Run("photos and rules insert in order with optional text", func(t *testing.T) {
		photos := []ListingPhoto{
			{ID: uuid.New(), ObjectKey: "listings/a/1.jpg", AltText: "Front", Position: 0},
			{ID: uuid.New(), ObjectKey: "listings/a/2.jpg", AltText: "", Position: 1},
			{ID: uuid.New(), ObjectKey: "listings/a/3.jpg", AltText: "Pool", Position: 2},
		}
		rules := []HouseRule{{Title: "No parties"}, {Title: "Quiet hours", Description: "After 10 pm"}}
		err := db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
			if err := insertListingPhotos(ctx, tx, listing.ID, photos); err != nil {
				return err
			}
			return insertListingRules(ctx, tx, listing.ID, rules)
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := count("listing_photos"); got != 3 {
			t.Fatalf("photos = %d", got)
		}
		if got := count("listing_rules"); got != 2 {
			t.Fatalf("rules = %d", got)
		}
		var nullAlt, nullDescription int
		err = db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.listing_photos WHERE listing_id = $1 AND alt_text IS NULL`, listing.ID).Scan(&nullAlt); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM app.listing_rules WHERE listing_id = $1 AND description IS NULL`, listing.ID).Scan(&nullDescription)
		})
		if err != nil || nullAlt != 1 || nullDescription != 1 {
			t.Fatalf("nullAlt = %d, nullDescription = %d, err = %v", nullAlt, nullDescription, err)
		}
	})

	t.Run("an unknown amenity fails the whole transaction", func(t *testing.T) {
		err := db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
			return insertListingAmenities(ctx, tx, listing.ID, []Amenity{"not_in_catalog"})
		})
		if err == nil {
			t.Fatal("expected an error for an amenity missing from the catalog")
		}
	})

	t.Run("empty lists are no-ops", func(t *testing.T) {
		err := db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
			if err := insertListingAmenities(ctx, tx, listing.ID, nil); err != nil {
				return err
			}
			if err := insertListingPhotos(ctx, tx, listing.ID, nil); err != nil {
				return err
			}
			return insertListingRules(ctx, tx, listing.ID, nil)
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
