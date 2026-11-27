package cancellations

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

type PostgresCancellationStore struct{ DB *pgxpool.Pool }

func NewPostgresCancellationStore(pool *pgxpool.Pool) *PostgresCancellationStore {
	return &PostgresCancellationStore{DB: pool}
}

const reportColumns = `booking_id, guest_id, host_id, listing_title, details, amount_cents, payment_intent_id,
	status, refund_cents, refund_status, COALESCE(resolution_note, ''), created_at`

func scanReport(row pgx.Row) (Report, error) {
	var report Report
	err := row.Scan(
		&report.BookingID, &report.GuestID, &report.HostID, &report.ListingTitle, &report.Details,
		&report.AmountCents, &report.PaymentIntentID, &report.Status, &report.RefundCents,
		&report.RefundStatus, &report.Note, &report.CreatedAt,
	)
	return report, err
}

func (store *PostgresCancellationStore) Booking(ctx context.Context, actorID, bookingID uuid.UUID) (Booking, error) {
	var booking Booking
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT b.id, b.listing_id, l.title, b.guest_id, l.host_id, b.status, b.starts_on,
			        to_char(l.check_in_time, 'HH24:MI'), l.time_zone, b.decided_at,
			        COALESCE(p.amount_cents, 0), COALESCE(p.guest_fee_cents, 0), COALESCE(p.currency, 'usd'), COALESCE(p.status, ''),
			        COALESCE(p.stripe_payment_intent_id, '')
			 FROM app.bookings b
			 JOIN app.listings l ON l.id = b.listing_id
			 LEFT JOIN app.payments p ON p.booking_id = b.id
			 WHERE b.id = $1 AND (b.guest_id = $2 OR l.host_id = $2)`,
			bookingID, actorID,
		).Scan(
			&booking.BookingID, &booking.ListingID, &booking.ListingTitle, &booking.GuestID, &booking.HostID,
			&booking.Status, &booking.StartsOn, &booking.CheckInTime, &booking.TimeZone, &booking.ApprovedAt,
			&booking.AmountCents, &booking.GuestFeeCents, &booking.Currency, &booking.PaymentStatus, &booking.PaymentIntentID,
		); err != nil {
			return err
		}
		var cancellation Cancellation
		err := tx.QueryRow(ctx,
			`SELECT cancelled_by, refund_cents, refund_status FROM app.booking_cancellations WHERE booking_id = $1`,
			bookingID,
		).Scan(&cancellation.CancelledBy, &cancellation.RefundCents, &cancellation.RefundStatus)
		switch {
		case err == nil:
			booking.Cancellation = &cancellation
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("select cancellation: %w", err)
		}
		report, err := scanReport(tx.QueryRow(ctx, `SELECT `+reportColumns+` FROM app.stay_reports WHERE booking_id = $1`, bookingID))
		switch {
		case err == nil:
			booking.Report = &report
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("select report: %w", err)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, ErrBookingNotFound
	}
	if err != nil {
		return Booking{}, fmt.Errorf("load booking: %w", err)
	}
	return booking, nil
}

func (store *PostgresCancellationStore) Cancel(ctx context.Context, actorID uuid.UUID, cancellation Cancellation, hostCancellationsSince time.Time) error {
	return db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		if err := insertCancellation(ctx, tx, cancellation); err != nil {
			return err
		}
		if err := markCancelled(ctx, tx, cancellation.BookingID, statusConfirmed); err != nil {
			return err
		}
		if cancellation.CancelledBy != ByHost {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.listing_suspensions (listing_id, host_id, reason)
			 SELECT $1, $2, $3
			 WHERE (
			     SELECT count(*) FROM app.booking_cancellations c
			     WHERE c.listing_id = $1 AND c.cancelled_by = 'host' AND c.created_at > GREATEST(
			         $4::timestamptz,
			         COALESCE((SELECT s.lifted_at FROM app.listing_suspensions s WHERE s.listing_id = $1 AND s.lifted_by IS NOT NULL), $4::timestamptz))
			 ) >= $5
			 ON CONFLICT (listing_id) DO UPDATE
			 SET reason = EXCLUDED.reason, paused_by = 'host_cancellations', created_at = now(),
			     lifted_at = NULL, lifted_by = NULL
			 WHERE app.listing_suspensions.lifted_at IS NOT NULL OR app.listing_suspensions.paused_by = 'verification'`,
			cancellation.ListingID, cancellation.HostID, suspensionReason, hostCancellationsSince, HostCancellationLimit,
		); err != nil {
			return fmt.Errorf("pause listing: %w", err)
		}
		return nil
	})
}

func (store *PostgresCancellationStore) ExpiredRequests(ctx context.Context, limit int) ([]ExpiredRequest, error) {
	rows, err := store.DB.Query(ctx, `SELECT booking_id, guest_id FROM app.expired_booking_requests($1)`, limit)
	if err != nil {
		return nil, fmt.Errorf("query expired requests: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ExpiredRequest, error) {
		var request ExpiredRequest
		err := row.Scan(&request.BookingID, &request.GuestID)
		return request, err
	})
}

func (store *PostgresCancellationStore) Expire(ctx context.Context, request ExpiredRequest) error {
	return db.WithUserTx(ctx, store.DB, request.GuestID.String(), func(tx pgx.Tx) error {
		cancellation := Cancellation{BookingID: request.BookingID, GuestID: request.GuestID, CancelledBy: ByExpired, RefundStatus: RefundNone}
		if err := tx.QueryRow(ctx,
			`SELECT b.listing_id, l.host_id FROM app.bookings b JOIN app.listings l ON l.id = b.listing_id
			 WHERE b.id = $1 AND b.guest_id = $2`,
			request.BookingID, request.GuestID,
		).Scan(&cancellation.ListingID, &cancellation.HostID); err != nil {
			return fmt.Errorf("select expiring booking: %w", err)
		}
		if err := insertCancellation(ctx, tx, cancellation); err != nil {
			return err
		}
		return markCancelled(ctx, tx, request.BookingID, "requested")
	})
}

func (store *PostgresCancellationStore) StrandedHolds(ctx context.Context, limit int) ([]StrandedHold, error) {
	rows, err := store.DB.Query(ctx, `SELECT booking_id, guest_id FROM app.stranded_booking_holds($1)`, limit)
	if err != nil {
		return nil, fmt.Errorf("query stranded holds: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (StrandedHold, error) {
		var hold StrandedHold
		err := row.Scan(&hold.BookingID, &hold.GuestID)
		return hold, err
	})
}

func insertCancellation(ctx context.Context, tx pgx.Tx, cancellation Cancellation) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO app.booking_cancellations
		     (booking_id, guest_id, host_id, listing_id, cancelled_by, amount_cents, refund_cents, payment_intent_id, refund_status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9)`,
		cancellation.BookingID, cancellation.GuestID, cancellation.HostID, cancellation.ListingID,
		string(cancellation.CancelledBy), cancellation.AmountCents, cancellation.RefundCents,
		cancellation.PaymentIntentID, string(cancellation.RefundStatus))
	return uniqueAs(err, ErrNotCancellable, "insert cancellation")
}

func markCancelled(ctx context.Context, tx pgx.Tx, bookingID uuid.UUID, from string) error {
	tag, err := tx.Exec(ctx, `UPDATE app.bookings SET status = 'cancelled' WHERE id = $1 AND status = $2`, bookingID, from)
	if err != nil {
		return fmt.Errorf("cancel booking: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotCancellable
	}
	return nil
}

func (store *PostgresCancellationStore) CreateReport(ctx context.Context, report Report) error {
	return db.WithUserTx(ctx, store.DB, report.GuestID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO app.stay_reports
			     (booking_id, guest_id, host_id, listing_title, details, amount_cents, payment_intent_id, status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'open')`,
			report.BookingID, report.GuestID, report.HostID, report.ListingTitle, report.Details,
			report.AmountCents, report.PaymentIntentID)
		return uniqueAs(err, ErrAlreadyReported, "insert report")
	})
}

func (store *PostgresCancellationStore) IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var admin bool
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.is_admin()`).Scan(&admin)
	})
	if err != nil {
		return false, fmt.Errorf("check admin: %w", err)
	}
	return admin, nil
}

func (store *PostgresCancellationStore) TeamReports(ctx context.Context, adminID uuid.UUID, status ReportStatus, limit int) ([]TeamReport, error) {
	reports := []TeamReport{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT r.booking_id, r.guest_id, r.host_id, r.listing_title, r.details, r.amount_cents, r.payment_intent_id,
			        r.status, r.refund_cents, r.refund_status, COALESCE(r.resolution_note, ''), r.created_at,
			        COALESCE(NULLIF(guest.display_name, ''), 'Guest'),
			        COALESCE(NULLIF(l.host_name, ''), NULLIF(host.display_name, ''), 'Host'),
			        r.guest_id <> $2 AND r.host_id <> $2, r.resolved_at
			 FROM app.stay_reports r
			 LEFT JOIN app.bookings b ON b.id = r.booking_id
			 LEFT JOIN app.listings l ON l.id = b.listing_id
			 LEFT JOIN app.account_names guest ON guest.account_id = r.guest_id
			 LEFT JOIN app.account_names host ON host.account_id = r.host_id
			 WHERE r.status = $1
			 ORDER BY CASE WHEN $1 = 'open' THEN r.created_at END ASC, r.resolved_at DESC, r.booking_id
			 LIMIT $3`, string(status), adminID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var report TeamReport
			if err := rows.Scan(
				&report.BookingID, &report.GuestID, &report.HostID, &report.ListingTitle, &report.Details,
				&report.AmountCents, &report.PaymentIntentID, &report.Status, &report.RefundCents,
				&report.RefundStatus, &report.Note, &report.CreatedAt,
				&report.GuestName, &report.HostName, &report.CanDecide, &report.ResolvedAt,
			); err != nil {
				return err
			}
			reports = append(reports, report)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	return reports, nil
}

func (store *PostgresCancellationStore) SuspendedBooking(ctx context.Context, adminID, bookingID uuid.UUID) (Booking, error) {
	var booking Booking
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT b.id, b.listing_id, b.guest_id, l.host_id, b.status, b.starts_on,
			        to_char(l.check_in_time, 'HH24:MI'), l.time_zone,
			        COALESCE(p.amount_cents, 0), COALESCE(p.status, ''), COALESCE(p.stripe_payment_intent_id, '')
			 FROM app.bookings b
			 JOIN app.listings l ON l.id = b.listing_id
			 LEFT JOIN app.payments p ON p.booking_id = b.id
			 WHERE b.id = $1 AND b.status IN ('requested', 'pending', 'confirmed')
			   AND (app.is_banned(b.guest_id) OR app.is_banned(l.host_id))`,
			bookingID,
		).Scan(
			&booking.BookingID, &booking.ListingID, &booking.GuestID, &booking.HostID, &booking.Status,
			&booking.StartsOn, &booking.CheckInTime, &booking.TimeZone,
			&booking.AmountCents, &booking.PaymentStatus, &booking.PaymentIntentID,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, ErrBookingNotFound
	}
	if err != nil {
		return Booking{}, fmt.Errorf("load booking for the team: %w", err)
	}
	return booking, nil
}

func (store *PostgresCancellationStore) TeamCancel(ctx context.Context, adminID uuid.UUID, cancellation Cancellation, fromStatus string) error {
	return db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		if err := insertCancellation(ctx, tx, cancellation); err != nil {
			return err
		}
		return markCancelled(ctx, tx, cancellation.BookingID, fromStatus)
	})
}

func (store *PostgresCancellationStore) Resolve(ctx context.Context, adminID, bookingID uuid.UUID, decision Decision, at time.Time) (Report, error) {
	var report Report
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, adminID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		current, err := scanReport(tx.QueryRow(ctx, `SELECT `+reportColumns+` FROM app.stay_reports WHERE booking_id = $1`, bookingID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBookingNotFound
		}
		if err != nil {
			return fmt.Errorf("select report: %w", err)
		}
		if current.GuestID == adminID || current.HostID == adminID {
			return ErrPartyToReport
		}
		if current.Status != ReportOpen {
			return ErrAlreadyDecided
		}
		if decision.RefundCents > current.AmountCents {
			return fmt.Errorf("%w: the refund is more than the guest paid", ErrInvalidReport)
		}
		report, err = scanReport(tx.QueryRow(ctx,
			`UPDATE app.stay_reports
			 SET status = 'resolved', refund_cents = $2,
			     refund_status = CASE WHEN $2 > 0 THEN 'pending' ELSE 'none' END,
			     resolution_note = NULLIF($3, ''), resolved_by = $4, resolved_at = $5
			 WHERE booking_id = $1 AND status = 'open'
			 RETURNING `+reportColumns,
			bookingID, decision.RefundCents, decision.Note, adminID, at))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAlreadyDecided
		}
		if err != nil {
			return fmt.Errorf("resolve report: %w", err)
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	return report, nil
}

func (store *PostgresCancellationStore) PendingRefunds(ctx context.Context, limit int) ([]PendingRefund, error) {
	rows, err := store.DB.Query(ctx,
		`SELECT booking_id, guest_id, source, payment_intent_id, amount_cents FROM app.pending_booking_refunds($1)`, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending refunds: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PendingRefund, error) {
		var refund PendingRefund
		err := row.Scan(&refund.BookingID, &refund.GuestID, &refund.Source, &refund.PaymentIntentID, &refund.AmountCents)
		return refund, err
	})
}

func (store *PostgresCancellationStore) MarkRefunded(ctx context.Context, refund PendingRefund, refundID string) error {
	query := `UPDATE app.booking_cancellations SET refund_status = 'succeeded', stripe_refund_id = $2
		WHERE booking_id = $1 AND refund_status = 'pending'`
	if refund.Source == SourceReport {
		query = `UPDATE app.stay_reports SET refund_status = 'succeeded', stripe_refund_id = $2
			WHERE booking_id = $1 AND refund_status = 'pending'`
	}
	return db.WithUserTx(ctx, store.DB, refund.GuestID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, query, refund.BookingID, refundID); err != nil {
			return fmt.Errorf("record refund: %w", err)
		}
		return nil
	})
}

func (store *PostgresCancellationStore) AdminListings(ctx context.Context, adminID uuid.UUID, cancellationsSince time.Time, limit int) ([]AdminListing, error) {
	listings := []AdminListing{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT l.id, l.host_id, l.title, COALESCE(l.host_name, ''), COALESCE(l.city, ''), l.status,
			        s.listing_id IS NOT NULL, COALESCE(s.reason, ''), s.created_at,
			        (SELECT count(*)::integer FROM app.booking_cancellations c
			         WHERE c.listing_id = l.id AND c.cancelled_by = 'host' AND c.created_at > $1),
			        COALESCE((SELECT p.object_key FROM app.listing_photos p
			                  WHERE p.listing_id = l.id AND p.screening = 'passed' ORDER BY p.position LIMIT 1), '')
			 FROM app.listings l
			 LEFT JOIN app.listing_suspensions s ON s.listing_id = l.id AND s.lifted_at IS NULL
			 WHERE l.status = 'published' OR s.listing_id IS NOT NULL
			 ORDER BY (s.listing_id IS NULL), s.created_at DESC, l.title, l.id
			 LIMIT $2`, cancellationsSince, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var listing AdminListing
			if err := rows.Scan(
				&listing.ListingID, &listing.HostID, &listing.Title, &listing.HostName, &listing.City, &listing.Status,
				&listing.Paused, &listing.Reason, &listing.PausedAt, &listing.HostCancellations,
				&listing.CoverPhotoKey,
			); err != nil {
				return err
			}
			listings = append(listings, listing)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list listings for the team: %w", err)
	}
	return listings, nil
}

func (store *PostgresCancellationStore) PopularListings(ctx context.Context, adminID uuid.UUID, bookedSince time.Time, cities []string, limit int) ([]PopularListing, error) {
	listings := []PopularListing{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT l.id, l.title, COALESCE(l.host_name, ''), COALESCE(l.city, ''),
			        COALESCE(b.bookings, 0), COALESCE(r.reviews, 0), COALESCE(r.rating, 0),
			        COALESCE((SELECT p.object_key FROM app.listing_photos p
			                  WHERE p.listing_id = l.id AND p.screening = 'passed' ORDER BY p.position LIMIT 1), '')
			 FROM app.listings l
			 LEFT JOIN (
			     SELECT listing_id, count(*)::integer AS bookings
			     FROM app.bookings
			     WHERE status IN ('confirmed', 'completed') AND created_at > $1
			     GROUP BY listing_id
			 ) b ON b.listing_id = l.id
			 LEFT JOIN (
			     SELECT listing_id, count(*)::integer AS reviews, avg(rating)::float8 AS rating
			     FROM app.listing_reviews
			     GROUP BY listing_id
			 ) r ON r.listing_id = l.id
			 WHERE l.status = 'published'
			   AND NOT EXISTS (SELECT 1 FROM app.listing_suspensions s WHERE s.listing_id = l.id AND s.lifted_at IS NULL)
			   AND (cardinality($3::text[]) = 0 OR lower(btrim(COALESCE(l.city, ''))) = ANY($3::text[]))
			 ORDER BY COALESCE(b.bookings, 0) DESC, COALESCE(r.reviews, 0) DESC, COALESCE(r.rating, 0) DESC, l.title, l.id
			 LIMIT $2`, bookedSince, limit, cities)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var listing PopularListing
			if err := rows.Scan(
				&listing.ListingID, &listing.Title, &listing.HostName, &listing.City,
				&listing.Bookings, &listing.Reviews, &listing.Rating, &listing.CoverPhotoKey,
			); err != nil {
				return err
			}
			listings = append(listings, listing)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list popular listings: %w", err)
	}
	return listings, nil
}

func (store *PostgresCancellationStore) PauseListing(ctx context.Context, adminID, listingID uuid.UUID, reason string) (uuid.UUID, error) {
	var hostID uuid.UUID
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT host_id FROM app.listings WHERE id = $1`, listingID).Scan(&hostID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrListingNotFound
		}
		if err != nil {
			return fmt.Errorf("select listing: %w", err)
		}
		tag, err := tx.Exec(ctx,
			`INSERT INTO app.listing_suspensions (listing_id, host_id, reason, paused_by)
			 VALUES ($1, $2, $3, 'team')
			 ON CONFLICT (listing_id) DO UPDATE
			 SET reason = EXCLUDED.reason, paused_by = 'team', created_at = now(), lifted_at = NULL, lifted_by = NULL
			 WHERE app.listing_suspensions.lifted_at IS NOT NULL OR app.listing_suspensions.paused_by = 'verification'`,
			listingID, hostID, reason)
		if err != nil {
			return fmt.Errorf("pause listing: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrAlreadyPaused
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return hostID, nil
}

func (store *PostgresCancellationStore) UnpauseListing(ctx context.Context, adminID, listingID uuid.UUID, at time.Time) (uuid.UUID, error) {
	var hostID uuid.UUID
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, adminID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		err := tx.QueryRow(ctx,
			`UPDATE app.listing_suspensions SET lifted_at = $2, lifted_by = $3
			 WHERE listing_id = $1 AND lifted_at IS NULL
			 RETURNING host_id`,
			listingID, at, adminID).Scan(&hostID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPaused
		}
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == restrictViolationCode {
			return ErrHostBanned
		}
		if err != nil {
			return fmt.Errorf("lift pause: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return hostID, nil
}

func uniqueAs(err, conflict error, operation string) error {
	var pgError *pgconn.PgError
	switch {
	case errors.As(err, &pgError) && pgError.Code == uniqueViolationCode:
		return conflict
	case err != nil:
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}
