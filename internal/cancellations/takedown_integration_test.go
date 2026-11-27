package cancellations_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/availability"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cancellations"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/google/uuid"
)

func (w *world) publiclyListed() bool {
	w.t.Helper()
	found, err := listings.NewPostgresListingStore(w.pool).ListPublished(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	return slices.ContainsFunc(found, func(item listings.PublicListing) bool { return item.ID == w.listingID })
}

func (w *world) team() uuid.UUID {
	w.t.Helper()
	team := w.account()
	w.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, team, team.String()+"@example.test")
	return team
}

func (w *world) publicReadErrors() (page, calendar error) {
	w.t.Helper()
	_, page = listings.NewPostgresListingStore(w.pool).GetPublished(w.ctx, w.listingID)
	_, calendar = availability.NewPostgresAvailabilityStore(w.pool).BookedRanges(w.ctx, w.listingID, time.Now(), 10)
	return page, calendar
}

func TestATakenDownListingLeavesEveryPublicReadUntilTheTeamRestoresIt(t *testing.T) {
	w := newWorld(t)
	team := w.team()
	if !w.publiclyListed() {
		t.Fatal("a published listing is missing from search")
	}
	if err := w.service.PauseListing(w.ctx, team, w.listingID, "Safety check"); err != nil {
		t.Fatal(err)
	}
	if w.publiclyListed() {
		t.Fatal("a taken down listing is still in search")
	}
	page, calendar := w.publicReadErrors()
	if !errors.Is(page, listings.ErrListingNotFound) {
		t.Fatalf("listing page: %v", page)
	}
	if !errors.Is(calendar, availability.ErrListingNotFound) {
		t.Fatalf("booked dates: %v", calendar)
	}
	enquiry := messages.OpenTarget{ListingID: w.listingID}
	if _, err := messages.NewPostgresMessageStore(w.pool).Open(w.ctx, w.account(), uuid.New(), enquiry, time.Now()); !errors.Is(err, messages.ErrConversationNotFound) {
		t.Fatalf("a new enquiry reached a taken down listing: %v", err)
	}

	if err := w.service.UnpauseListing(w.ctx, team, w.listingID); err != nil {
		t.Fatal(err)
	}
	page, calendar = w.publicReadErrors()
	if !w.publiclyListed() || page != nil || calendar != nil {
		t.Fatalf("a restored listing stayed hidden: %v %v", page, calendar)
	}
}

func TestAnAutomaticPauseStaysVisibleAndTheTeamCanStillTakeItDown(t *testing.T) {
	w := newWorld(t)
	team := w.team()
	for _, startsIn := range []int{10, 20, 30} {
		booking, _ := w.book(startsIn, "confirmed")
		if _, err := w.service.Cancel(w.ctx, w.host, booking, 40000); err != nil {
			t.Fatal(err)
		}
	}
	if page, _ := w.publicReadErrors(); page != nil {
		t.Fatalf("an automatic pause hid the listing: %v", page)
	}
	listed := w.adminListing(team)
	if !listed.Paused || listed.TakenDown {
		t.Fatalf("automatic pause shown as %+v", listed)
	}

	if err := w.service.PauseListing(w.ctx, team, w.listingID, "Safety check"); err != nil {
		t.Fatalf("the team could not take down an auto-paused listing: %v", err)
	}
	if page, _ := w.publicReadErrors(); !errors.Is(page, listings.ErrListingNotFound) {
		t.Fatalf("listing page: %v", page)
	}
	if listed = w.adminListing(team); !listed.TakenDown || listed.Reason != "Safety check" {
		t.Fatalf("take down shown as %+v", listed)
	}
	if err := w.service.PauseListing(w.ctx, team, w.listingID, "Again"); !errors.Is(err, cancellations.ErrAlreadyPaused) {
		t.Fatalf("taken down twice: %v", err)
	}
}

func (w *world) adminListing(team uuid.UUID) cancellations.AdminListing {
	w.t.Helper()
	listed, err := w.service.AdminListings(w.ctx, team)
	if err != nil {
		w.t.Fatal(err)
	}
	index := slices.IndexFunc(listed, func(item cancellations.AdminListing) bool { return item.ListingID == w.listingID })
	if index < 0 {
		w.t.Fatal("the listing is missing from the admin list")
	}
	return listed[index]
}

func TestAHostCannotApproveARequestOnAPausedListing(t *testing.T) {
	w := newWorld(t)
	team := w.team()
	requested, _ := w.book(10, "requested")
	retried, _ := w.book(20, "requested")
	w.sql(`UPDATE app.bookings SET status = 'pending', decided_at = now() WHERE id = $1`, retried)
	if err := w.service.PauseListing(w.ctx, team, w.listingID, "Safety check"); err != nil {
		t.Fatal(err)
	}
	store := bookings.NewPostgresBookingStore(w.pool)
	if err := store.Decide(w.ctx, w.host, requested, bookings.StatusPending, time.Now()); !errors.Is(err, bookings.ErrListingPaused) {
		t.Fatalf("the database let an approval through: %v", err)
	}
	approvals := bookings.NewBookingService(store).WithDetails(store)
	for name, booking := range map[string]uuid.UUID{"new request": requested, "retried capture": retried} {
		if _, err := approvals.Approve(w.ctx, w.host, booking); !errors.Is(err, bookings.ErrListingPaused) {
			t.Fatalf("%s approved while paused: %v", name, err)
		}
	}
	if err := store.Decide(w.ctx, w.host, requested, bookings.StatusDeclined, time.Now()); err != nil {
		t.Fatalf("a host could not decline while paused: %v", err)
	}
}
