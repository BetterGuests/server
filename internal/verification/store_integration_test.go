package verification

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
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
	accountID, otherAccountID := dbtest.SignedUpAccount(t, ctx, pool), dbtest.SignedUpAccount(t, ctx, pool)
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

func TestRedactionQueriesHonorRetentionHoldsAndCap(t *testing.T) {
	appURL := os.Getenv("TEST_DATABASE_URL")
	migratorURL := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if appURL == "" || migratorURL == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATION_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	migratorPool, err := pgxpool.New(ctx, migratorURL)
	if err != nil {
		t.Fatal(err)
	}
	defer migratorPool.Close()

	store := NewPostgresVerificationStore(appPool)
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	suffix := uuid.NewString()[:8]

	type fixture struct {
		name      string
		inquiry   string
		id        uuid.UUID
		createdAt time.Time
		eventAt   time.Time
		hold      string
		wantDue   bool
	}
	fixtures := []*fixture{
		{name: "old completed", createdAt: now.Add(-40 * day), eventAt: now.Add(-31 * day), wantDue: true},
		{name: "recent completed", createdAt: now.Add(-12 * day), eventAt: now.Add(-10 * day), wantDue: false},
		{name: "old but held", createdAt: now.Add(-40 * day), eventAt: now.Add(-31 * day), hold: "active", wantDue: false},
		{name: "held past the cap", createdAt: now.Add(-1200 * day), eventAt: now.Add(-1100 * day), hold: "active", wantDue: true},
		{name: "old with released hold", createdAt: now.Add(-40 * day), eventAt: now.Add(-31 * day), hold: "released", wantDue: true},
		{name: "never finished", createdAt: now.Add(-45 * day), wantDue: true},
		{name: "just created", createdAt: now, wantDue: false},
	}
	for index, item := range fixtures {
		item.inquiry = "inq_" + suffix + string(rune('a'+index))
		item.id = uuid.New()
		accountID := dbtest.SignedUpAccount(t, ctx, appPool)
		if err := store.Create(ctx, Verification{ID: item.id, AccountID: accountID, PersonaInquiryID: item.inquiry, Status: StatusCreated, CreatedAt: item.createdAt, UpdatedAt: item.createdAt}); err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		if !item.eventAt.IsZero() {
			if err := store.ApplyStatus(ctx, accountID, item.inquiry, StatusCompleted, item.eventAt); err != nil {
				t.Fatalf("%s: %v", item.name, err)
			}
		}
		if item.hold != "" {
			holdID := uuid.New()
			if _, err := migratorPool.Exec(ctx, `INSERT INTO app.identity_verification_holds (id, verification_id, case_reference, approved_by) VALUES ($1, $2, 'case-1', 'counsel')`, holdID, item.id); err != nil {
				t.Fatalf("%s hold: %v", item.name, err)
			}
			if item.hold == "released" {
				if _, err := migratorPool.Exec(ctx, `UPDATE app.identity_verification_holds SET released_at = now(), released_by = 'counsel' WHERE id = $1`, holdID); err != nil {
					t.Fatalf("%s release: %v", item.name, err)
				}
			}
		}
	}

	dueSet := func(retention, cap int) map[string]bool {
		due, err := store.ListDueForRedaction(ctx, retention, cap, 500)
		if err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		for _, item := range due {
			set[item.InquiryID] = true
		}
		return set
	}

	due := dueSet(30, 1095)
	for _, item := range fixtures {
		if due[item.inquiry] != item.wantDue {
			t.Errorf("%s: due = %v, want %v", item.name, due[item.inquiry], item.wantDue)
		}
	}

	hostile := dueSet(-5, -5)
	if hostile[fixtures[6].inquiry] {
		t.Error("a hostile retention value must not make a brand new inquiry due")
	}

	if err := store.MarkRedacted(ctx, fixtures[0].id); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRedacted(ctx, fixtures[0].id); err != nil {
		t.Fatalf("marking twice must be harmless: %v", err)
	}
	if dueSet(30, 1095)[fixtures[0].inquiry] {
		t.Error("a redacted verification must stop being due")
	}
	var redactedAt *time.Time
	if err := migratorPool.QueryRow(ctx, `SELECT redacted_at FROM app.identity_verifications WHERE id = $1`, fixtures[0].id).Scan(&redactedAt); err != nil || redactedAt == nil {
		t.Fatalf("redacted_at = %v err = %v", redactedAt, err)
	}

	var status string
	if err := migratorPool.QueryRow(ctx, `SELECT status FROM app.identity_verifications WHERE id = $1`, fixtures[0].id).Scan(&status); err != nil || status != string(StatusCompleted) {
		t.Fatalf("redaction must keep the pass result: status = %q err = %v", status, err)
	}

	if _, err := appPool.Exec(ctx, `INSERT INTO app.identity_verification_holds (id, verification_id, case_reference, approved_by) VALUES ($1, $2, 'x', 'y')`, uuid.New(), fixtures[1].id); err == nil {
		t.Error("the API role must not be able to place holds")
	}
	if _, err := appPool.Exec(ctx, `UPDATE app.identity_verification_holds SET released_at = now(), released_by = 'x'`); err == nil {
		t.Error("the API role must not be able to release holds")
	}
	var holdRows int
	if err := appPool.QueryRow(ctx, `SELECT count(*) FROM app.identity_verification_holds`).Scan(&holdRows); err == nil {
		t.Errorf("the API role must not read holds directly, got %d rows", holdRows)
	}
	_, _ = appPool.Exec(ctx, `UPDATE app.identity_verifications SET redacted_at = now()`)
	var stillDue bool
	if err := migratorPool.QueryRow(ctx, `SELECT redacted_at IS NULL FROM app.identity_verifications WHERE id = $1`, fixtures[1].id).Scan(&stillDue); err != nil || !stillDue {
		t.Errorf("the API role must not redact rows outside a user context: stillDue = %v err = %v", stillDue, err)
	}
}

func TestRequestDeletionIsScopedHoldAwareAndPickedUpByTheJob(t *testing.T) {
	appURL := os.Getenv("TEST_DATABASE_URL")
	migratorURL := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if appURL == "" || migratorURL == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATION_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	migratorPool, err := pgxpool.New(ctx, migratorURL)
	if err != nil {
		t.Fatal(err)
	}
	defer migratorPool.Close()

	store := NewPostgresVerificationStore(appPool)
	now := time.Now().UTC().Truncate(time.Second)
	suffix := uuid.NewString()[:8]
	accountA, accountB, accountC := uuid.New(), uuid.New(), uuid.New()
	dbtest.SignUp(t, ctx, appPool, accountA, accountB, accountC)

	create := func(account uuid.UUID, label string) (uuid.UUID, string) {
		id, inquiry := uuid.New(), "inq_"+suffix+label
		if err := store.Create(ctx, Verification{ID: id, AccountID: account, PersonaInquiryID: inquiry, Status: StatusCreated, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyStatus(ctx, account, inquiry, StatusCompleted, now); err != nil {
			t.Fatal(err)
		}
		return id, inquiry
	}
	freeID, freeInquiry := create(accountA, "free")
	heldID, heldInquiry := create(accountA, "held")
	otherID, otherInquiry := create(accountB, "other")

	holdID := uuid.New()
	if _, err := migratorPool.Exec(ctx, `INSERT INTO app.identity_verification_holds (id, verification_id, case_reference, approved_by) VALUES ($1, $2, 'case-2', 'counsel')`, holdID, heldID); err != nil {
		t.Fatal(err)
	}

	requestedAt := func(id uuid.UUID) *time.Time {
		var at *time.Time
		if err := migratorPool.QueryRow(ctx, `SELECT deletion_requested_at FROM app.identity_verifications WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	dueSet := func() map[string]bool {
		due, err := store.ListDueForRedaction(ctx, 30, 1095, 500)
		if err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		for _, item := range due {
			set[item.InquiryID] = true
		}
		return set
	}

	if dueSet()[freeInquiry] || dueSet()[otherInquiry] {
		t.Fatal("fresh verifications must not be due before anyone asks")
	}

	got, err := store.RequestDeletion(ctx, accountA)
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, item := range got {
		flags[item.InquiryID] = item.Held
	}
	if len(got) != 2 || flags[freeInquiry] != false || flags[heldInquiry] != true {
		t.Fatalf("requested = %+v", got)
	}
	if _, present := flags[otherInquiry]; present {
		t.Fatal("another account's verification must never be returned")
	}
	if requestedAt(freeID) == nil || requestedAt(heldID) == nil {
		t.Fatal("the account's own verifications must be flagged")
	}
	if requestedAt(otherID) != nil {
		t.Fatal("another account's verification must never be flagged")
	}

	due := dueSet()
	if !due[freeInquiry] {
		t.Error("a requested, unheld verification must be due immediately")
	}
	if due[heldInquiry] {
		t.Error("a held verification must wait even when deletion is requested")
	}
	if due[otherInquiry] {
		t.Error("an unrequested fresh verification must stay out of the job")
	}

	firstRequest := *requestedAt(freeID)
	if _, err := store.RequestDeletion(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	if !requestedAt(freeID).Equal(firstRequest) {
		t.Error("asking twice must keep the original request time")
	}

	if err := store.MarkRedacted(ctx, freeID); err != nil {
		t.Fatal(err)
	}
	again, err := store.RequestDeletion(ctx, accountA)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].InquiryID != heldInquiry || !again[0].Held {
		t.Fatalf("only the held verification should remain: %+v", again)
	}

	if _, err := migratorPool.Exec(ctx, `UPDATE app.identity_verification_holds SET released_at = now(), released_by = 'counsel' WHERE id = $1`, holdID); err != nil {
		t.Fatal(err)
	}
	if !dueSet()[heldInquiry] {
		t.Error("once the hold is released, the requested deletion must go through")
	}

	none, err := store.RequestDeletion(ctx, accountC)
	if err != nil || len(none) != 0 {
		t.Fatalf("an account with no verifications: %+v err = %v", none, err)
	}

	if _, err := appPool.Exec(ctx, `SELECT * FROM app.request_identity_data_deletion()`); err != nil {
		t.Logf("calling without a user context: %v", err)
	}
	if requestedAt(otherID) != nil {
		t.Fatal("calling without a user context must never flag anyone")
	}
}
