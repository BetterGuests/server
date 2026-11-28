package settlements

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type finishedStay struct {
	t                                       *testing.T
	ctx                                     context.Context
	admin                                   *pgx.Conn
	store                                   *PostgresSettlementStore
	review                                  *SettlementReviewService
	reverser                                *reversals
	host, guest, team, listingID, bookingID uuid.UUID
}

func newFinishedStay(t *testing.T) *finishedStay {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin, err := pgx.Connect(ctx, os.Getenv("TEST_SUPERUSER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	store := NewPostgresSettlementStore(pool)
	reverser := &reversals{}
	stay := &finishedStay{
		t: t, ctx: ctx, admin: admin, store: store, review: NewSettlementReviewService(store, reverser), reverser: reverser,
		host: uuid.New(), guest: uuid.New(), team: uuid.New(), bookingID: uuid.New(),
	}
	dbtest.SignUp(t, ctx, pool, stay.host, stay.guest, stay.team)
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, stay.host, listings.CreateListingInput{
		Title: "Held House", Description: "A complete listing used by the payout hold integration test.",
		Address: listings.Address{Line1: "3 Hold Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	stay.listingID = listing.ID
	stay.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, stay.team, stay.team.String()+"@example.test")
	stay.sql(`UPDATE app.listings SET status = 'published' WHERE id = $1`, listing.ID)
	stay.sql(`INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on)
	          VALUES ($1, $2, $3, 'confirmed', DATE '2024-05-01', DATE '2024-05-03')`, stay.bookingID, listing.ID, stay.guest)
	stay.sql(`INSERT INTO app.payments (id, booking_id, guest_id, host_id, stripe_payment_intent_id, amount_cents, currency, status)
	          VALUES ($1, $2, $3, $4, $5, 60000, 'usd', 'succeeded')`, uuid.New(), stay.bookingID, stay.guest, stay.host, "pi_"+uuid.NewString())
	return stay
}

func (stay *finishedStay) sql(statement string, args ...any) {
	stay.t.Helper()
	if _, err := stay.admin.Exec(stay.ctx, statement, args...); err != nil {
		stay.t.Fatal(err)
	}
}

func (stay *finishedStay) due() bool {
	stay.t.Helper()
	day := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)
	due, err := stay.store.ListDue(stay.ctx, day, day, 500)
	if err != nil {
		stay.t.Fatal(err)
	}
	return slices.ContainsFunc(due, func(item Due) bool { return item.BookingID == stay.bookingID })
}

func (stay *finishedStay) record(status Status, reason string) {
	stay.t.Helper()
	err := stay.store.Save(stay.ctx, Settlement{
		ID: uuid.New(), BookingID: stay.bookingID, GuestID: stay.guest, HostID: stay.host,
		FeeCents: 4830, PayoutCents: 55170, Status: status, Reason: reason,
	})
	if err != nil {
		stay.t.Fatal(err)
	}
}

func (stay *finishedStay) blocked(viewer uuid.UUID) (BlockedSettlement, bool) {
	stay.t.Helper()
	found, err := stay.review.Blocked(stay.ctx, viewer)
	if err != nil {
		stay.t.Fatal(err)
	}
	index := slices.IndexFunc(found, func(item BlockedSettlement) bool { return item.BookingID == stay.bookingID })
	if index < 0 {
		return BlockedSettlement{}, false
	}
	return found[index], true
}

func TestATakedownHoldsThePayoutUntilTheListingIsRestored(t *testing.T) {
	for name, status := range map[string]Status{"never started": "", "waiting for the host": StatusAwaitingHost} {
		t.Run(name, func(t *testing.T) {
			stay := newFinishedStay(t)
			if status != "" {
				stay.record(status, "")
			}
			if !stay.due() {
				t.Fatal("a finished stay is not due")
			}
			stay.sql(`INSERT INTO app.listing_suspensions (listing_id, host_id, reason, paused_by) VALUES ($1, $2, 'Fraud report', 'team')`, stay.listingID, stay.host)
			if stay.due() {
				t.Fatal("a taken down listing is still being paid")
			}
			if err := db.WithUserTx(stay.ctx, stay.store.DB, stay.team.String(), func(tx pgx.Tx) error {
				_, err := tx.Exec(stay.ctx, `UPDATE app.listing_suspensions SET lifted_at = now(), lifted_by = $2 WHERE listing_id = $1`, stay.listingID, stay.team)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if !stay.due() {
				t.Fatal("a restored listing is never paid")
			}
		})
	}
}

func TestAnAutomaticPauseDoesNotHoldThePayout(t *testing.T) {
	stay := newFinishedStay(t)
	stay.sql(`INSERT INTO app.listing_suspensions (listing_id, host_id, reason, paused_by) VALUES ($1, $2, 'Too many cancellations', 'host_cancellations')`, stay.listingID, stay.host)
	if !stay.due() {
		t.Fatal("an automatic pause held a finished stay")
	}
}

func TestTheTeamSendsABlockedPayoutBackToTheNextRun(t *testing.T) {
	stay := newFinishedStay(t)
	stay.record(StatusBlocked, reasonDisputed)
	if stay.due() {
		t.Fatal("a blocked payout is due")
	}
	item, found := stay.blocked(stay.team)
	if !found || item.Reason != reasonDisputed || item.ListingTitle != "Held House" || item.PayoutCents != 55170 || item.Currency != "usd" || !item.CanRetry {
		t.Fatalf("blocked = %+v, found = %v", item, found)
	}
	if err := stay.review.Retry(stay.ctx, stay.team, stay.bookingID); err != nil {
		t.Fatal(err)
	}
	if !stay.due() {
		t.Fatal("a retried payout is not due")
	}
	if _, found := stay.blocked(stay.team); found {
		t.Fatal("a retried payout is still listed")
	}
	if err := stay.review.Retry(stay.ctx, stay.team, stay.bookingID); !errors.Is(err, ErrNotBlocked) {
		t.Fatalf("retried twice: %v", err)
	}
}

func TestOnlyAnUninvolvedAdminCanRetry(t *testing.T) {
	stay := newFinishedStay(t)
	stay.record(StatusBlocked, reasonDisputed)
	for name, viewer := range map[string]uuid.UUID{"host": stay.host, "guest": stay.guest} {
		if _, err := stay.review.Blocked(stay.ctx, viewer); !errors.Is(err, ErrNotAdmin) {
			t.Fatalf("%s listed blocked payouts: %v", name, err)
		}
		if err := stay.review.Retry(stay.ctx, viewer, stay.bookingID); !errors.Is(err, ErrNotAdmin) {
			t.Fatalf("%s retried: %v", name, err)
		}
		released, err := stay.store.Retry(stay.ctx, viewer, stay.bookingID)
		if err != nil || released {
			t.Fatalf("the database let the %s retry: %v %v", name, released, err)
		}
	}
	stay.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, stay.host, stay.host.String()+"@example.test")
	if item, found := stay.blocked(stay.host); !found || item.CanRetry {
		t.Fatalf("own payout = %+v", item)
	}
	if err := stay.review.Retry(stay.ctx, stay.host, stay.bookingID); !errors.Is(err, ErrOwnBooking) {
		t.Fatalf("an admin released their own payout: %v", err)
	}
	if released, err := stay.store.Retry(stay.ctx, stay.host, stay.bookingID); err != nil || released {
		t.Fatalf("the database let an admin release their own payout: %v %v", released, err)
	}
}
