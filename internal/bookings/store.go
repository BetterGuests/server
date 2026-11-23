package bookings

import (
	"context"
	"errors"
	"fmt"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// exclusionViolationCode is the PostgreSQL error code for a failed
// exclusion constraint (23P01). PostgresBookingStore.Create translates it to
// ErrBookingOverlaps so callers never see a raw Postgres error code.
const exclusionViolationCode = "23P01"

// PostgresBookingStore persists rows in app.bookings.
type PostgresBookingStore struct{ DB *pgxpool.Pool }

// NewPostgresBookingStore returns a booking store backed by pool.
func NewPostgresBookingStore(pool *pgxpool.Pool) *PostgresBookingStore {
	return &PostgresBookingStore{DB: pool}
}

// Create inserts a new booking row. The bookings_no_overlap exclusion
// constraint (db/migrations/00008_booking_overlap_prevention.sql) rejects
// the insert if it overlaps another pending or confirmed booking for the
// same listing; under concurrent requests for the same dates, PostgreSQL
// guarantees exactly one of them wins and the rest get this error.
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
		case err != nil:
			return fmt.Errorf("insert booking: %w", err)
		}
		return nil
	})
}
