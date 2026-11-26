package cashback

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
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
	dbtest.SignUp(t, ctx, pool, hostID, guestID, strangerID)
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
		_, err := tx.Exec(ctx, `INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on) VALUES ($1,$2,$3,'requested',CURRENT_DATE - 2,CURRENT_DATE)`, bookingID, listing.ID, guestID)
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
	service := NewCashbackService(NewPostgresCashbackStore(pool), verificationStore, newFakeObjects())
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
		attempts, err := verificationStore.Attempts(ctx, guestID)
		if err != nil || len(attempts) != 1 || attempts[0].Status != verification.StatusCompleted {
			t.Fatalf("attempts = %+v, err = %v", attempts, err)
		}
		attempts, err = verificationStore.Attempts(ctx, strangerID)
		if err != nil || len(attempts) != 0 {
			t.Fatalf("stranger attempts = %+v, err = %v", attempts, err)
		}
	})

	t.Run("stranger sees nothing and cannot review", func(t *testing.T) {
		if _, err := service.Trip(ctx, strangerID, bookingID); !errors.Is(err, ErrBookingNotFound) {
			t.Fatalf("trip error = %v", err)
		}
		if _, err := service.Review(ctx, strangerID, submissions["run_dishwasher"].ID, DecisionApprove, ""); !errors.Is(err, ErrSubmissionNotFound) {
			t.Fatalf("review error = %v", err)
		}
	})

	t.Run("guest cannot approve their own photo", func(t *testing.T) {
		if _, err := service.Review(ctx, guestID, submissions["run_dishwasher"].ID, DecisionApprove, ""); !errors.Is(err, ErrNotHost) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("host approves two and rejects the third for good", func(t *testing.T) {
		for _, task := range []string{"run_dishwasher", "empty_fridge"} {
			if _, err := service.Review(ctx, hostID, submissions[task].ID, DecisionApprove, ""); err != nil {
				t.Fatalf("%s: err = %v", task, err)
			}
		}
		reviewed, err := service.Review(ctx, hostID, submissions["wipe_counters"].ID, DecisionReject, "Counters still wet")
		if err != nil || reviewed.Status != TaskRejected {
			t.Fatalf("reviewed = %+v, err = %v", reviewed, err)
		}
		if _, err := service.SubmitPhoto(ctx, guestID, bookingID, "wipe_counters", photo("wipe_counters")); !errors.Is(err, ErrSubmissionLocked) {
			t.Fatalf("resubmit error = %v", err)
		}
	})

	t.Run("duplicate review is rejected", func(t *testing.T) {
		if _, err := service.Review(ctx, hostID, submissions["run_dishwasher"].ID, DecisionApprove, ""); !errors.Is(err, ErrNotAwaitingReview) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("nothing is earned before settlement", func(t *testing.T) {
		trip, err := service.Trip(ctx, guestID, bookingID)
		if err != nil || trip.EarnedCents != 0 || trip.ExpectedCents != 0 || trip.CapCents != 6000 || trip.Bundles[0].Status != TaskRejected {
			t.Fatalf("trip = %+v, err = %v", trip, err)
		}
	})
}
