package verification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const passedVerification = `status = 'completed' AND revoked_at IS NULL AND (review_state IS NULL OR review_state = 'cleared')`

type PostgresVerificationStore struct{ DB *pgxpool.Pool }

func NewPostgresVerificationStore(pool *pgxpool.Pool) *PostgresVerificationStore {
	return &PostgresVerificationStore{DB: pool}
}

func (store *PostgresVerificationStore) HasCompleted(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var completed bool
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.identity_verifications WHERE account_id = $1 AND `+passedVerification+`)`, accountID).Scan(&completed)
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
			 WHERE account_id = $1 AND `+passedVerification+`
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

func (store *PostgresVerificationStore) Attempts(ctx context.Context, accountID uuid.UUID) ([]Attempt, error) {
	var attempts []Attempt
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT persona_inquiry_id, status, COALESCE(review_state, ''), revoked_at IS NOT NULL,
			        COALESCE(last_event_at, updated_at)
			 FROM app.identity_verifications
			 WHERE account_id = $1
			 ORDER BY created_at DESC, id`,
			accountID)
		if err != nil {
			return err
		}
		attempts, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Attempt, error) {
			var attempt Attempt
			err := row.Scan(&attempt.InquiryID, &attempt.Status, &attempt.Review, &attempt.Revoked, &attempt.CompletedAt)
			return attempt, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list verification attempts: %w", err)
	}
	return attempts, nil
}

func (store *PostgresVerificationStore) LatestOpenInquiry(ctx context.Context, accountID uuid.UUID, since time.Time) (string, bool, error) {
	var inquiryID string
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT persona_inquiry_id FROM app.identity_verifications
			 WHERE account_id = $1 AND status = 'created' AND created_at >= $2
			   AND deletion_requested_at IS NULL AND redacted_at IS NULL
			 ORDER BY created_at DESC, id
			 LIMIT 1`,
			accountID, since).Scan(&inquiryID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("latest open verification: %w", err)
	}
	return inquiryID, true, nil
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
		current, err := lockVerification(ctx, tx, accountID, inquiryID)
		if err != nil || current.supersedes(occurredAt) {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE app.identity_verifications
			 SET status = $2, last_event_at = $3, updated_at = now(),
			     revoked_at = CASE WHEN $2 = 'completed' AND status <> 'completed' AND deletion_requested_at IS NULL THEN NULL ELSE revoked_at END
			 WHERE id = $1`,
			current.id, string(status), occurredAt); err != nil {
			return fmt.Errorf("update verification: %w", err)
		}
		if status != StatusDeclined {
			return nil
		}
		return revokeEarlierPasses(ctx, tx, accountID, current, occurredAt)
	})
}

type lockedVerification struct {
	id          uuid.UUID
	status      Status
	review      ReviewState
	lastEventAt *time.Time
}

func (current lockedVerification) supersedes(occurredAt time.Time) bool {
	return current.lastEventAt != nil && current.lastEventAt.After(occurredAt)
}

func lockVerification(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, inquiryID string) (lockedVerification, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "identity:"+accountID.String()); err != nil {
		return lockedVerification{}, fmt.Errorf("lock account verifications: %w", err)
	}
	var current lockedVerification
	err := tx.QueryRow(ctx,
		`SELECT id, status, COALESCE(review_state, ''), last_event_at FROM app.identity_verifications
		 WHERE account_id = $1 AND persona_inquiry_id = $2
		 FOR UPDATE`,
		accountID, inquiryID).Scan(&current.id, &current.status, &current.review, &current.lastEventAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedVerification{}, ErrInquiryNotFound
	}
	if err != nil {
		return lockedVerification{}, fmt.Errorf("look up verification: %w", err)
	}
	return current, nil
}

func revokeEarlierPasses(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, declined lockedVerification, occurredAt time.Time) error {
	if _, err := tx.Exec(ctx,
		`UPDATE app.identity_verifications SET revoked_at = $3, updated_at = now()
		 WHERE account_id = $1 AND revoked_at IS NULL
		   AND ((status = 'completed' AND COALESCE(last_event_at, updated_at) <= $3) OR (id = $2 AND $4))`,
		accountID, declined.id, occurredAt, declined.status == StatusCompleted); err != nil {
		return fmt.Errorf("revoke earlier verifications: %w", err)
	}
	return nil
}

func (store *PostgresVerificationStore) Complete(ctx context.Context, completion Completion, judge Judge) ([]ReviewReason, error) {
	var held []ReviewReason
	err := db.WithUserTx(ctx, store.DB, completion.AccountID.String(), func(tx pgx.Tx) error {
		current, err := lockVerification(ctx, tx, completion.AccountID, completion.InquiryID)
		if err != nil {
			return err
		}
		if !current.supersedes(completion.OccurredAt) {
			firstPass := current.status != StatusCompleted
			if firstPass && current.review == "" {
				history, err := readHistory(ctx, tx, completion, current.id)
				if err != nil {
					return err
				}
				held = judge(history)
			}
			if err := recordPass(ctx, tx, current.id, completion.OccurredAt, firstPass, held); err != nil {
				return err
			}
			if len(held) > 0 {
				settled, err := settleReview(ctx, tx, current.id, completion.OccurredAt)
				if err != nil {
					return err
				}
				if settled != outcomeHeld {
					slog.Info("ID check settled without a person", "inquiry_id", completion.InquiryID, "reasons", held, "outcome", settled)
					held = nil
				}
			}
		}
		if err := saveIDName(ctx, tx, completion.AccountID, completion.InquiryID, completion.Name); err != nil {
			return err
		}
		return saveFingerprint(ctx, tx, completion)
	})
	if err != nil {
		return nil, err
	}
	return held, nil
}

func readHistory(ctx context.Context, tx pgx.Tx, completion Completion, verificationID uuid.UUID) (History, error) {
	var history History
	err := tx.QueryRow(ctx,
		`SELECT
		     EXISTS (SELECT 1 FROM app.identity_verifications WHERE account_id = $1 AND id <> $2 AND `+passedVerification+`),
		     EXISTS (SELECT 1 FROM app.property_proofs WHERE host_id = $1 AND status = 'approved'),
		     EXISTS (SELECT 1 FROM app.identity_verifications WHERE account_id = $1 AND id <> $2 AND status = 'declined')`,
		completion.AccountID, verificationID).Scan(&history.HasEarlierPass, &history.HasApprovedProof, &history.HasDecline)
	if err != nil {
		return History{}, fmt.Errorf("read verification history: %w", err)
	}
	err = tx.QueryRow(ctx,
		`SELECT n.first_name, n.middle_name, n.last_name, n.suffix
		 FROM app.identity_names n
		 JOIN app.identity_verifications v ON v.id = n.verification_id
		 WHERE v.account_id = $1 AND v.id <> $2 AND v.status = 'completed' AND v.revoked_at IS NULL
		   AND (v.review_state IS NULL OR v.review_state = 'cleared')
		 ORDER BY n.verified_at DESC, n.verification_id
		 LIMIT 1`,
		completion.AccountID, verificationID).Scan(
		&history.PreviousName.First, &history.PreviousName.Middle, &history.PreviousName.Last, &history.PreviousName.Suffix)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return History{}, fmt.Errorf("read previous ID name: %w", err)
	}
	if len(completion.Fingerprint) == 0 {
		return history, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(encode($1::bytea, 'hex'), 0))`, completion.Fingerprint); err != nil {
		return History{}, fmt.Errorf("lock identity fingerprint: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT app.identity_fingerprint_taken($1)`, completion.Fingerprint).Scan(&history.DuplicateIdentity); err != nil {
		return History{}, fmt.Errorf("check for a duplicate identity: %w", err)
	}
	return history, nil
}

const outcomeHeld = "held"

func settleReview(ctx context.Context, tx pgx.Tx, verificationID uuid.UUID, at time.Time) (string, error) {
	var outcome string
	if err := tx.QueryRow(ctx, `SELECT app.settle_identity_review($1, $2)`, verificationID, at).Scan(&outcome); err != nil {
		return "", fmt.Errorf("settle ID review: %w", err)
	}
	return outcome, nil
}

func recordPass(ctx context.Context, tx pgx.Tx, verificationID uuid.UUID, occurredAt time.Time, firstPass bool, held []ReviewReason) error {
	reasons := make([]string, len(held))
	for index, reason := range held {
		reasons[index] = string(reason)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE app.identity_verifications
		 SET status = 'completed', last_event_at = $2, updated_at = now(),
		     revoked_at = CASE WHEN $3 AND deletion_requested_at IS NULL THEN NULL ELSE revoked_at END,
		     review_state = CASE WHEN cardinality($4::text[]) > 0 THEN 'pending' ELSE review_state END,
		     review_reasons = CASE WHEN cardinality($4::text[]) > 0 THEN $4::text[] ELSE review_reasons END
		 WHERE id = $1`,
		verificationID, occurredAt, firstPass, reasons); err != nil {
		return fmt.Errorf("record passed verification: %w", err)
	}
	return nil
}

func saveFingerprint(ctx context.Context, tx pgx.Tx, completion Completion) error {
	if len(completion.Fingerprint) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app.identity_fingerprints (verification_id, account_id, fingerprint)
		 SELECT v.id, v.account_id, $3
		 FROM app.identity_verifications v
		 WHERE v.account_id = $1 AND v.persona_inquiry_id = $2 AND v.status = 'completed'
		   AND v.deletion_requested_at IS NULL AND v.redacted_at IS NULL
		 ON CONFLICT (verification_id) DO NOTHING`,
		completion.AccountID, completion.InquiryID, completion.Fingerprint); err != nil {
		return fmt.Errorf("save identity fingerprint: %w", err)
	}
	return nil
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
		return saveIDName(ctx, tx, accountID, inquiryID, name)
	})
}

func saveIDName(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, inquiryID string, name IDName) error {
	if !named(name) {
		return nil
	}
	_, err := tx.Exec(ctx,
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

func (store *PostgresVerificationStore) ApplyVerificationPauses(ctx context.Context, validDays, graceDays int) error {
	if _, err := store.DB.Exec(ctx, `SELECT app.apply_verification_pauses($1, $2)`, validDays, graceDays); err != nil {
		return fmt.Errorf("apply verification pauses: %w", err)
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
