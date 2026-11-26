package reviews

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

const stayReviewColumns = `r.id, r.booking_id, r.listing_id, 'guest', COALESCE(r.author_name, ''), r.rating, r.comment,
	r.cleanliness, r.accuracy, r.check_in, r.communication, r.location, r.value, r.created_at, r.published_at`

const guestReviewColumns = `g.id, g.booking_id, g.listing_id, 'host', COALESCE(g.author_name, ''), g.rating, g.comment,
	NULL::integer, NULL::integer, NULL::integer, NULL::integer, NULL::integer, NULL::integer, g.created_at, g.published_at`

type PostgresReviewStore struct{ DB *pgxpool.Pool }

func NewPostgresReviewStore(pool *pgxpool.Pool) *PostgresReviewStore {
	return &PostgresReviewStore{DB: pool}
}

func (store *PostgresReviewStore) Booking(ctx context.Context, actorID, bookingID uuid.UUID) (Booking, error) {
	var booking Booking
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT b.id, b.listing_id, l.title, b.guest_id, l.host_id,
			        COALESCE(n.display_name, ''), COALESCE(l.host_name, ''), b.status, b.completed_at
			 FROM app.bookings b
			 JOIN app.listings l ON l.id = b.listing_id
			 LEFT JOIN app.account_names n ON n.account_id = b.guest_id
			 WHERE b.id = $1 AND (b.guest_id = $2 OR l.host_id = $2)`,
			bookingID, actorID,
		).Scan(
			&booking.BookingID, &booking.ListingID, &booking.ListingTitle, &booking.GuestID, &booking.HostID,
			&booking.GuestName, &booking.HostName, &booking.Status, &booking.CompletedAt,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, ErrBookingNotFound
	}
	if err != nil {
		return Booking{}, fmt.Errorf("load booking: %w", err)
	}
	return booking, nil
}

func (store *PostgresReviewStore) Insert(ctx context.Context, actorID uuid.UUID, review NewReview) error {
	return db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, actorID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		var err error
		if review.AuthorRole == RoleHost {
			_, err = tx.Exec(ctx,
				`INSERT INTO app.guest_reviews (id, booking_id, listing_id, host_id, guest_id, rating, comment, author_name, created_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				review.ID, review.BookingID, review.ListingID, review.HostID, review.GuestID,
				review.Rating, review.Comment, review.AuthorName, review.CreatedAt)
		} else {
			scores := Categories{}
			if review.Categories != nil {
				scores = *review.Categories
			}
			_, err = tx.Exec(ctx,
				`INSERT INTO app.listing_reviews
				     (id, listing_id, guest_id, booking_id, rating, comment, author_name, created_at, updated_at,
				      cleanliness, accuracy, check_in, communication, location, value)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9, $10, $11, $12, $13, $14)`,
				review.ID, review.ListingID, review.GuestID, review.BookingID, review.Rating, review.Comment,
				review.AuthorName, review.CreatedAt,
				scores.Cleanliness, scores.Accuracy, scores.CheckIn, scores.Communication, scores.Location, scores.Value)
		}
		var pgError *pgconn.PgError
		switch {
		case errors.As(err, &pgError) && pgError.Code == uniqueViolationCode:
			return ErrReviewAlreadyExists
		case err != nil:
			return fmt.Errorf("insert review: %w", err)
		}
		return nil
	})
}

func (store *PostgresReviewStore) ForBooking(ctx context.Context, actorID, bookingID uuid.UUID) ([]Review, error) {
	var found []Review
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		var err error
		found, err = queryReviews(ctx, tx,
			`SELECT `+stayReviewColumns+` FROM app.listing_reviews r WHERE r.booking_id = $1
			 UNION ALL
			 SELECT `+guestReviewColumns+` FROM app.guest_reviews g WHERE g.booking_id = $1`,
			bookingID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list booking reviews: %w", err)
	}
	return found, nil
}

func (store *PostgresReviewStore) ForListing(ctx context.Context, listingID uuid.UUID, limit int) (ListingReviews, error) {
	var result ListingReviews
	err := db.WithPublicTx(ctx, store.DB, func(tx pgx.Tx) error {
		averages := &result.Categories
		if err := tx.QueryRow(ctx,
			`SELECT count(*)::integer, COALESCE(round(avg(rating), 2), 0)::float8,
			        round(avg(cleanliness), 2)::float8, round(avg(accuracy), 2)::float8,
			        round(avg(check_in), 2)::float8, round(avg(communication), 2)::float8,
			        round(avg(location), 2)::float8, round(avg(value), 2)::float8
			 FROM app.listing_reviews WHERE listing_id = $1 AND published_at IS NOT NULL`,
			listingID,
		).Scan(
			&result.Count, &result.Average, &averages.Cleanliness, &averages.Accuracy,
			&averages.CheckIn, &averages.Communication, &averages.Location, &averages.Value,
		); err != nil {
			return fmt.Errorf("summarize reviews: %w", err)
		}
		var err error
		result.Reviews, err = queryReviews(ctx, tx,
			`SELECT `+stayReviewColumns+` FROM app.listing_reviews r
			 WHERE r.listing_id = $1 AND r.published_at IS NOT NULL
			 ORDER BY r.created_at DESC, r.id
			 LIMIT $2`, listingID, limit)
		return err
	})
	if err != nil {
		return ListingReviews{}, fmt.Errorf("list listing reviews: %w", err)
	}
	return result, nil
}

func (store *PostgresReviewStore) AboutGuest(ctx context.Context, actorID, guestID uuid.UUID, limit int) (GuestReviews, error) {
	var result GuestReviews
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*)::integer, COALESCE(round(avg(rating), 2), 0)::float8
			 FROM app.guest_reviews WHERE guest_id = $1 AND published_at IS NOT NULL`,
			guestID,
		).Scan(&result.Count, &result.Average); err != nil {
			return fmt.Errorf("summarize guest reviews: %w", err)
		}
		var err error
		result.Reviews, err = queryReviews(ctx, tx,
			`SELECT `+guestReviewColumns+` FROM app.guest_reviews g
			 WHERE g.guest_id = $1 AND g.published_at IS NOT NULL
			 ORDER BY g.created_at DESC, g.id
			 LIMIT $2`, guestID, limit)
		return err
	})
	if err != nil {
		return GuestReviews{}, fmt.Errorf("list guest reviews: %w", err)
	}
	return result, nil
}

func (store *PostgresReviewStore) Pending(ctx context.Context, actorID uuid.UUID, completedAfter time.Time, limit int) ([]PendingReview, error) {
	pending := []PendingReview{}
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT b.id, l.title,
			        CASE WHEN l.host_id = $1 THEN 'host' ELSE 'guest' END,
			        CASE WHEN l.host_id = $1 THEN COALESCE(n.display_name, $4) ELSE COALESCE(l.host_name, $5) END,
			        b.completed_at
			 FROM app.bookings b
			 JOIN app.listings l ON l.id = b.listing_id
			 LEFT JOIN app.account_names n ON n.account_id = b.guest_id
			 WHERE b.status = 'completed' AND b.completed_at > $2
			   AND (
			       (b.guest_id = $1 AND NOT EXISTS (SELECT 1 FROM app.listing_reviews r WHERE r.booking_id = b.id))
			       OR (l.host_id = $1 AND NOT EXISTS (SELECT 1 FROM app.guest_reviews g WHERE g.booking_id = b.id))
			   )
			 ORDER BY b.completed_at, b.id
			 LIMIT $3`,
			actorID, completedAfter, limit, missingGuestName, missingHostName)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item PendingReview
			var completedAt time.Time
			if err := rows.Scan(&item.BookingID, &item.ListingTitle, &item.Role, &item.SubjectName, &completedAt); err != nil {
				return err
			}
			item.WindowClosesAt = completedAt.Add(ReviewWindow)
			pending = append(pending, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list pending reviews: %w", err)
	}
	return pending, nil
}

func (store *PostgresReviewStore) PublishDue(ctx context.Context) (int, error) {
	var published int
	if err := store.DB.QueryRow(ctx, `SELECT count(*) FROM app.publish_due_reviews()`).Scan(&published); err != nil {
		return 0, fmt.Errorf("publish reviews: %w", err)
	}
	return published, nil
}

var summarySources = map[SummarySubject]struct{ reviews, cache string }{
	SummaryOfListing: {
		reviews: `SELECT md5(coalesce(string_agg(r.id::text, ',' ORDER BY r.id::text), '')), count(*)::integer
		          FROM app.listing_reviews r WHERE r.listing_id = $1 AND r.published_at IS NOT NULL`,
		cache: `SELECT fingerprint, review_count, summary, updated_at, checked_at FROM app.review_summaries WHERE listing_id = $1`,
	},
	SummaryOfGuest: {
		reviews: `SELECT md5(coalesce(string_agg(g.id::text, ',' ORDER BY g.id::text), '')), count(*)::integer
		          FROM app.guest_reviews g WHERE g.guest_id = $1 AND g.published_at IS NOT NULL`,
		cache: `SELECT fingerprint, review_count, summary, updated_at, checked_at FROM app.review_summaries WHERE guest_id = $1`,
	},
}

func (store *PostgresReviewStore) SummaryState(ctx context.Context, actorID uuid.UUID, subject SummarySubject, subjectID uuid.UUID) (SummaryState, error) {
	source, known := summarySources[subject]
	if !known {
		return SummaryState{}, fmt.Errorf("unknown summary subject %q", subject)
	}
	var state SummaryState
	read := func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, source.reviews, subjectID).Scan(&state.Fingerprint, &state.ReviewCount); err != nil {
			return fmt.Errorf("fingerprint reviews: %w", err)
		}
		var cached CachedSummary
		err := tx.QueryRow(ctx, source.cache, subjectID).Scan(&cached.Fingerprint, &cached.ReviewCount, &cached.Summary, &cached.UpdatedAt, &cached.CheckedAt)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("load cached summary: %w", err)
		}
		state.Cached = &cached
		return nil
	}
	var err error
	if subject == SummaryOfListing {
		err = db.WithPublicTx(ctx, store.DB, read)
	} else {
		err = db.WithUserTx(ctx, store.DB, actorID.String(), read)
	}
	if err != nil {
		return SummaryState{}, fmt.Errorf("review summary state: %w", err)
	}
	return state, nil
}

func (store *PostgresReviewStore) SaveSummary(ctx context.Context, actorID uuid.UUID, subject SummarySubject, subjectID uuid.UUID, summary CachedSummary) error {
	const save = `SELECT app.save_review_summary($1, $2, $3, $4, $5)`
	args := []any{string(subject), subjectID, summary.Fingerprint, summary.ReviewCount, summary.Summary}
	var saved bool
	var err error
	if subject == SummaryOfListing {
		err = store.DB.QueryRow(ctx, save, args...).Scan(&saved)
	} else {
		err = db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, save, args...).Scan(&saved)
		})
	}
	if err != nil {
		return fmt.Errorf("save review summary: %w", err)
	}
	return nil
}

func queryReviews(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]Review, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []Review{}
	for rows.Next() {
		var review Review
		var scores Categories
		if err := rows.Scan(
			&review.ID, &review.BookingID, &review.ListingID, &review.AuthorRole, &review.AuthorName,
			&review.Rating, &review.Comment,
			&scores.Cleanliness, &scores.Accuracy, &scores.CheckIn, &scores.Communication, &scores.Location, &scores.Value,
			&review.CreatedAt, &review.PublishedAt,
		); err != nil {
			return nil, err
		}
		if scores != (Categories{}) {
			review.Categories = &scores
		}
		found = append(found, review)
	}
	return found, rows.Err()
}
