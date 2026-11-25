package settlements

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSettlementAgainstPostgres(t *testing.T) {
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

	hostID, guestID, strangerID := uuid.New(), uuid.New(), uuid.New()
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, hostID, listings.CreateListingInput{
		Title: "Settlement House", Description: "A complete listing used by the settlement integration test.",
		Address: listings.Address{Line1: "2 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	asUser := func(userID uuid.UUID, query string, arguments ...any) {
		t.Helper()
		if err := db.WithUserTx(ctx, pool, userID.String(), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, query, arguments...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	asUser(hostID, `UPDATE app.listings SET status = 'published', nightly_price_cents = 30000 WHERE id = $1`, listing.ID)
	bookingID := uuid.New()
	asUser(guestID, `INSERT INTO app.accounts (id) VALUES ($1)`, guestID)
	asUser(guestID, `INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on) VALUES ($1,$2,$3,'requested',DATE '2025-03-01',DATE '2025-03-03')`, bookingID, listing.ID, guestID)
	asUser(hostID, `UPDATE app.bookings SET status = 'pending', decided_at = now() WHERE id = $1`, bookingID)

	paymentStore := payments.NewPostgresPaymentStore(pool)
	intentID := "pi_settlement_" + uuid.NewString()
	now := time.Now().UTC()
	if err := paymentStore.CreatePayment(ctx, payments.Payment{ID: uuid.New(), BookingID: bookingID, GuestID: guestID, HostID: hostID, StripePaymentIntentID: intentID, AmountCents: 60000, Currency: "usd", Status: payments.StatusPending, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := paymentStore.ApplyOutcome(ctx, guestID, intentID, payments.StatusSucceeded, now); err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"strip_beds", "towels_in_hamper"} {
		asUser(guestID, `INSERT INTO app.booking_task_submissions (id, booking_id, guest_id, task_key, photo_url, status, created_at, updated_at) VALUES ($1,$2,$3,$4,'cashback/photo.jpg','checking',now(),now())`, uuid.New(), bookingID, guestID, task)
	}

	store := NewPostgresSettlementStore(pool)
	window := func(earliest, latest string) []Due {
		t.Helper()
		from, _ := time.Parse(time.DateOnly, earliest)
		to, _ := time.Parse(time.DateOnly, latest)
		due, err := store.ListDue(ctx, from, to, 500)
		if err != nil {
			t.Fatal(err)
		}
		var mine []Due
		for _, booking := range due {
			if booking.BookingID == bookingID {
				mine = append(mine, booking)
			}
		}
		return mine
	}

	t.Run("stays outside the window are not due", func(t *testing.T) {
		if len(window("2025-03-04", "2025-04-01")) != 0 || len(window("2025-01-01", "2025-03-02")) != 0 {
			t.Fatal("booking listed outside its window")
		}
	})

	t.Run("a host account is saved once", func(t *testing.T) {
		first := "acct_" + uuid.NewString()
		saved, err := store.SaveHostAccount(ctx, hostID, first)
		if err != nil || saved != first {
			t.Fatalf("saved = %q, err = %v", saved, err)
		}
		again, err := store.SaveHostAccount(ctx, hostID, "acct_"+uuid.NewString())
		if err != nil || again != first {
			t.Fatalf("again = %q, err = %v", again, err)
		}
		if _, found, err := store.HostAccount(ctx, strangerID); err != nil || found {
			t.Fatalf("stranger found = %v, err = %v", found, err)
		}
	})

	var due Due
	t.Run("a finished stay lists its payment, photos and host account", func(t *testing.T) {
		found := window("2025-01-01", "2025-04-01")
		if len(found) != 1 {
			t.Fatalf("due = %+v", found)
		}
		due = found[0]
		if due.HostID != hostID || due.GuestID != guestID || due.AmountCents != 60000 || due.PaymentIntentID != intentID || len(due.AcceptedTaskKeys) != 2 || due.HostAccountID == "" || due.Saved != nil {
			t.Fatalf("due = %+v", due)
		}
	})

	settlement := Settlement{ID: uuid.New(), BookingID: bookingID, GuestID: guestID, HostID: hostID, FeeCents: 7200, CashbackCents: 5000, RefundDueCents: 5000, PayoutCents: 47800, Status: StatusProcessing}

	t.Run("a stranger cannot record a settlement", func(t *testing.T) {
		forged := settlement
		forged.HostID = strangerID
		if err := store.Save(ctx, forged); err == nil {
			t.Fatal("stranger recorded a settlement")
		}
	})

	t.Run("saved amounts survive a later save", func(t *testing.T) {
		if err := store.Save(ctx, settlement); err != nil {
			t.Fatal(err)
		}
		changed := settlement
		changed.PayoutCents, changed.RefundID, changed.Status = 1, "re_"+bookingID.String(), StatusAwaitingHost
		if err := store.Save(ctx, changed); err != nil {
			t.Fatal(err)
		}
		found := window("2025-01-01", "2025-04-01")
		if len(found) != 1 || found[0].Saved == nil || found[0].Saved.PayoutCents != 47800 || found[0].Saved.RefundID != changed.RefundID {
			t.Fatalf("due = %+v", found)
		}
	})

	t.Run("a settled booking is closed for good", func(t *testing.T) {
		done := settlement
		done.Status, done.TransferID = StatusSettled, "tr_"+bookingID.String()
		if err := store.Save(ctx, done); err != nil {
			t.Fatal(err)
		}
		if len(window("2025-01-01", "2025-04-01")) != 0 {
			t.Fatal("settled booking is still due")
		}
		done.Status = StatusProcessing
		if err := store.Save(ctx, done); err == nil {
			t.Fatal("settled booking was reopened")
		}
	})
}
