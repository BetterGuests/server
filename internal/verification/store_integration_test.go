package verification

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVerificationStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store := NewPostgresVerificationStore(pool)
	accountID, otherAccountID := uuid.New(), uuid.New()
	inquiryID := "inq_" + uuid.NewString()
	base := time.Now().UTC().Truncate(time.Second)

	create := func(account uuid.UUID, inquiry string) error {
		return store.Create(ctx, Verification{ID: uuid.New(), AccountID: account, PersonaInquiryID: inquiry, Status: StatusCreated, CreatedAt: base, UpdatedAt: base})
	}
	statusOf := func(account uuid.UUID, inquiry string) Status {
		var status string
		_ = db.WithUserTx(ctx, pool, account.String(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT status FROM app.identity_verifications WHERE persona_inquiry_id = $1`, inquiry).Scan(&status)
		})
		return Status(status)
	}

	if err := create(accountID, inquiryID); err != nil {
		t.Fatal(err)
	}
	if err := create(otherAccountID, "inq_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	t.Run("duplicate inquiry id is rejected", func(t *testing.T) {
		if err := create(accountID, inquiryID); err == nil {
			t.Fatal("expected unique violation")
		}
	})

	t.Run("not completed before any event", func(t *testing.T) {
		completed, err := store.HasCompleted(ctx, accountID)
		if err != nil || completed {
			t.Fatalf("completed = %v err = %v", completed, err)
		}
	})

	t.Run("event applies and duplicate delivery is a no-op", func(t *testing.T) {
		for range 2 {
			if err := store.ApplyStatus(ctx, accountID, inquiryID, StatusNeedsReview, base.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		if got := statusOf(accountID, inquiryID); got != StatusNeedsReview {
			t.Fatalf("status = %s", got)
		}
	})

	t.Run("older event does not overwrite newer", func(t *testing.T) {
		if err := store.ApplyStatus(ctx, accountID, inquiryID, StatusCompleted, base.Add(10*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyStatus(ctx, accountID, inquiryID, StatusNeedsReview, base.Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}
		if got := statusOf(accountID, inquiryID); got != StatusCompleted {
			t.Fatalf("status = %s", got)
		}
		completed, err := store.HasCompleted(ctx, accountID)
		if err != nil || !completed {
			t.Fatalf("completed = %v err = %v", completed, err)
		}
	})

	t.Run("unknown inquiry", func(t *testing.T) {
		if err := store.ApplyStatus(ctx, accountID, "inq_missing", StatusCompleted, base); !errors.Is(err, ErrInquiryNotFound) {
			t.Fatalf("expected ErrInquiryNotFound, got %v", err)
		}
	})

	t.Run("another account cannot update the inquiry", func(t *testing.T) {
		if err := store.ApplyStatus(ctx, otherAccountID, inquiryID, StatusDeclined, base.Add(time.Hour)); !errors.Is(err, ErrInquiryNotFound) {
			t.Fatalf("expected ErrInquiryNotFound, got %v", err)
		}
		if got := statusOf(accountID, inquiryID); got != StatusCompleted {
			t.Fatalf("status = %s", got)
		}
	})

	t.Run("other account is not reported as completed", func(t *testing.T) {
		completed, err := store.HasCompleted(ctx, otherAccountID)
		if err != nil || completed {
			t.Fatalf("completed = %v err = %v", completed, err)
		}
	})
}
