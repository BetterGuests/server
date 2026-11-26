package teamevents_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/teamevents"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTheTeamFeedShowsBookingsAndPaymentsOnlyToAdmins(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	superuser, err := pgx.Connect(ctx, os.Getenv("TEST_SUPERUSER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })
	run := func(statement string, args ...any) {
		t.Helper()
		if _, err := superuser.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}

	host, guest, admin := uuid.New(), uuid.New(), uuid.New()
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, host, listings.CreateListingInput{
		Title: "Feed House", Description: "A complete listing used by the team feed integration test.",
		Address: listings.Address{Line1: "1 Feed Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.45, Longitude: -112.07},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	run(`UPDATE app.listings SET status = 'published', published_at = now() WHERE id = $1`, listing.ID)
	start := time.Now().UTC().AddDate(0, 0, 30)
	booking, err := bookings.NewBookingService(bookings.NewPostgresBookingStore(pool)).CreateBooking(ctx, guest, bookings.CreateBookingInput{
		ListingID: listing.ID, StartsOn: start, EndsOn: start.AddDate(0, 0, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	run(`INSERT INTO app.payments (id, booking_id, guest_id, host_id, stripe_payment_intent_id, amount_cents, currency, status)
	     VALUES ($1, $2, $3, $4, $5, 40000, 'usd', 'succeeded')`, uuid.New(), booking.ID, guest, host, "pi_"+uuid.NewString())
	run(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, admin, admin.String()+"@team.test")

	store := teamevents.NewPostgresTeamEventsStore(pool)
	query := teamevents.Query{Since: time.Now().Add(-time.Hour), Kinds: teamevents.AllKinds, Limit: 500}

	seen := map[teamevents.Kind]teamevents.Event{}
	events, err := store.Events(ctx, admin, query)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.ListingID != nil && *event.ListingID == listing.ID {
			seen[event.Kind] = event
		}
	}
	for _, kind := range []teamevents.Kind{teamevents.BookingRequested, teamevents.PaymentSucceeded, teamevents.ListingPublished} {
		if _, ok := seen[kind]; !ok {
			t.Fatalf("admin feed is missing %s: %+v", kind, seen)
		}
	}
	payment := seen[teamevents.PaymentSucceeded]
	if payment.AmountCents != 40000 || payment.Latitude == nil || *payment.Latitude != 33.45 || payment.GuestAccountID == nil || *payment.GuestAccountID != guest {
		t.Fatalf("payment event = %+v", payment)
	}

	strangerEvents, err := store.Events(ctx, uuid.New(), query)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range strangerEvents {
		if event.Kind == teamevents.PaymentSucceeded && event.RefID == payment.RefID {
			t.Fatal("a stranger saw someone else's payment")
		}
	}
}
