package messages

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEachSideOfAConversationSeesTheOtherPersonsPhoto(t *testing.T) {
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

	people := accounts.NewPostgresAccountStore(pool)
	join := func(name, photo string) uuid.UUID {
		clerkUserID := "clerk-" + uuid.NewString()
		id := accounts.DeterministicID(clerkUserID)
		if err := people.Ensure(ctx, accounts.Account{ID: id, ClerkUserID: clerkUserID}); err != nil {
			t.Fatal(err)
		}
		if err := people.Remember(ctx, id, name, photo); err != nil {
			t.Fatal(err)
		}
		return id
	}
	host := join("Luis H.", "https://img.clerk.com/luis")
	guest := join("Maria S.", "https://img.clerk.com/maria")

	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, host, listings.CreateListingInput{
		Title: "Photo House", Description: "A complete listing used by the conversation photo test.",
		Address: listings.Address{Line1: "1 Photo Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.Cabin, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithUserTx(ctx, pool, host.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published', nightly_price_cents = 25000 WHERE id = $1`, listing.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	store := NewPostgresMessageStore(pool)
	conversation, err := store.Open(ctx, guest, uuid.New(), OpenTarget{ListingID: listing.ID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	for viewer, want := range map[uuid.UUID]string{host: "https://img.clerk.com/maria", guest: "https://img.clerk.com/luis"} {
		summary, err := store.Get(ctx, viewer, conversation)
		if err != nil {
			t.Fatal(err)
		}
		if summary.CounterpartyAvatarURL != want {
			t.Fatalf("photo = %q, want %q", summary.CounterpartyAvatarURL, want)
		}
	}
}
