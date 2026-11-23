package cashback

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const insufficientPrivilegeCode = "42501"

type PostgresCashbackStore struct{ DB *pgxpool.Pool }

func NewPostgresCashbackStore(pool *pgxpool.Pool) *PostgresCashbackStore {
	return &PostgresCashbackStore{DB: pool}
}

func (store *PostgresCashbackStore) BookingState(ctx context.Context, actingID, bookingID uuid.UUID) (BookingState, error) {
	state := BookingState{BookingID: bookingID}
	err := db.WithUserTx(ctx, store.DB, actingID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT b.status, COALESCE(p.stripe_payment_intent_id, ''), COALESCE(p.status, '')
			 FROM app.bookings b
			 LEFT JOIN app.payments p ON p.booking_id = b.id
			 WHERE b.id = $1 AND b.guest_id = $2`,
			bookingID, actingID,
		).Scan(&state.BookingStatus, &state.PaymentIntentID, &state.PaymentStatus)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return BookingState{}, ErrBookingNotFound
	}
	if err != nil {
		return BookingState{}, fmt.Errorf("load booking state: %w", err)
	}
	return state, nil
}

func (store *PostgresCashbackStore) SaveSubmission(ctx context.Context, submission Submission) (Submission, error) {
	var saved Submission
	err := db.WithUserTx(ctx, store.DB, submission.GuestID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO app.booking_task_submissions
			     (id, booking_id, guest_id, task_key, photo_url, status, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, 'checking', $6, $7)
			 ON CONFLICT (booking_id, task_key) DO UPDATE
			 SET photo_url = EXCLUDED.photo_url, status = 'checking', note = NULL,
			     reviewed_at = NULL, updated_at = EXCLUDED.updated_at
			 WHERE app.booking_task_submissions.status = 'retake'
			 RETURNING id, booking_id, guest_id, task_key, photo_url, status, COALESCE(note, ''), reviewed_at, created_at, updated_at`,
			submission.ID, submission.BookingID, submission.GuestID, submission.TaskKey, submission.PhotoURL,
			submission.CreatedAt, submission.UpdatedAt,
		).Scan(scanSubmission(&saved)...)
	})
	var pgError *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgError) && pgError.Code == insufficientPrivilegeCode) {
		return Submission{}, ErrSubmissionLocked
	}
	if err != nil {
		return Submission{}, fmt.Errorf("save submission: %w", err)
	}
	return saved, nil
}

func (store *PostgresCashbackStore) ListSubmissions(ctx context.Context, actingID, bookingID uuid.UUID) ([]Submission, error) {
	var submissions []Submission
	err := db.WithUserTx(ctx, store.DB, actingID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, booking_id, guest_id, task_key, photo_url, status, COALESCE(note, ''), reviewed_at, created_at, updated_at
			 FROM app.booking_task_submissions WHERE booking_id = $1 ORDER BY created_at`,
			bookingID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var submission Submission
			if err := rows.Scan(scanSubmission(&submission)...); err != nil {
				return err
			}
			submissions = append(submissions, submission)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list submissions: %w", err)
	}
	return submissions, nil
}

func (store *PostgresCashbackStore) ListRefunds(ctx context.Context, actingID, bookingID uuid.UUID) ([]Refund, error) {
	var refunds []Refund
	err := db.WithUserTx(ctx, store.DB, actingID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, booking_id, guest_id, bundle_key, amount_cents, status, COALESCE(stripe_refund_id, ''), created_at, updated_at
			 FROM app.booking_bundle_refunds WHERE booking_id = $1 ORDER BY created_at`,
			bookingID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var refund Refund
			if err := rows.Scan(&refund.ID, &refund.BookingID, &refund.GuestID, &refund.BundleKey, &refund.AmountCents,
				&refund.Status, &refund.StripeRefundID, &refund.CreatedAt, &refund.UpdatedAt); err != nil {
				return err
			}
			refunds = append(refunds, refund)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list refunds: %w", err)
	}
	return refunds, nil
}

func (store *PostgresCashbackStore) HostBooking(ctx context.Context, hostID, bookingID uuid.UUID) (HostBooking, error) {
	booking := HostBooking{BookingID: bookingID}
	err := db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT b.guest_id, l.title
			 FROM app.bookings b
			 JOIN app.listings l ON l.id = b.listing_id
			 WHERE b.id = $1 AND l.host_id = $2`,
			bookingID, hostID,
		).Scan(&booking.GuestID, &booking.ListingTitle)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return HostBooking{}, ErrBookingNotFound
	}
	if err != nil {
		return HostBooking{}, fmt.Errorf("load host booking: %w", err)
	}
	return booking, nil
}

func (store *PostgresCashbackStore) ListPendingForHost(ctx context.Context, hostID uuid.UUID) ([]Submission, error) {
	submissions := []Submission{}
	err := db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT s.id, s.booking_id, s.guest_id, s.task_key, s.photo_url, s.status, COALESCE(s.note, ''), s.reviewed_at, s.created_at, s.updated_at
			 FROM app.booking_task_submissions s
			 JOIN app.bookings b ON b.id = s.booking_id
			 JOIN app.listings l ON l.id = b.listing_id
			 WHERE s.status = 'checking' AND l.host_id = $1
			 ORDER BY s.created_at
			 LIMIT 200`,
			hostID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var submission Submission
			if err := rows.Scan(scanSubmission(&submission)...); err != nil {
				return err
			}
			submissions = append(submissions, submission)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list pending for host: %w", err)
	}
	return submissions, nil
}

func (store *PostgresCashbackStore) LoadReviewTarget(ctx context.Context, actingID, submissionID uuid.UUID) (ReviewTarget, error) {
	var target ReviewTarget
	err := db.WithUserTx(ctx, store.DB, actingID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT s.id, s.booking_id, s.guest_id, s.task_key, s.photo_url, s.status, COALESCE(s.note, ''), s.reviewed_at,
			        s.created_at, s.updated_at, l.host_id, COALESCE(p.stripe_payment_intent_id, '')
			 FROM app.booking_task_submissions s
			 JOIN app.bookings b ON b.id = s.booking_id
			 JOIN app.listings l ON l.id = b.listing_id
			 LEFT JOIN app.payments p ON p.booking_id = b.id AND p.status = 'succeeded'
			 WHERE s.id = $1`,
			submissionID,
		).Scan(&target.Submission.ID, &target.Submission.BookingID, &target.Submission.GuestID, &target.Submission.TaskKey,
			&target.Submission.PhotoURL, &target.Submission.Status, &target.Submission.Note, &target.Submission.ReviewedAt,
			&target.Submission.CreatedAt, &target.Submission.UpdatedAt, &target.HostID, &target.PaymentIntentID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewTarget{}, ErrSubmissionNotFound
	}
	if err != nil {
		return ReviewTarget{}, fmt.Errorf("load review target: %w", err)
	}
	return target, nil
}

func (store *PostgresCashbackStore) ApplyReview(ctx context.Context, actingID, submissionID uuid.UUID, status TaskStatus, note string, reviewedAt time.Time) error {
	return db.WithUserTx(ctx, store.DB, actingID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE app.booking_task_submissions
			 SET status = $2, note = NULLIF($3, ''), reviewed_at = $4, updated_at = $4
			 WHERE id = $1 AND status = 'checking'`,
			submissionID, string(status), note, reviewedAt)
		if err != nil {
			return fmt.Errorf("update submission: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotAwaitingReview
		}
		return nil
	})
}

func (store *PostgresCashbackStore) ApprovedTaskKeys(ctx context.Context, actingID, bookingID uuid.UUID) ([]string, error) {
	var keys []string
	err := db.WithUserTx(ctx, store.DB, actingID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT task_key FROM app.booking_task_submissions WHERE booking_id = $1 AND status = 'approved'`, bookingID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("approved task keys: %w", err)
	}
	return keys, nil
}

func (store *PostgresCashbackStore) ReserveRefund(ctx context.Context, actingID uuid.UUID, refund Refund) (Refund, bool, error) {
	var reserved Refund
	created := true
	err := db.WithUserTx(ctx, store.DB, actingID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO app.booking_bundle_refunds (id, booking_id, guest_id, bundle_key, amount_cents, status, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7)
			 ON CONFLICT (booking_id, bundle_key) DO NOTHING`,
			refund.ID, refund.BookingID, refund.GuestID, refund.BundleKey, refund.AmountCents, refund.CreatedAt, refund.UpdatedAt)
		if err != nil {
			return fmt.Errorf("insert refund: %w", err)
		}
		created = tag.RowsAffected() == 1
		return tx.QueryRow(ctx,
			`SELECT id, booking_id, guest_id, bundle_key, amount_cents, status, COALESCE(stripe_refund_id, ''), created_at, updated_at
			 FROM app.booking_bundle_refunds WHERE booking_id = $1 AND bundle_key = $2`,
			refund.BookingID, refund.BundleKey,
		).Scan(&reserved.ID, &reserved.BookingID, &reserved.GuestID, &reserved.BundleKey, &reserved.AmountCents,
			&reserved.Status, &reserved.StripeRefundID, &reserved.CreatedAt, &reserved.UpdatedAt)
	})
	if err != nil {
		return Refund{}, false, err
	}
	return reserved, created, nil
}

func (store *PostgresCashbackStore) SettleRefund(ctx context.Context, actingID, refundID uuid.UUID, status RefundStatus, stripeRefundID string, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, actingID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE app.booking_bundle_refunds
			 SET status = $2, stripe_refund_id = NULLIF($3, ''), updated_at = $4
			 WHERE id = $1 AND status <> 'succeeded'`,
			refundID, string(status), stripeRefundID, at)
		if err != nil {
			return fmt.Errorf("settle refund: %w", err)
		}
		return nil
	})
}

func scanSubmission(submission *Submission) []any {
	return []any{&submission.ID, &submission.BookingID, &submission.GuestID, &submission.TaskKey, &submission.PhotoURL,
		&submission.Status, &submission.Note, &submission.ReviewedAt, &submission.CreatedAt, &submission.UpdatedAt}
}
