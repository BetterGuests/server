package bans_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/bans"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cancellations"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/BetterGuests/BetterGuests-Server/internal/settlements"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type money struct {
	refunded []cancellations.PendingRefund
	released []uuid.UUID
}

func (log *money) Refund(_ context.Context, pending cancellations.PendingRefund) (string, error) {
	log.refunded = append(log.refunded, pending)
	return "re_" + uuid.NewString(), nil
}

func (log *money) Release(_ context.Context, _, bookingID uuid.UUID) error {
	log.released = append(log.released, bookingID)
	return nil
}

type silent struct{}

func (silent) Publish(uuid.UUID, string, any) {}

func (silent) Disconnect(uuid.UUID) {}

type world struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	superuser *pgx.Conn
	store     *bans.PostgresBanStore
	service   *bans.BanService
	cancel    *cancellations.CancellationService
	bookings  *bookings.BookingService
	money     *money
	admin     uuid.UUID
	host      uuid.UUID
	listing   uuid.UUID
}

func (w *world) sql(statement string, args ...any) {
	w.t.Helper()
	if _, err := w.superuser.Exec(w.ctx, statement, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) text(query string, args ...any) string {
	w.t.Helper()
	var value string
	if err := w.superuser.QueryRow(w.ctx, query, args...).Scan(&value); err != nil {
		w.t.Fatal(err)
	}
	return value
}

func (w *world) account() uuid.UUID {
	w.t.Helper()
	return dbtest.SignedUpAccount(w.t, w.ctx, w.pool)
}

func (w *world) grantAdmin(accountID uuid.UUID) {
	w.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, accountID, accountID.String()+"@example.test")
	w.t.Cleanup(func() {
		_, _ = w.superuser.Exec(context.Background(), `DELETE FROM app.admin_accounts WHERE account_id = $1`, accountID)
	})
}

func newWorld(t *testing.T) *world {
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

	w := &world{t: t, ctx: ctx, pool: pool, superuser: superuser, money: &money{}}
	w.admin, w.host = w.account(), w.account()
	w.grantAdmin(w.admin)
	w.store = bans.NewPostgresBanStore(pool)
	w.service = bans.NewBanService(w.store, proofs.NewProofService(proofs.NewPostgresProofStore(pool), nil), silent{})
	w.cancel = cancellations.NewCancellationService(cancellations.NewPostgresCancellationStore(pool), w.money, w.money, silent{})
	w.bookings = bookings.NewBookingService(bookings.NewPostgresBookingStore(pool))
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, w.host, listings.CreateListingInput{
		Title: "Ban House", Description: "A complete listing used by the ban integration test.",
		Address: listings.Address{Line1: "1 Ban Way " + uuid.NewString(), City: "Banville", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.listing = listing.ID
	w.sql(`UPDATE app.listings SET status = 'published', host_name = 'Hana Host' WHERE id = $1`, listing.ID)
	return w
}

func (w *world) request(startsIn int) (uuid.UUID, uuid.UUID, error) {
	guest := w.account()
	start := time.Now().UTC().AddDate(0, 0, startsIn)
	booking, err := w.bookings.CreateBooking(w.ctx, guest, bookings.CreateBookingInput{
		ListingID: w.listing, StartsOn: start, EndsOn: start.AddDate(0, 0, 2),
	})
	return booking.ID, guest, err
}

func (w *world) book(startsIn int, status, payment string) (uuid.UUID, uuid.UUID) {
	w.t.Helper()
	booking, guest, err := w.request(startsIn)
	if err != nil {
		w.t.Fatal(err)
	}
	w.sql(`UPDATE app.bookings SET status = $2 WHERE id = $1`, booking, status)
	w.sql(`INSERT INTO app.payments (id, booking_id, guest_id, host_id, stripe_payment_intent_id, amount_cents, currency, status)
	       VALUES ($1, $2, $3, $4, $5, 40000, 'usd', $6)`, uuid.New(), booking, guest, w.host, "pi_"+uuid.NewString(), payment)
	return booking, guest
}

func (w *world) ban(accountID uuid.UUID) uuid.UUID {
	w.t.Helper()
	banID, err := w.service.Ban(w.ctx, w.admin, bans.Request{AccountID: accountID, Reason: "Fraud."})
	if err != nil {
		w.t.Fatal(err)
	}
	return banID
}

func (w *world) pausedBy() string {
	return w.text(`SELECT COALESCE((SELECT paused_by FROM app.listing_suspensions WHERE listing_id = $1 AND lifted_at IS NULL), '')`, w.listing)
}

func (w *world) payoutAccountFor(booking uuid.UUID) (string, bool) {
	w.t.Helper()
	due, err := settlements.NewPostgresSettlementStore(w.pool).ListDue(w.ctx, time.Now().AddDate(0, 0, -60), time.Now(), 500)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, item := range due {
		if item.BookingID == booking {
			return item.HostAccountID, true
		}
	}
	return "", false
}

func TestBanningAHostPausesListingsAndStopsBookingsUntilTheBanIsLifted(t *testing.T) {
	w := newWorld(t)
	if banned, err := w.store.IsBanned(w.ctx, w.host); err != nil || banned {
		t.Fatalf("before: %v %v", banned, err)
	}
	banID := w.ban(w.host)

	if banned, err := w.store.IsBanned(w.ctx, w.host); err != nil || !banned {
		t.Fatalf("after: %v %v", banned, err)
	}
	if got := w.pausedBy(); got != "ban" {
		t.Fatalf("paused by %q", got)
	}
	if _, _, err := w.request(30); !errors.Is(err, bookings.ErrListingPaused) {
		t.Fatalf("a new booking: %v", err)
	}
	if err := w.cancel.UnpauseListing(w.ctx, w.admin, w.listing); !errors.Is(err, cancellations.ErrHostBanned) {
		t.Fatalf("reopening while banned: %v", err)
	}
	w.sql(`DELETE FROM app.listing_suspensions WHERE listing_id = $1`, w.listing)
	if _, _, err := w.request(34); !errors.Is(err, bookings.ErrListingPaused) {
		t.Fatalf("the ban must refuse bookings without a pause row: %v", err)
	}
	w.sql(`INSERT INTO app.listing_suspensions (listing_id, host_id, reason, paused_by) VALUES ($1, $2, 'Account suspended', 'ban')`, w.listing, w.host)
	if _, err := w.service.Ban(w.ctx, w.admin, bans.Request{AccountID: w.host, Reason: "again"}); !errors.Is(err, bans.ErrAlreadyBanned) {
		t.Fatalf("banned twice: %v", err)
	}

	if err := w.service.Lift(w.ctx, w.admin, banID); err != nil {
		t.Fatal(err)
	}
	if banned, _ := w.store.IsBanned(w.ctx, w.host); banned || w.pausedBy() != "" {
		t.Fatalf("after lifting: banned=%v paused=%q", banned, w.pausedBy())
	}
	if _, _, err := w.request(38); err != nil {
		t.Fatalf("booking after lifting: %v", err)
	}
	if err := w.service.Lift(w.ctx, w.admin, banID); !errors.Is(err, bans.ErrBanNotFound) {
		t.Fatalf("lifted twice: %v", err)
	}
	w.ban(w.host)
}

func TestLiftingABanKeepsAPauseTheTeamMadeForAnotherReason(t *testing.T) {
	w := newWorld(t)
	if err := w.cancel.PauseListing(w.ctx, w.admin, w.listing, "Photos under review"); err != nil {
		t.Fatal(err)
	}
	if err := w.service.Lift(w.ctx, w.admin, w.ban(w.host)); err != nil {
		t.Fatal(err)
	}
	if got := w.pausedBy(); got != "team" {
		t.Fatalf("paused by %q", got)
	}
}

func TestABanMovesNoMoneyUntilTheTeamCancelsABooking(t *testing.T) {
	w := newWorld(t)
	paid, paidGuest := w.book(20, "confirmed", "succeeded")
	held, _ := w.book(30, "requested", "authorized")
	underway, _ := w.book(40, "confirmed", "succeeded")
	w.sql(`UPDATE app.bookings SET starts_on = CURRENT_DATE - 1, ends_on = CURRENT_DATE + 2 WHERE id = $1`, underway)
	if err := w.cancel.TeamCancel(w.ctx, w.admin, paid); !errors.Is(err, cancellations.ErrBookingNotFound) {
		t.Fatalf("cancelled a booking with nobody banned: %v", err)
	}

	w.ban(w.host)
	if len(w.money.refunded) != 0 || len(w.money.released) != 0 {
		t.Fatalf("the ban moved money: %+v", w.money)
	}
	list, err := w.service.List(w.ctx, w.admin)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(list, func(ban bans.Ban) bool { return ban.AccountID == w.host })
	if index < 0 || list[index].Name != "Hana Host" || len(list[index].Bookings) != 3 || !list[index].Bookings[0].BannedIsHost {
		t.Fatalf("listed = %+v", list)
	}

	if err := w.cancel.TeamCancel(w.ctx, paidGuest, paid); !errors.Is(err, cancellations.ErrNotAdmin) {
		t.Fatalf("a guest used the team cancellation: %v", err)
	}
	if err := w.cancel.TeamCancel(w.ctx, w.admin, paid); err != nil {
		t.Fatalf("cancel the paid booking: %v", err)
	}
	if len(w.money.refunded) != 1 || w.money.refunded[0].AmountCents != 40000 || w.money.refunded[0].GuestID != paidGuest {
		t.Fatalf("refunds = %+v", w.money.refunded)
	}
	if got := w.text(`SELECT cancelled_by || ' ' || refund_cents || ' ' || refund_status FROM app.booking_cancellations WHERE booking_id = $1`, paid); got != "team 40000 succeeded" {
		t.Fatalf("cancellation = %q", got)
	}
	if err := w.cancel.TeamCancel(w.ctx, w.admin, held); err != nil {
		t.Fatalf("cancel the held request: %v", err)
	}
	if len(w.money.released) != 1 || w.money.released[0] != held || len(w.money.refunded) != 1 {
		t.Fatalf("money = %+v", w.money)
	}
	for _, booking := range []uuid.UUID{paid, held} {
		if got := w.text(`SELECT status FROM app.bookings WHERE id = $1`, booking); got != "cancelled" {
			t.Fatalf("status = %q", got)
		}
	}
	if err := w.cancel.TeamCancel(w.ctx, w.admin, underway); !errors.Is(err, cancellations.ErrStayUnderway) {
		t.Fatalf("cancelled a stay underway: %v", err)
	}
	if err := w.cancel.TeamCancel(w.ctx, w.admin, paid); !errors.Is(err, cancellations.ErrBookingNotFound) {
		t.Fatalf("cancelled twice: %v", err)
	}
}

func TestABannedHostsPayoutWaitsAndResumesAfterTheBanIsLifted(t *testing.T) {
	w := newWorld(t)
	booking, _ := w.book(10, "confirmed", "succeeded")
	w.sql(`UPDATE app.bookings SET status = 'completed', completed_at = now() - interval '6 days', starts_on = CURRENT_DATE - 8, ends_on = CURRENT_DATE - 6 WHERE id = $1`, booking)
	payoutAccount := "acct_" + uuid.NewString()
	w.sql(`INSERT INTO app.host_payout_accounts (account_id, stripe_account_id) VALUES ($1, $2)`, w.host, payoutAccount)
	if got, due := w.payoutAccountFor(booking); !due || got != payoutAccount {
		t.Fatalf("before: %q %v", got, due)
	}

	banID := w.ban(w.host)
	if got, due := w.payoutAccountFor(booking); !due || got != "" {
		t.Fatalf("while banned the guest is still settled and the host has nowhere to be paid: %q %v", got, due)
	}
	w.sql(`INSERT INTO app.booking_settlements (id, booking_id, guest_id, host_id, fee_cents, cashback_cents, refund_due_cents, payout_cents, status)
	       SELECT $1, $2, guest_id, host_id, 3230, 0, 0, 36770, 'awaiting_host' FROM app.payments WHERE booking_id = $2`, uuid.New(), booking)
	if _, err := notifications.NewPostgresNotificationStore(w.pool).QueueTeamAlerts(w.ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.text(`SELECT count(*)::text FROM app.notifications WHERE account_id = $1 AND kind = 'payout_setup_needed'`, w.host); got != "0" {
		t.Fatalf("asked a banned host to set up payouts %s times", got)
	}

	if err := w.service.Lift(w.ctx, w.admin, banID); err != nil {
		t.Fatal(err)
	}
	if got, due := w.payoutAccountFor(booking); !due || got != payoutAccount {
		t.Fatalf("after lifting: %q %v", got, due)
	}
}

func TestTheDatabaseKeepsABannedGuestFromBeingApprovedOrBooking(t *testing.T) {
	w := newWorld(t)
	booking, guest := w.book(20, "requested", "authorized")
	w.ban(guest)
	store := bookings.NewPostgresBookingStore(w.pool)
	if err := store.Decide(w.ctx, w.host, booking, bookings.StatusPending, time.Now()); err == nil {
		t.Fatal("a banned guest was approved")
	}
	start := time.Now().UTC().AddDate(0, 0, 60)
	if _, err := w.bookings.CreateBooking(w.ctx, guest, bookings.CreateBookingInput{
		ListingID: w.listing, StartsOn: start, EndsOn: start.AddDate(0, 0, 2),
	}); err == nil {
		t.Fatal("a banned guest booked")
	}
}

func TestTheDatabaseOnlyLetsAdminsBanMembers(t *testing.T) {
	w := newWorld(t)
	member, target, otherAdmin := w.account(), w.account(), w.account()
	w.grantAdmin(otherAdmin)
	insert := func(actor, subject uuid.UUID) error {
		return db.WithUserTx(w.ctx, w.pool, actor.String(), func(tx pgx.Tx) error {
			_, err := tx.Exec(w.ctx,
				`INSERT INTO app.account_bans (id, account_id, account_name, reason, banned_by) VALUES ($1, $2, 'x', 'x', $3)`,
				uuid.New(), subject, actor)
			return err
		})
	}
	if insert(member, target) == nil {
		t.Fatal("a member banned someone")
	}
	if insert(w.admin, otherAdmin) == nil {
		t.Fatal("an admin was banned")
	}
	if _, err := w.service.Ban(w.ctx, w.admin, bans.Request{AccountID: uuid.New(), Reason: "x"}); !errors.Is(err, bans.ErrAccountNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	w.ban(target)
	var seen int
	if err := db.WithUserTx(w.ctx, w.pool, member.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT count(*) FROM app.account_bans`).Scan(&seen)
	}); err != nil || seen != 0 {
		t.Fatalf("a member sees %d bans (%v)", seen, err)
	}
}

func TestTheTeamSeesOpenAndResolvedDisputesAndAPartyCannotDecide(t *testing.T) {
	w := newWorld(t)
	booking, guest := w.book(1, "confirmed", "succeeded")
	w.sql(`INSERT INTO app.account_names (account_id, display_name) VALUES ($1, 'Gil Guest')
	       ON CONFLICT (account_id) DO UPDATE SET display_name = excluded.display_name`, guest)
	w.sql(`UPDATE app.listings SET check_in_time = '00:00' WHERE id = $1`, w.listing)
	w.sql(`UPDATE app.bookings SET starts_on = (now() AT TIME ZONE 'America/New_York')::date, ends_on = CURRENT_DATE + 3 WHERE id = $1`, booking)
	if _, err := w.cancel.Report(w.ctx, guest, booking, "No hot water."); err != nil {
		t.Fatal(err)
	}
	find := func(viewer uuid.UUID, status cancellations.ReportStatus) *cancellations.TeamReport {
		reports, err := w.cancel.Reports(w.ctx, viewer, status)
		if err != nil {
			t.Fatal(err)
		}
		for index := range reports {
			if reports[index].BookingID == booking {
				return &reports[index]
			}
		}
		return nil
	}
	open := find(w.admin, cancellations.ReportOpen)
	if open == nil || open.GuestName != "Gil Guest" || open.HostName != "Hana Host" || !open.CanDecide || open.ResolvedAt != nil {
		t.Fatalf("open = %+v", open)
	}
	w.grantAdmin(w.host)
	if mine := find(w.host, cancellations.ReportOpen); mine == nil || mine.CanDecide {
		t.Fatalf("the host admin sees %+v", mine)
	}
	if _, err := w.cancel.Resolve(w.ctx, w.host, booking, cancellations.Decision{}); !errors.Is(err, cancellations.ErrPartyToReport) {
		t.Fatalf("party decision: %v", err)
	}
	if _, err := w.cancel.Resolve(w.ctx, w.admin, booking, cancellations.Decision{RefundCents: 10000, Note: "Sorry."}); err != nil {
		t.Fatal(err)
	}
	if find(w.admin, cancellations.ReportOpen) != nil {
		t.Fatal("still open")
	}
	closed := find(w.admin, cancellations.ReportResolved)
	if closed == nil || closed.RefundCents != 10000 || closed.Note != "Sorry." || closed.ResolvedAt == nil {
		t.Fatalf("resolved = %+v", closed)
	}
}
