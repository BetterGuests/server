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

func (store *PostgresVerificationStore) LatestCompleted(ctx context.Context, accountID uuid.UUID) (CompletedVerification, bool, error) {
	var completed CompletedVerification
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, COALESCE(last_event_at, updated_at) FROM app.identity_verifications
			 WHERE account_id = $1 AND status = 'completed'
			 ORDER BY COALESCE(last_event_at, updated_at) DESC
			 LIMIT 1`,
			accountID).Scan(&completed.ID, &completed.CompletedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CompletedVerification{}, false, nil
	}
	if err != nil {
		return CompletedVerification{}, false, fmt.Errorf("latest completed verification: %w", err)
	}
	return completed, true, nil
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

func (store *PostgresVerificationStore) SyncIDName(ctx context.Context, accountID uuid.UUID, inquiryID string, name IDName) error {
	return db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM app.identity_names n USING app.identity_verifications v
			 WHERE n.verification_id = v.id AND v.account_id = $1 AND v.persona_inquiry_id = $2 AND v.status <> 'completed'`,
			accountID, inquiryID)
		if err != nil {
			return fmt.Errorf("remove ID name: %w", err)
		}
		if name.First == "" && name.Last == "" {
			return nil
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO app.identity_names (verification_id, account_id, first_name, middle_name, last_name, suffix, verified_at)
			 SELECT v.id, v.account_id, $3, $4, $5, $6, COALESCE(v.last_event_at, v.updated_at)
			 FROM app.identity_verifications v
			 WHERE v.account_id = $1 AND v.persona_inquiry_id = $2 AND v.status = 'completed' AND v.deletion_requested_at IS NULL AND v.redacted_at IS NULL
			 ON CONFLICT (verification_id) DO UPDATE
			 SET first_name = EXCLUDED.first_name, middle_name = EXCLUDED.middle_name, last_name = EXCLUDED.last_name,
			     suffix = EXCLUDED.suffix, verified_at = EXCLUDED.verified_at, updated_at = now()`,
			accountID, inquiryID, name.First, name.Middle, name.Last, name.Suffix)
		if err != nil {
			return fmt.Errorf("save ID name: %w", err)
		}
		return nil
	})
}

func (store *PostgresVerificationStore) ListDueForRedaction(ctx context.Context, retentionDays, holdCapDays, batchSize int) ([]DueVerification, error) {
	rows, err := store.DB.Query(ctx,
		`SELECT id, account_id, persona_inquiry_id FROM app.identity_verifications_due_for_redaction($1, $2, $3)`,
		retentionDays, holdCapDays, batchSize)
	if err != nil {
		return nil, fmt.Errorf("list verifications due for redaction: %w", err)
	}
	defer rows.Close()
	var due []DueVerification
	for rows.Next() {
		var item DueVerification
		if err := rows.Scan(&item.ID, &item.AccountID, &item.InquiryID); err != nil {
			return nil, fmt.Errorf("scan verification due for redaction: %w", err)
		}
		due = append(due, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read verifications due for redaction: %w", err)
	}
	return due, nil
}

func (store *PostgresVerificationStore) MarkRedacted(ctx context.Context, id uuid.UUID) error {
	if _, err := store.DB.Exec(ctx, `SELECT app.mark_identity_verification_redacted($1)`, id); err != nil {
		return fmt.Errorf("mark verification redacted: %w", err)
	}
	return nil
}

func (store *PostgresVerificationStore) RequestDeletion(ctx context.Context, accountID uuid.UUID) ([]RequestedDeletion, error) {
	var requested []RequestedDeletion
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, persona_inquiry_id, held FROM app.request_identity_data_deletion()`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item RequestedDeletion
			if err := rows.Scan(&item.ID, &item.InquiryID, &item.Held); err != nil {
				return err
			}
			requested = append(requested, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("request ID deletion: %w", err)
	}
	return requested, nil
}
