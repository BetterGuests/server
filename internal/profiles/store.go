package profiles

import (
	"context"
	"errors"
	"fmt"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresProfileStore struct{ DB *pgxpool.Pool }

func NewPostgresProfileStore(pool *pgxpool.Pool) *PostgresProfileStore {
	return &PostgresProfileStore{DB: pool}
}

func (store *PostgresProfileStore) BannerKey(ctx context.Context, userID uuid.UUID) (string, error) {
	var key string
	err := db.WithUserTx(ctx, store.DB, userID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(banner_key, '') FROM app.accounts WHERE id = $1`, userID).Scan(&key)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read banner key: %w", err)
	}
	return key, nil
}

func (store *PostgresProfileStore) SetBannerKey(ctx context.Context, userID uuid.UUID, key string) error {
	return db.WithUserTx(ctx, store.DB, userID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE app.accounts SET banner_key = NULLIF($2, '') WHERE id = $1`, userID, key); err != nil {
			return fmt.Errorf("set banner key: %w", err)
		}
		return nil
	})
}
