package cancellations_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/availability"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cancellations"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/settlements"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type refunds struct{ sent []cancellations.PendingRefund }

func (stub *refunds) Refund(_ context.Context, pending cancellations.PendingRefund) (string, error) {
	stub.sent = append(stub.sent, pending)
	return "re_" + uuid.NewString(), nil
}

type noHold struct{}

func (noHold) Release(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type silent struct{}

func (silent) Publish(uuid.UUID, string, any) {}

type world struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	admin     *pgx.Conn
	host      uuid.UUID
	listingID uuid.UUID
	refunds   *refunds
	service   *cancellations.CancellationService
	bookings  *bookings.BookingService
}

func (w *world) sql(statement string, args ...any) {
	w.t.Helper()
	if _, err := w.admin.Exec(w.ctx, statement, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) book(startsIn int, status string) (uuid.UUID, uuid.UUID) {
	w.t.Helper()
	guest := uuid.New()
	start := time.Now().UTC().AddDate(0, 0, startsIn)
	booking, err := w.bookings.CreateBooking(w.ctx, guest, bookings.CreateBookingInput{
		ListingID: w.listingID, StartsOn: start, EndsOn: start.AddDate(0, 0, 2),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if status == "confirmed" {
		w.sql(`UPDATE app.bookings SET status = 'confirmed', decided_at = now() - interval '1 day' WHERE id = $1`, booking.ID)
		w.sql(`INSERT INTO app.payments (id, booking_id, guest_id, host_id, stripe_payment_intent_id, amount_cents, currency, status)
		       VALUES ($1, $2, $3, $4, $5, 40000, 'usd', 'succeeded')`, uuid.New(), booking.ID, guest, w.host, "pi_"+uuid.NewString())
	}
	return booking.ID, guest
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
	admin, err := pgx.Connect(ctx, os.Getenv("TEST_SUPERUSER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	w := &world{t: t, ctx: ctx, pool: pool, admin: admin, host: uuid.New(), refunds: &refunds{}}
	w.service = cancellations.NewCancellationService(cancellations.NewPostgresCancellationStore(pool), w.refunds, noHold{}, silent{})
	w.bookings = bookings.NewBookingService(bookings.NewPostgresBookingStore(pool))
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, w.host, listings.CreateListingInput{
		Title: "Cancel House", Description: "A complete listing used by the cancellation integration test.",
		Address: listings.Address{Line1: "1 Cancel Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.listingID = listing.ID
	w.sql(`UPDATE app.listings SET status = 'published' WHERE id = $1`, listing.ID)
	return w
}

func TestALateGuestCancellationRefundsHalfAndStillPaysTheHost(t *testing.T) {
	w := newWorld(t)
	booking, guest := w.book(2, "confirmed")

	if _, err := w.service.Cancel(w.ctx, uuid.New(), booking, 20000); !errors.Is(err, cancellations.ErrBookingNotFound) {
		t.Fatalf("a stranger cancelled: %v", err)
	}
	options, err := w.service.Cancel(w.ctx, guest, booking, 20000)
	if err != nil || options.Cancellation == nil || options.Cancellation.RefundStatus != cancellations.RefundSucceeded {
		t.Fatalf("cancel: %+v %v", options, err)
	}
	if len(w.refunds.sent) != 1 || w.refunds.sent[0].AmountCents != 20000 {
		t.Fatalf("refunds = %+v", w.refunds.sent)
	}
	if _, err := w.service.Cancel(w.ctx, guest, booking, 20000); !errors.Is(err, cancellations.ErrNotCancellable) {
		t.Fatalf("cancelled twice: %v", err)
	}
	w.sql(`UPDATE app.bookings SET starts_on = CURRENT_DATE - 12, ends_on = CURRENT_DATE - 10 WHERE id = $1`, booking)
	due, err := settlements.NewPostgresSettlementStore(w.pool).ListDue(w.ctx, time.Now().AddDate(0, 0, -30), time.Now(), 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range due {
		if item.BookingID == booking {
			if item.AdjustmentCents != 20000 {
				t.Fatalf("adjustment = %d", item.AdjustmentCents)
			}
			return
		}
	}
	t.Fatal("the host's half is never paid out")
}

func TestThreeHostCancellationsPauseTheListingAndKeepTheDatesBlocked(t *testing.T) {
	w := newWorld(t)
	var first uuid.UUID
	for index, startsIn := range []int{10, 20, 30} {
		booking, _ := w.book(startsIn, "confirmed")
		if index == 0 {
			first = booking
		}
		if _, err := w.service.Cancel(w.ctx, w.host, booking, 40000); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			start := time.Now().UTC().AddDate(0, 0, startsIn)
			_, err := w.bookings.CreateBooking(w.ctx, uuid.New(), bookings.CreateBookingInput{ListingID: w.listingID, StartsOn: start, EndsOn: start.AddDate(0, 0, 2)})
			if !errors.Is(err, bookings.ErrBookingOverlaps) {
				t.Fatalf("dates the host cancelled were rebooked: %v", err)
			}
			booked, err := availability.NewPostgresAvailabilityStore(w.pool).BookedRanges(w.ctx, w.listingID, time.Now(), 50)
			if err != nil || len(booked) != 1 {
				t.Fatalf("calendar = %+v %v", booked, err)
			}
		}
	}
	start := time.Now().UTC().AddDate(0, 0, 60)
	_, err := w.bookings.CreateBooking(w.ctx, uuid.New(), bookings.CreateBookingInput{ListingID: w.listingID, StartsOn: start, EndsOn: start.AddDate(0, 0, 2)})
	if !errors.Is(err, bookings.ErrListingPaused) {
		t.Fatalf("a paused listing took a booking: %v (first %s)", err, first)
	}
}

func TestAnUnansweredRequestExpiresAfterTwelveHours(t *testing.T) {
	w := newWorld(t)
	stale, guest := w.book(10, "requested")
	fresh, freshGuest := w.book(20, "requested")
	w.sql(`UPDATE app.bookings SET created_at = now() - interval '13 hours' WHERE id = $1`, stale)

	if _, err := w.service.ExpireRequests(w.ctx); err != nil {
		t.Fatal(err)
	}
	expired, err := w.service.Options(w.ctx, guest, stale)
	if err != nil || expired.Cancellation == nil || expired.Cancellation.CancelledBy != cancellations.ByExpired {
		t.Fatalf("stale request: %+v %v", expired, err)
	}
	open, err := w.service.Options(w.ctx, freshGuest, fresh)
	if err != nil || open.Cancellation != nil {
		t.Fatalf("fresh request: %+v %v", open, err)
	}
}

func TestOnlyTheTeamDecidesAProblemReport(t *testing.T) {
	w := newWorld(t)
	booking, guest := w.book(1, "confirmed")
	w.sql(`UPDATE app.listings SET check_in_time = '00:00' WHERE id = $1`, w.listingID)
	w.sql(`UPDATE app.bookings SET starts_on = (now() AT TIME ZONE 'America/New_York')::date, ends_on = CURRENT_DATE + 3 WHERE id = $1`, booking)

	if _, err := w.service.Report(w.ctx, guest, booking, "The door code does not work"); err != nil {
		t.Fatal(err)
	}
	for _, account := range []uuid.UUID{guest, w.host} {
		if _, err := w.service.Resolve(w.ctx, account, booking, cancellations.Decision{RefundCents: 40000}); !errors.Is(err, cancellations.ErrNotAdmin) {
			t.Fatalf("a participant decided: %v", err)
		}
	}
	err := db.WithUserTx(w.ctx, w.pool, guest.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(w.ctx, `UPDATE app.stay_reports SET status = 'resolved', refund_cents = amount_cents,
		    refund_status = 'pending', resolved_by = $2, resolved_at = now() WHERE booking_id = $1`, booking, guest)
		if err == nil && tag.RowsAffected() != 0 {
			return errors.New("guest changed the report")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	team := uuid.New()
	w.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, team, team.String()+"@example.test")
	report, err := w.service.Resolve(w.ctx, team, booking, cancellations.Decision{RefundCents: 10000, Note: "Locked out for two hours"})
	if err != nil || report.RefundCents != 10000 {
		t.Fatalf("resolve: %+v %v", report, err)
	}
	options, err := w.service.Options(w.ctx, guest, booking)
	if err != nil || options.Report == nil || options.Report.RefundStatus != cancellations.RefundSucceeded {
		t.Fatalf("refund not recorded: %+v %v", options.Report, err)
	}
}
