package accounts

import (
	"context"
	"fmt"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresAccountStore struct{ DB *pgxpool.Pool }

func NewPostgresAccountStore(pool *pgxpool.Pool) *PostgresAccountStore {
	return &PostgresAccountStore{DB: pool}
}

func (store *PostgresAccountStore) Ensure(ctx context.Context, account Account) error {
	return db.WithUserTx(ctx, store.DB, account.ID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO app.accounts (id, clerk_user_id) VALUES ($1, NULLIF($2, '')) ON CONFLICT (id) DO NOTHING`,
			account.ID,
			account.ClerkUserID,
		); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		return nil
	})
}

func (store *PostgresAccountStore) Remember(ctx context.Context, accountID uuid.UUID, displayName string) error {
	return db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO app.account_names (account_id, display_name) VALUES ($1, $2)
			 ON CONFLICT (account_id) DO UPDATE SET display_name = EXCLUDED.display_name, updated_at = now()`,
			accountID,
			displayName,
		); err != nil {
			return fmt.Errorf("remember account name: %w", err)
		}
		return nil
	})
}

func (store *PostgresAccountStore) ClerkUserID(ctx context.Context, accountID uuid.UUID) (string, error) {
	var clerkUserID string
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(clerk_user_id, '') FROM app.accounts WHERE id = $1`, accountID,
		).Scan(&clerkUserID)
	})
	if err != nil {
		return "", fmt.Errorf("look up clerk user id: %w", err)
	}
	return clerkUserID, nil
}
