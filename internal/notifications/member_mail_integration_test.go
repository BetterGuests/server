package notifications

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAnUnreadMessageIsEmailedOnceAndStopsAfterUnsubscribing(t *testing.T) {
	databaseURL, superuserURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_SUPERUSER_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	admin, err := pgxpool.New(ctx, superuserURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Skip("needs the disposable database superuser")
	}

	hostID, guestID := dbtest.SignedUpAccount(t, ctx, pool), dbtest.SignedUpAccount(t, ctx, pool)
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Mail Test House", Description: "A complete listing used by the message email test.",
		Address: listings.Address{Line1: "3 Mail Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published' WHERE id = $1`, listing.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	service := messages.NewMessageService(messages.NewPostgresMessageStore(pool))
	conversation, err := service.Open(ctx, guestID, messages.OpenTarget{ListingID: listing.ID})
	if err != nil {
		t.Fatal(err)
	}
	send := func() {
		if _, err := service.Send(ctx, guestID, conversation.ID, messages.Draft{ID: uuid.New(), Body: "Is there parking?"}); err != nil {
			t.Fatal(err)
		}
	}
	age := func() {
		if _, err := admin.Exec(ctx, `UPDATE app.messages SET created_at = created_at - interval '10 minutes' WHERE conversation_id = $1`, conversation.ID); err != nil {
			t.Fatal(err)
		}
	}
	store := NewPostgresNotificationStore(pool)
	waiting := func() []PendingEmail {
		all, err := store.AwaitingEmail(ctx, 200)
		if err != nil {
			t.Fatal(err)
		}
		var mine []PendingEmail
		for _, item := range all {
			if item.ConversationID == conversation.ID {
				mine = append(mine, item)
			}
		}
		return mine
	}

	send()
	if fresh := waiting(); len(fresh) != 0 {
		t.Fatalf("a message just sent must wait for the host to read it: %+v", fresh)
	}
	age()
	due := waiting()
	if len(due) != 1 || due[0].AccountID != hostID || !due[0].AsHost || due[0].Category != categoryMessages {
		t.Fatalf("due = %+v", due)
	}
	if err := store.RecordEmailAttempt(ctx, due[0].NotificationID, true); err != nil {
		t.Fatal(err)
	}
	send()
	if again := waiting(); len(again) != 0 {
		t.Fatalf("one email per unread run: %+v", again)
	}
	if found, err := store.Unsubscribe(ctx, due[0].Unsubscribe, categoryMessages); err != nil || !found {
		t.Fatalf("unsubscribe: %v %v", found, err)
	}
	if _, err := admin.Exec(ctx, `UPDATE app.conversations SET host_read_at = now() - interval '5 minutes' WHERE id = $1`, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE app.messages SET created_at = now() - interval '6 minutes' WHERE conversation_id = $1`, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE app.conversations SET host_read_at = now() - interval '8 minutes' WHERE id = $1`, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if after := waiting(); len(after) != 0 {
		t.Fatalf("unsubscribed host still emailed: %+v", after)
	}
}
