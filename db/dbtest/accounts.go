package dbtest

import (
	"context"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type transactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

func SignedUpAccount(t testing.TB, ctx context.Context, pool transactionBeginner) uuid.UUID {
	t.Helper()
	accountID := uuid.New()
	SignUp(t, ctx, pool, accountID)
	return accountID
}

func SignUp(t testing.TB, ctx context.Context, pool transactionBeginner, accountIDs ...uuid.UUID) {
	t.Helper()
	for _, accountID := range accountIDs {
		err := db.WithUserTx(ctx, pool, accountID.String(), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO app.accounts (id, clerk_user_id) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
				accountID, "user_test_"+accountID.String())
			return err
		})
		if err != nil {
			t.Fatalf("sign up test account %s: %v", accountID, err)
		}
	}
}
