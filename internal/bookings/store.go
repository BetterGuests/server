package bookings

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

const (
	exclusionViolationCode = "23P01"
	restrictViolationCode  = "23001"
	tripOverlapCode        = "BG001"
)

const maxListedBookings = 100

const detailColumns = `b.id, b.listing_id, b.guest_id, l.host_id, b.status, b.starts_on, b.ends_on,
	b.completed_at, b.decided_at, b.verification_id, b.created_at,
	COALESCE(gn.display_name, ''), l.title, COALESCE(l.city, ''), COALESCE(l.region, ''),
	COALESCE(l.host_name, ''), COALESCE(l.host_avatar_url, ''),
	(b.ends_on - b.starts_on), COALESCE(l.nightly_price_cents, 0), l.currency, COALESCE(p.status, ''),
	COALESCE(p.amount_cents, 0)`

const detailFrom = `FROM app.bookings b
	JOIN app.listings l ON l.id = b.listing_id
	LEFT JOIN app.account_names gn ON gn.account_id = b.guest_id
	LEFT JOIN app.payments p ON p.booking_id = b.id`

type PostgresBookingStore struct{ DB *pgxpool.Pool }

func NewPostgresBookingStore(pool *pgxpool.Pool) *PostgresBookingStore {
	return &PostgresBookingStore{DB: pool}
}

func (store *PostgresBookingStore) Create(ctx context.Context, actingUserID string, booking Booking) error {
	return db.WithUserTx(ctx, store.DB, actingUserID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`,
			booking.GuestID,
		); err != nil {
			return fmt.Errorf("ensure guest account: %w", err)
		}

		_, err := tx.Exec(
			ctx,
			`INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on, created_at, verification_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			booking.ID,
			booking.ListingID,
			booking.GuestID,
			booking.Status,
			booking.StartsOn,
			booking.EndsOn,
			booking.CreatedAt,
			booking.VerificationID,
		)

		var pgError *pgconn.PgError
		switch {
		case errors.As(err, &pgError) && pgError.Code == exclusionViolationCode:
			return ErrBookingOverlaps
		case errors.As(err, &pgError) && pgError.Code == tripOverlapCode:
			return ErrTripOverlaps
		case errors.As(err, &pgError) && pgError.Code == restrictViolationCode:
			return ErrListingPaused
		case err != nil:
			return fmt.Errorf("insert booking: %w", err)
		}
		return nil
	})
}

func (store *PostgresBookingStore) Get(ctx context.Context, actorID, bookingID uuid.UUID) (Detail, error) {
	var detail Detail
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		var err error
		detail, err = scanDetail(tx.QueryRow(ctx, `SELECT `+detailColumns+` `+detailFrom+` WHERE b.id = $1`, bookingID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrBookingNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("select booking: %w", err)
	}
	return withRole(detail, actorID), nil
}

func (store *PostgresBookingStore) ListForGuest(ctx context.Context, guestID uuid.UUID) ([]Detail, error) {
	return store.list(ctx, guestID, `WHERE b.guest_id = $1 AND NOT (b.status = 'requested' AND COALESCE(p.status, '') NOT IN ('authorized', 'succeeded')) ORDER BY b.starts_on DESC, b.created_at DESC LIMIT $2`)
}

func (store *PostgresBookingStore) ListOverlapping(ctx context.Context, guestID uuid.UUID, startsOn, endsOn time.Time) ([]Detail, error) {
	return store.list(ctx, guestID, `WHERE b.guest_id = $1 AND b.status IN ('requested', 'pending', 'confirmed') AND daterange(b.starts_on, b.ends_on, '[)') && daterange($3, $4, '[)') ORDER BY b.starts_on, b.created_at LIMIT $2`, startsOn, endsOn)
}

func (store *PostgresBookingStore) ListForHost(ctx context.Context, hostID uuid.UUID) ([]Detail, error) {
	return store.list(ctx, hostID, `WHERE l.host_id = $1 AND b.ends_on >= CURRENT_DATE - 31 AND (b.status IN ('pending', 'confirmed', 'completed') OR (b.status = 'requested' AND p.status = 'authorized')) ORDER BY b.starts_on ASC, b.created_at ASC LIMIT $2`)
}

func (store *PostgresBookingStore) ListRequestsForHost(ctx context.Context, hostID uuid.UUID) ([]Detail, error) {
	return store.list(ctx, hostID, `WHERE l.host_id = $1 AND b.status = 'requested' AND p.status = 'authorized' ORDER BY b.created_at ASC LIMIT $2`)
}

func (store *PostgresBookingStore) list(ctx context.Context, actorID uuid.UUID, clause string, extra ...any) ([]Detail, error) {
	var details []Detail
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		arguments := append([]any{actorID, maxListedBookings}, extra...)
		rows, err := tx.Query(ctx, `SELECT `+detailColumns+` `+detailFrom+` `+clause, arguments...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			detail, err := scanDetail(rows)
			if err != nil {
				return err
			}
			details = append(details, withRole(detail, actorID))
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list bookings: %w", err)
	}
	return details, nil
}

func (store *PostgresBookingStore) Decide(ctx context.Context, hostID, bookingID uuid.UUID, next Status, decidedAt time.Time) error {
	return db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE app.bookings b
			 SET status = $3, decided_at = $4
			 FROM app.listings l
			 WHERE b.id = $1 AND l.id = b.listing_id AND l.host_id = $2 AND b.status = 'requested'`,
			bookingID, hostID, string(next), decidedAt)
		if err != nil {
			return fmt.Errorf("decide booking: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		return missingOrConflict(ctx, tx, ErrNotAwaitingApproval,
			`SELECT EXISTS (
				SELECT 1 FROM app.bookings b JOIN app.listings l ON l.id = b.listing_id
				WHERE b.id = $1 AND l.host_id = $2)`,
			bookingID, hostID)
	})
}

func (store *PostgresBookingStore) CancelByGuest(ctx context.Context, guestID, bookingID uuid.UUID) error {
	return db.WithUserTx(ctx, store.DB, guestID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE app.bookings SET status = 'cancelled'
			 WHERE id = $1 AND guest_id = $2 AND status IN ('requested', 'pending')
			   AND NOT EXISTS (SELECT 1 FROM app.payments p WHERE p.booking_id = $1 AND p.status = 'succeeded')`,
			bookingID, guestID)
		if err != nil {
			return fmt.Errorf("cancel booking: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		return missingOrConflict(ctx, tx, ErrNotCancellable,
			`SELECT EXISTS (SELECT 1 FROM app.bookings WHERE id = $1 AND guest_id = $2)`,
			bookingID, guestID)
	})
}

func missingOrConflict(ctx context.Context, tx pgx.Tx, conflict error, ownsQuery string, args ...any) error {
	var owns bool
	if err := tx.QueryRow(ctx, ownsQuery, args...).Scan(&owns); err != nil {
		return fmt.Errorf("look up booking: %w", err)
	}
	if !owns {
		return ErrBookingNotFound
	}
	return conflict
}

func scanDetail(row pgx.Row) (Detail, error) {
	var detail Detail
	var chargedCents int
	err := row.Scan(
		&detail.ID, &detail.ListingID, &detail.GuestID, &detail.HostID, &detail.Status, &detail.StartsOn, &detail.EndsOn,
		&detail.CompletedAt, &detail.DecidedAt, &detail.VerificationID, &detail.CreatedAt,
		&detail.GuestName, &detail.ListingTitle, &detail.ListingCity, &detail.ListingRegion,
		&detail.HostName, &detail.HostAvatarURL,
		&detail.Nights, &detail.NightlyPriceCents, &detail.Currency, &detail.PaymentStatus, &chargedCents,
	)
	if err != nil {
		return Detail{}, err
	}
	detail.GuestVerified = detail.VerificationID != nil
	detail.TotalCents = detail.Nights * detail.NightlyPriceCents
	if chargedCents > 0 {
		detail.TotalCents = chargedCents
	}
	return detail, nil
}

func withRole(detail Detail, actorID uuid.UUID) Detail {
	detail.Role = RoleGuest
	if detail.HostID == actorID {
		detail.Role = RoleHost
	}
	return detail
}
