package calendars

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/availability"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type feedStub struct {
	data []byte
	err  error
}

func (stub *feedStub) Fetch(context.Context, string) ([]byte, error) { return stub.data, stub.err }

func TestSyncedDatesBlockBookingsAndOurBookingsAreExported(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
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

	hostID := dbtest.SignedUpAccount(t, ctx, pool)
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Calendar Sync House", Description: "A complete listing used by the calendar sync test.",
		Address: listings.Address{Line1: "4 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published' WHERE id = $1`, listing.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	first := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 40)
	stamp := func(offset int) string { return first.AddDate(0, 0, offset).Format(calendarDate) }
	feed := &feedStub{data: calendarOf(eventOf("UID:x", "DTSTART:"+stamp(0), "DTEND:"+stamp(3)))}
	service := NewCalendarService(NewPostgresCalendarStore(pool), feed)

	if _, err := service.Overview(ctx, uuid.New(), listing.ID); err == nil {
		t.Fatal("another account saw the calendar")
	}
	added, err := service.AddFeed(ctx, hostID, listing.ID, "Other site", "https://other.test/"+uuid.NewString()+".ics")
	if err != nil || len(added.Feeds) != 1 || added.Feeds[0].BlockedNights != 3 || len(added.Blocks) != 1 {
		t.Fatalf("overview %+v, error %v", added, err)
	}

	bookingService := bookings.NewBookingService(bookings.NewPostgresBookingStore(pool))
	book := func(from, to int) (bookings.Booking, error) {
		return bookingService.CreateBooking(ctx, dbtest.SignedUpAccount(t, ctx, pool), bookings.CreateBookingInput{
			ListingID: listing.ID, StartsOn: first.AddDate(0, 0, from), EndsOn: first.AddDate(0, 0, to),
		})
	}
	if _, err := book(1, 4); !errors.Is(err, bookings.ErrBookingOverlaps) {
		t.Fatalf("booking over a synced date: %v", err)
	}
	ours, err := book(3, 6)
	if err != nil {
		t.Fatalf("booking on the checkout day: %v", err)
	}

	booked, err := availability.NewAvailabilityService(availability.NewPostgresAvailabilityStore(pool)).BookedRanges(ctx, listing.ID)
	if err != nil || len(booked) != 2 {
		t.Fatalf("booked dates %v, error %v", booked, err)
	}

	calendar, err := service.Export(ctx, added.ExportToken)
	if err != nil || !strings.Contains(string(calendar), ours.ID.String()) || strings.Contains(string(calendar), "DTSTART;VALUE=DATE:"+stamp(0)) {
		t.Fatalf("export %s, error %v", calendar, err)
	}
	reset, err := service.ResetLink(ctx, hostID, listing.ID)
	if err != nil || reset.ExportToken == added.ExportToken {
		t.Fatalf("reset %+v, error %v", reset, err)
	}
	if _, err := service.Export(ctx, added.ExportToken); !errors.Is(err, ErrCalendarUnknown) {
		t.Fatalf("old link: %v", err)
	}

	feed.data = []byte("<html>")
	failed, err := service.Refresh(ctx, hostID, listing.ID, added.Feeds[0].ID)
	if err != nil || failed.Feeds[0].LastError == "" || len(failed.Blocks) != 1 {
		t.Fatalf("a failed sync must keep dates blocked: %+v, error %v", failed, err)
	}

	feed.data = calendarOf(eventOf("UID:y", "DTSTART:"+stamp(4), "DTEND:"+stamp(8)))
	service.now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	synced, err := service.Overview(ctx, hostID, listing.ID)
	if err != nil || synced.Feeds[0].LastError != "" || len(synced.Conflicts) != 1 {
		t.Fatalf("overview %+v, error %v", synced, err)
	}
	if _, err := book(0, 2); err != nil {
		t.Fatalf("freed dates stayed blocked: %v", err)
	}

	removed, err := service.RemoveFeed(ctx, hostID, listing.ID, added.Feeds[0].ID)
	if err != nil || len(removed.Feeds) != 0 || len(removed.Blocks) != 0 {
		t.Fatalf("removed %+v, error %v", removed, err)
	}
}
