package verification

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresVerificationStore struct{ DB *pgxpool.Pool }

func NewPostgresVerificationStore(pool *pgxpool.Pool) *PostgresVerificationStore {
	return &PostgresVerificationStore{DB: pool}
}

func (store *PostgresVerificationStore) HasCompleted(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var completed bool
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.identity_verifications WHERE account_id = $1 AND status = 'completed')`, accountID).Scan(&completed)
	})
	if err != nil {
		return false, fmt.Errorf("check completed verification: %w", err)
	}
	return completed, nil
}

func (store *PostgresVerificationStore) LatestStatus(ctx context.Context, accountID uuid.UUID) (Status, bool, error) {
	var status string
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status FROM app.identity_verifications
			 WHERE account_id = $1
			 ORDER BY (status = 'completed') DESC, created_at DESC
			 LIMIT 1`,
			accountID).Scan(&status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("latest verification status: %w", err)
	}
	return Status(status), true, nil
}

func (store *PostgresVerificationStore) Create(ctx context.Context, verification Verification) error {
	return db.WithUserTx(ctx, store.DB, verification.AccountID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, verification.AccountID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO app.identity_verifications (id, account_id, persona_inquiry_id, status, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			verification.ID, verification.AccountID, verification.PersonaInquiryID, string(verification.Status), verification.CreatedAt, verification.UpdatedAt)
		if err != nil {
			return fmt.Errorf("insert verification: %w", err)
		}
		return nil
	})
}

func (store *PostgresVerificationStore) ApplyStatus(ctx context.Context, accountID uuid.UUID, inquiryID string, status Status, occurredAt time.Time) error {
	return db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE app.identity_verifications SET status = $3, last_event_at = $4, updated_at = now() WHERE account_id = $1 AND persona_inquiry_id = $2 AND (last_event_at IS NULL OR last_event_at <= $4)`,
			accountID, inquiryID, string(status), occurredAt)
		if err != nil {
			return fmt.Errorf("update verification: %w", err)
		}
		if tag.RowsAffected() > 0 {
			return nil
		}
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.identity_verifications WHERE account_id = $1 AND persona_inquiry_id = $2)`, accountID, inquiryID).Scan(&exists)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look up verification: %w", err)
		}
		if !exists {
			return ErrInquiryNotFound
		}
		return nil
	})
}
