package compliance

import (
	"context"
	"fmt"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresComplianceStore struct{ DB *pgxpool.Pool }

func NewPostgresComplianceStore(pool *pgxpool.Pool) *PostgresComplianceStore {
	return &PostgresComplianceStore{DB: pool}
}

func (store *PostgresComplianceStore) SaveAge(ctx context.Context, accountID uuid.UUID, dateOfBirth time.Time) error {
	return db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		if err := ensureAccount(ctx, tx, accountID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.age_confirmations (account_id, date_of_birth) VALUES ($1, $2) ON CONFLICT (account_id) DO NOTHING`,
			accountID, dateOfBirth); err != nil {
			return fmt.Errorf("insert age confirmation: %w", err)
		}
		return nil
	})
}

func (store *PostgresComplianceStore) HasAge(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var confirmed bool
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM app.age_confirmations WHERE account_id = $1)`,
			accountID).Scan(&confirmed)
	})
	if err != nil {
		return false, fmt.Errorf("select age confirmation: %w", err)
	}
	return confirmed, nil
}

func (store *PostgresComplianceStore) SaveConsent(ctx context.Context, consent Consent) error {
	return db.WithUserTx(ctx, store.DB, consent.AccountID.String(), func(tx pgx.Tx) error {
		if err := ensureAccount(ctx, tx, consent.AccountID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.consents (id, account_id, kind, policy_version, granted_at)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (account_id, kind, policy_version) DO UPDATE SET withdrawn_at = NULL`,
			consent.ID, consent.AccountID, consent.Kind, consent.PolicyVersion, consent.GrantedAt); err != nil {
			return fmt.Errorf("insert consent: %w", err)
		}
		return nil
	})
}

func (store *PostgresComplianceStore) HasConsent(ctx context.Context, accountID uuid.UUID, kind string) (bool, error) {
	var granted bool
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM app.consents WHERE account_id = $1 AND kind = $2 AND withdrawn_at IS NULL)`,
			accountID, kind).Scan(&granted)
	})
	if err != nil {
		return false, fmt.Errorf("select consent: %w", err)
	}
	return granted, nil
}

func ensureAccount(ctx context.Context, tx pgx.Tx, accountID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, accountID); err != nil {
		return fmt.Errorf("ensure account: %w", err)
	}
	return nil
}
