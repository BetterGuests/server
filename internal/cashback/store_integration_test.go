package cashback

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type scriptedIntents struct{ intentID string }

func (intents scriptedIntents) CreateIntent(context.Context, payments.IntentParams) (payments.Intent, error) {
	return payments.Intent{ID: intents.intentID, ClientSecret: intents.intentID + "_secret"}, nil
}

func TestMoneyBackWorkflowAgainstPostgres(t *testing.T) {
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
		Title: "Cashback House", Description: "A complete listing used by the cashback integration test.",
		Address: listings.Address{Line1: "1 Test Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published', nightly_price_cents = 12000 WHERE id = $1`, listing.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	bookingID := uuid.New()
	if err := db.WithUserTx(ctx, pool, guestID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1)`, guestID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on) VALUES ($1,$2,$3,'requested',CURRENT_DATE + 10,CURRENT_DATE + 12)`, bookingID, listing.ID, guestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithUserTx(ctx, pool, hostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.bookings SET status = 'pending', decided_at = now() WHERE id = $1`, bookingID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	intentID := "pi_integration_" + uuid.NewString()
	paymentStore := payments.NewPostgresPaymentStore(pool)
	paymentService := payments.NewPaymentService(paymentStore, scriptedIntents{intentID: intentID}, nil, payments.Config{})
	verificationStore := verification.NewPostgresVerificationStore(pool)
	refunder := &fakeRefunder{}
	service := NewCashbackService(NewPostgresCashbackStore(pool), verificationStore, refunder, newFakeObjects())
	photo := func(task string) string { return photoKeyPrefix(bookingID, task) + uuid.NewString() + ".jpg" }

	t.Run("checkout charges nights times nightly price", func(t *testing.T) {
		result, err := paymentService.StartCheckout(ctx, guestID, bookingID)
		if err != nil || result.AmountCents != 24000 {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})

	t.Run("host cannot start checkout for a guest's booking", func(t *testing.T) {
		if _, err := paymentService.StartCheckout(ctx, hostID, bookingID); !errors.Is(err, payments.ErrBookingNotFound) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unverified guest with unpaid booking cannot submit", func(t *testing.T) {
		if _, err := service.SubmitPhoto(ctx, guestID, bookingID, "run_dishwasher", photo("run_dishwasher")); !errors.Is(err, ErrNotVerified) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("verified guest with unpaid booking cannot submit", func(t *testing.T) {
		inquiryID := "inq_" + uuid.NewString()
		now := time.Now().UTC()
		if err := verificationStore.Create(ctx, verification.Verification{ID: uuid.New(), AccountID: guestID, PersonaInquiryID: inquiryID, Status: verification.StatusCreated, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := verificationStore.ApplyStatus(ctx, guestID, inquiryID, verification.StatusCompleted, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := service.SubmitPhoto(ctx, guestID, bookingID, "run_dishwasher", photo("run_dishwasher")); !errors.Is(err, ErrBookingNotEarning) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("failed payment does not confirm the booking", func(t *testing.T) {
		if err := paymentStore.ApplyOutcome(ctx, guestID, intentID, payments.StatusFailed, time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := service.SubmitPhoto(ctx, guestID, bookingID, "run_dishwasher", photo("run_dishwasher")); !errors.Is(err, ErrBookingNotEarning) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("successful payment confirms the booking", func(t *testing.T) {
		if err := paymentStore.ApplyOutcome(ctx, guestID, intentID, payments.StatusSucceeded, time.Now()); err != nil {
			t.Fatal(err)
		}
		trip, err := service.Trip(ctx, guestID, bookingID)
		if err != nil || !trip.CanEarn || !trip.Verified {
			t.Fatalf("trip = %+v, err = %v", trip, err)
		}
	})

	t.Run("repeated success webhook is a no-op", func(t *testing.T) {
		if err := paymentStore.ApplyOutcome(ctx, guestID, intentID, payments.StatusSucceeded, time.Now()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("late failure webhook cannot undo a paid booking", func(t *testing.T) {
		if err := paymentStore.ApplyOutcome(ctx, guestID, intentID, payments.StatusFailed, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		trip, _ := service.Trip(ctx, guestID, bookingID)
		if !trip.CanEarn {
			t.Fatal("booking stopped earning after a late failure event")
		}
	})

	t.Run("webhook for another guest's payment is rejected", func(t *testing.T) {
		if err := paymentStore.ApplyOutcome(ctx, strangerID, intentID, payments.StatusSucceeded, time.Now()); !errors.Is(err, payments.ErrPaymentNotFound) {
			t.Fatalf("error = %v", err)
		}
	})

	submissions := map[string]Submission{}
	for _, task := range []string{"run_dishwasher", "empty_fridge", "wipe_counters"} {
		submission, err := service.SubmitPhoto(ctx, guestID, bookingID, task, photo(task))
		if err != nil {
			t.Fatalf("SubmitPhoto(%s) error = %v", task, err)
		}
		submissions[task] = submission
	}

	t.Run("duplicate submission while waiting is locked", func(t *testing.T) {
		if _, err := service.SubmitPhoto(ctx, guestID, bookingID, "run_dishwasher", photo("run_dishwasher")); !errors.Is(err, ErrSubmissionLocked) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("host sees the photos waiting and the guest's trip", func(t *testing.T) {
		pending, err := service.PendingForHost(ctx, hostID)
		if err != nil || len(pending) != 3 {
			t.Fatalf("pending = %d, err = %v", len(pending), err)
		}
		trip, err := service.HostTrip(ctx, hostID, bookingID)
		if err != nil || trip.GuestID != guestID || trip.ListingTitle != "Cashback House" || trip.Bundles[0].Tasks[0].SubmissionID == nil {
			t.Fatalf("trip = %+v, err = %v", trip, err)
		}
	})

	t.Run("a stranger host sees no pending photos and no trip", func(t *testing.T) {
		pending, err := service.PendingForHost(ctx, strangerID)
		if err != nil || len(pending) != 0 {
			t.Fatalf("pending = %d, err = %v", len(pending), err)
		}
		if _, err := service.HostTrip(ctx, strangerID, bookingID); !errors.Is(err, ErrBookingNotFound) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("guest cannot use the host trip endpoint", func(t *testing.T) {
		if _, err := service.HostTrip(ctx, guestID, bookingID); !errors.Is(err, ErrBookingNotFound) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("verification status reads completed for the guest", func(t *testing.T) {
		status, found, err := verificationStore.LatestStatus(ctx, guestID)
		if err != nil || !found || status != verification.StatusCompleted {
			t.Fatalf("status = %s, found = %v, err = %v", status, found, err)
		}
		_, found, err = verificationStore.LatestStatus(ctx, strangerID)
		if err != nil || found {
			t.Fatalf("stranger found = %v, err = %v", found, err)
		}
	})

	t.Run("stranger sees nothing and cannot review", func(t *testing.T) {
		if _, err := service.Trip(ctx, strangerID, bookingID); !errors.Is(err, ErrBookingNotFound) {
			t.Fatalf("trip error = %v", err)
		}
		if _, _, err := service.Review(ctx, strangerID, submissions["run_dishwasher"].ID, DecisionApprove, ""); !errors.Is(err, ErrSubmissionNotFound) {
			t.Fatalf("review error = %v", err)
		}
	})

	t.Run("guest cannot approve their own photo", func(t *testing.T) {
		if _, _, err := service.Review(ctx, guestID, submissions["run_dishwasher"].ID, DecisionApprove, ""); !errors.Is(err, ErrNotHost) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("host approves two and asks for a retake on the third", func(t *testing.T) {
		for _, task := range []string{"run_dishwasher", "empty_fridge"} {
			if _, refund, err := service.Review(ctx, hostID, submissions[task].ID, DecisionApprove, ""); err != nil || refund != nil {
				t.Fatalf("%s: refund = %+v, err = %v", task, refund, err)
			}
		}
		reviewed, refund, err := service.Review(ctx, hostID, submissions["wipe_counters"].ID, DecisionRetake, "Counters still wet")
		if err != nil || refund != nil || reviewed.Status != TaskRetake {
			t.Fatalf("reviewed = %+v, refund = %+v, err = %v", reviewed, refund, err)
		}
		if len(refunder.calls) != 0 {
			t.Fatal("refund issued before the bundle was complete")
		}
	})

	t.Run("duplicate review is rejected", func(t *testing.T) {
		if _, _, err := service.Review(ctx, hostID, submissions["run_dishwasher"].ID, DecisionApprove, ""); !errors.Is(err, ErrNotAwaitingReview) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("guest retakes the photo and the host approval pays fifty dollars", func(t *testing.T) {
		retaken, err := service.SubmitPhoto(ctx, guestID, bookingID, "wipe_counters", photo("wipe_counters"))
		if err != nil || retaken.Status != TaskChecking {
			t.Fatalf("retaken = %+v, err = %v", retaken, err)
		}
		_, refund, err := service.Review(ctx, hostID, retaken.ID, DecisionApprove, "")
		if err != nil || refund == nil || refund.Status != RefundSucceeded || refund.AmountCents != 5000 {
			t.Fatalf("refund = %+v, err = %v", refund, err)
		}
		if len(refunder.calls) != 1 || refunder.calls[0].intentID != intentID || refunder.calls[0].amount != 5000 {
			t.Fatalf("calls = %+v", refunder.calls)
		}
	})

	t.Run("guest sees the money earned back", func(t *testing.T) {
		trip, err := service.Trip(ctx, guestID, bookingID)
		if err != nil || trip.EarnedCents != 5000 || trip.Bundles[0].Status != TaskApproved || trip.Bundles[0].RefundStatus != RefundSucceeded {
			t.Fatalf("trip = %+v, err = %v", trip, err)
		}
	})

	t.Run("a completed bundle cannot be paid twice", func(t *testing.T) {
		refundStore := NewPostgresCashbackStore(pool)
		again, created, err := refundStore.ReserveRefund(ctx, hostID, Refund{ID: uuid.New(), BookingID: bookingID, GuestID: guestID, BundleKey: "kitchen", AmountCents: 5000, Status: RefundPending, CreatedAt: time.Now(), UpdatedAt: time.Now()})
		if err != nil || created || again.Status != RefundSucceeded {
			t.Fatalf("again = %+v, created = %v, err = %v", again, created, err)
		}
	})
}
