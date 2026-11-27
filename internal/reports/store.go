package reports

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

const uniqueViolation = "23505"

const (
	fromPublishedListing = `SELECT l.id, l.title, l.host_id, l.host_name
		FROM app.listings l
		WHERE l.id = $1 AND l.status = 'published' AND $2::uuid IS NOT NULL`
	hostFromBooking = `SELECT l.id, l.title, l.host_id, l.host_name
		FROM app.bookings b JOIN app.listings l ON l.id = b.listing_id
		WHERE b.id = $1 AND b.guest_id = $2`
	hostFromConversation = `SELECT c.listing_id, l.title, c.host_id, l.host_name
		FROM app.conversations c LEFT JOIN app.listings l ON l.id = c.listing_id
		WHERE c.id = $1 AND c.guest_id = $2`
	guestFromBooking = `SELECT l.id, l.title, b.guest_id, n.display_name
		FROM app.bookings b
		JOIN app.listings l ON l.id = b.listing_id
		LEFT JOIN app.account_names n ON n.account_id = b.guest_id
		WHERE b.id = $1 AND l.host_id = $2`
	guestFromConversation = `SELECT c.listing_id, l.title, c.guest_id, n.display_name
		FROM app.conversations c
		LEFT JOIN app.listings l ON l.id = c.listing_id
		LEFT JOIN app.account_names n ON n.account_id = c.guest_id
		WHERE c.id = $1 AND c.host_id = $2`
)

type subject struct {
	listingID    uuid.UUID
	listingTitle string
	accountID    uuid.UUID
	name         string
}

type PostgresReportStore struct {
	DB *pgxpool.Pool
}

func NewPostgresReportStore(pool *pgxpool.Pool) *PostgresReportStore {
	return &PostgresReportStore{DB: pool}
}

func (store *PostgresReportStore) Create(ctx context.Context, reporterID, reportID uuid.UUID, submission Submission, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, reporterID.String(), func(tx pgx.Tx) error {
		reported, err := findSubject(ctx, tx, reporterID, submission)
		if err != nil {
			return err
		}
		if reported.accountID == reporterID {
			return ErrOwnReport
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, reporterID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO app.user_reports
			   (id, reporter_id, subject_id, target, listing_id, booking_id, conversation_id,
			    listing_title, reporter_name, subject_name, reason, details, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, left($8, 200),
			   left(COALESCE((SELECT NULLIF(n.display_name, '') FROM app.account_names n WHERE n.account_id = $2), 'Member'), 200),
			   left($9, 200), $10, $11, $12)`,
			reportID, reporterID, reported.accountID, submission.Target, reported.listingID,
			optionalID(submission.BookingID), optionalID(submission.ConversationID),
			reported.listingTitle, reported.name, submission.Reason, submission.Details, at)
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == uniqueViolation {
			return ErrAlreadyReported
		}
		if err != nil {
			return fmt.Errorf("insert report: %w", err)
		}
		return nil
	})
}

func findSubject(ctx context.Context, tx pgx.Tx, reporterID uuid.UUID, submission Submission) (subject, error) {
	query, contextID, fallbackName := subjectQuery(submission)
	var (
		found       subject
		title, name *string
	)
	err := tx.QueryRow(ctx, query, contextID, reporterID).Scan(&found.listingID, &title, &found.accountID, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return subject{}, ErrTargetNotFound
	}
	if err != nil {
		return subject{}, fmt.Errorf("find reported subject: %w", err)
	}
	if title != nil {
		found.listingTitle = *title
	}
	found.name = fallbackName
	if name != nil && *name != "" {
		found.name = *name
	}
	return found, nil
}

func subjectQuery(submission Submission) (query string, contextID uuid.UUID, fallbackName string) {
	switch {
	case submission.Target == TargetGuest && submission.BookingID != uuid.Nil:
		return guestFromBooking, submission.BookingID, "Guest"
	case submission.Target == TargetGuest:
		return guestFromConversation, submission.ConversationID, "Guest"
	case submission.BookingID != uuid.Nil:
		return hostFromBooking, submission.BookingID, "Host"
	case submission.ConversationID != uuid.Nil:
		return hostFromConversation, submission.ConversationID, "Host"
	default:
		return fromPublishedListing, submission.ListingID, "Host"
	}
}

func optionalID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func (store *PostgresReportStore) List(ctx context.Context, adminID uuid.UUID, open bool, limit int) ([]Report, error) {
	found := []Report{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT r.id, r.target, r.reason, r.details, r.status, r.reporter_id, r.reporter_name,
			        r.subject_id, r.subject_name,
			        (SELECT count(*)::integer FROM app.user_reports other WHERE other.subject_id = r.subject_id),
			        r.listing_id, r.listing_title, r.booking_id, r.conversation_id IS NOT NULL,
			        COALESCE(r.resolution_note, ''), r.created_at, r.resolved_at
			 FROM app.user_reports r
			 WHERE (r.status = 'open') = $1 AND r.subject_id <> $2
			 ORDER BY CASE WHEN $1 THEN r.created_at END ASC, r.resolved_at DESC, r.id
			 LIMIT $3`, open, adminID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var report Report
			if err := rows.Scan(
				&report.ID, &report.Target, &report.Reason, &report.Details, &report.Status,
				&report.ReporterID, &report.ReporterName, &report.SubjectID, &report.SubjectName,
				&report.SubjectReports, &report.ListingID, &report.ListingTitle, &report.BookingID,
				&report.FromConversation, &report.Note, &report.CreatedAt, &report.ResolvedAt,
			); err != nil {
				return err
			}
			found = append(found, report)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("select reports: %w", err)
	}
	return found, nil
}

func (store *PostgresReportStore) Resolve(ctx context.Context, adminID, reportID uuid.UUID, decision Decision, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, adminID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		tag, err := tx.Exec(ctx,
			`UPDATE app.user_reports
			 SET status = $2, resolution_note = NULLIF($3, ''), resolved_by = $4, resolved_at = $5
			 WHERE id = $1 AND status = 'open'`,
			reportID, decision.Outcome, decision.Note, adminID, at)
		if err != nil {
			return fmt.Errorf("update report: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrReportNotFound
		}
		return nil
	})
}
