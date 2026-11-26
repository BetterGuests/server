package reviews

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type summaryFixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	service   *ReviewService
	listingID uuid.UUID
	hostID    uuid.UUID
}

func (fixture summaryFixture) reviewedStay(t *testing.T, guestID uuid.UUID, daysAgo int) uuid.UUID {
	t.Helper()
	bookingID := uuid.New()
	if err := db.WithUserTx(fixture.ctx, fixture.pool, guestID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(fixture.ctx,
			`INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on)
			 VALUES ($1, $2, $3, 'requested', CURRENT_DATE - $4::integer, CURRENT_DATE - $4::integer + 1)`,
			bookingID, fixture.listingID, guestID, daysAgo)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	finishAsAdministrator(t, fixture.ctx, bookingID)
	for author, comment := range map[uuid.UUID]string{guestID: "The circuit breaker kept tripping", fixture.hostID: "Quiet guest"} {
		if _, err := fixture.service.CreateReview(fixture.ctx, author, CreateReviewInput{BookingID: bookingID, Rating: 4, Comment: comment}); err != nil {
			t.Fatal(err)
		}
	}
	return bookingID
}

func TestReviewSummariesAreCachedUntilANewReviewIsPublished(t *testing.T) {
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

	hostID, strangerHostID, guestID, otherGuestID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	dbtest.SignUp(t, ctx, pool, hostID, strangerHostID, guestID, otherGuestID)
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Summary House", Description: "A complete listing used by the review summary test.",
		Address: listings.Address{Line1: "2 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi, listings.KitchenAmenity},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := publishListing(ctx, pool, hostID, listing.ID); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresReviewStore(pool)
	summarizer := &summarizerStub{answer: "The house was great, but the circuit breaker kept tripping."}
	fixture := summaryFixture{ctx: ctx, pool: pool, listingID: listing.ID, hostID: hostID,
		service: NewReviewService(store).WithSummaries(store, summarizer)}

	firstBooking := fixture.reviewedStay(t, guestID, 30)

	t.Run("one review is too few to summarize", func(t *testing.T) {
		summary, err := fixture.service.ListingSummary(ctx, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Summary != "" || summary.ReviewCount != 1 || summarizer.calls != 0 {
			t.Fatalf("summary %+v after %d model calls", summary, summarizer.calls)
		}
	})

	fixture.reviewedStay(t, guestID, 20)
	fixture.reviewedStay(t, otherGuestID, 10)

	t.Run("the listing summary is generated once and then read from the cache", func(t *testing.T) {
		for range 2 {
			summary, err := fixture.service.ListingSummary(ctx, listing.ID)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Summary == "" || summary.ReviewCount != 3 {
				t.Fatalf("unexpected summary %+v", summary)
			}
		}
		if summarizer.calls != 1 {
			t.Fatalf("model called %d times", summarizer.calls)
		}
	})

	t.Run("a stale fingerprint cannot overwrite the cache", func(t *testing.T) {
		if err := store.SaveSummary(ctx, uuid.Nil, SummaryOfListing, listing.ID, CachedSummary{
			Fingerprint: "00000000000000000000000000000000", ReviewCount: 3, Summary: "Forged",
		}); err != nil {
			t.Fatal(err)
		}
		state, err := store.SummaryState(ctx, uuid.Nil, SummaryOfListing, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Cached == nil || state.Cached.Summary == "Forged" || state.Cached.Fingerprint != state.Fingerprint {
			t.Fatalf("cache is %+v", state.Cached)
		}
	})

	t.Run("a new review the summary already covers keeps the text", func(t *testing.T) {
		summarizer.stillAccurate = true
		defer func() { summarizer.stillAccurate = false }()
		before, err := store.SummaryState(ctx, uuid.Nil, SummaryOfListing, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		fixture.reviewedStay(t, otherGuestID, 15)
		summary, err := fixture.service.ListingSummary(ctx, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		after, err := store.SummaryState(ctx, uuid.Nil, SummaryOfListing, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		if summarizer.calls != 2 || summary.ReviewCount != 4 || summary.Summary != before.Cached.Summary {
			t.Fatalf("summary %+v after %d model calls", summary, summarizer.calls)
		}
		if !after.Cached.UpdatedAt.Equal(before.Cached.UpdatedAt) || after.Cached.Fingerprint != after.Fingerprint {
			t.Fatalf("kept summary was rewritten: before %+v, after %+v", before.Cached, after.Cached)
		}
	})

	fixture.reviewedStay(t, guestID, 5)

	t.Run("a new published review that changes the picture updates the summary", func(t *testing.T) {
		summarizer.answer = "The house was great, but the circuit breaker and the hot water both failed."
		summary, err := fixture.service.ListingSummary(ctx, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		if summarizer.calls != 3 || summary.ReviewCount != 5 || summary.Summary != summarizer.answer {
			t.Fatalf("summary %+v after %d model calls", summary, summarizer.calls)
		}
	})

	t.Run("the host gets a cached summary of the guest", func(t *testing.T) {
		for range 2 {
			summary, err := fixture.service.BookingGuestSummary(ctx, hostID, firstBooking)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Summary == "" || summary.ReviewCount != 3 {
				t.Fatalf("unexpected guest summary %+v", summary)
			}
		}
		if summarizer.calls != 4 || summarizer.subjects[3] != SummaryOfGuest {
			t.Fatalf("model called %d times for %v", summarizer.calls, summarizer.subjects)
		}
	})

	t.Run("a host the guest never booked cannot read or write the guest summary", func(t *testing.T) {
		state, err := store.SummaryState(ctx, strangerHostID, SummaryOfGuest, guestID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Cached != nil || state.ReviewCount != 0 {
			t.Fatalf("stranger saw %+v", state)
		}
		owned, err := store.SummaryState(ctx, hostID, SummaryOfGuest, guestID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSummary(ctx, strangerHostID, SummaryOfGuest, guestID, CachedSummary{
			Fingerprint: owned.Fingerprint, ReviewCount: owned.ReviewCount, Summary: "Forged",
		}); err != nil {
			t.Fatal(err)
		}
		after, err := store.SummaryState(ctx, hostID, SummaryOfGuest, guestID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Cached == nil || after.Cached.Summary == "Forged" {
			t.Fatalf("stranger overwrote the guest summary: %+v", after.Cached)
		}
	})
}
