package db_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openSignUpTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run account sign-up tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func insertBareAccount(ctx context.Context, pool *pgxpool.Pool, accountID uuid.UUID) error {
	return db.WithUserTx(ctx, pool, accountID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, accountID)
		return err
	})
}

func TestAnAccountWithoutASignUpCannotBeCreated(t *testing.T) {
	ctx, pool := openSignUpTestPool(t)
	err := insertBareAccount(ctx, pool, uuid.New())
	if err == nil || !strings.Contains(err.Error(), "needs a sign-up") {
		t.Fatalf("expected a refusal for an account with no sign-up, got %v", err)
	}
}

func TestEnsuringAnExistingSignedUpAccountStillWorks(t *testing.T) {
	ctx, pool := openSignUpTestPool(t)
	accountID := dbtest.SignedUpAccount(t, ctx, pool)
	if err := insertBareAccount(ctx, pool, accountID); err != nil {
		t.Fatalf("ensuring a signed-up account should be a no-op, got %v", err)
	}
	var clerkUserID string
	err := db.WithUserTx(ctx, pool, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT clerk_user_id FROM app.accounts WHERE id = $1`, accountID).Scan(&clerkUserID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if clerkUserID != "user_test_"+accountID.String() {
		t.Fatalf("sign-up was changed to %q", clerkUserID)
	}
}

func TestTeamAlertsStillWorkForAnAdminGrantedBeforeSigningUp(t *testing.T) {
	ctx, pool := openSignUpTestPool(t)
	migrationURL := os.Getenv("TEST_MIGRATION_DATABASE_URL")
	if migrationURL == "" {
		t.Skip("set TEST_MIGRATION_DATABASE_URL to run the admin grant test")
	}
	migrator, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(migrator.Close)
	adminID := uuid.New()
	if _, err := migrator.Exec(ctx, `INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`,
		adminID, adminID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = migrator.Exec(context.Background(), `DELETE FROM app.admin_accounts WHERE account_id = $1`, adminID)
	})
	if _, err := pool.Exec(ctx, `SELECT count(*) FROM app.queue_team_alerts()`); err != nil {
		t.Fatalf("team alerts broke for an admin with no account yet: %v", err)
	}
}
