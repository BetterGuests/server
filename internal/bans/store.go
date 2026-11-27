package bans

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
	foreignKeyViolation   = "23503"
	uniqueViolation       = "23505"
	insufficientPrivilege = "42501"
)

type PostgresBanStore struct {
	DB *pgxpool.Pool
}

func NewPostgresBanStore(pool *pgxpool.Pool) *PostgresBanStore {
	return &PostgresBanStore{DB: pool}
}

func (store *PostgresBanStore) IsBanned(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var banned bool
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.is_banned($1)`, accountID).Scan(&banned)
	})
	if err != nil {
		return false, fmt.Errorf("select ban: %w", err)
	}
	return banned, nil
}

func (store *PostgresBanStore) Ban(ctx context.Context, adminID, banID uuid.UUID, request Request, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, adminID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO app.account_bans (id, account_id, account_name, reason, banned_by, banned_at)
			 VALUES ($1, $2, left(COALESCE(
			     (SELECT NULLIF(n.display_name, '') FROM app.account_names n WHERE n.account_id = $2),
			     (SELECT l.host_name FROM app.listings l WHERE l.host_id = $2 AND l.host_name <> '' ORDER BY l.id LIMIT 1),
			     (SELECT r.subject_name FROM app.user_reports r WHERE r.subject_id = $2 ORDER BY r.created_at DESC LIMIT 1),
			     'Member'), 200), $3, $4, $5)`,
			banID, request.AccountID, request.Reason, adminID, at)
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) {
			switch pgError.Code {
			case foreignKeyViolation:
				return ErrAccountNotFound
			case uniqueViolation:
				return ErrAlreadyBanned
			case insufficientPrivilege:
				return ErrCannotBanAdmin
			}
		}
		if err != nil {
			return fmt.Errorf("insert ban: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.listing_suspensions (listing_id, host_id, reason, paused_by)
			 SELECT l.id, l.host_id, $2, 'ban' FROM app.listings l
			 WHERE l.host_id = $1 AND l.status = 'published'
			 ON CONFLICT (listing_id) DO UPDATE
			 SET reason = EXCLUDED.reason, paused_by = 'ban', created_at = now(), lifted_at = NULL, lifted_by = NULL
			 WHERE app.listing_suspensions.lifted_at IS NOT NULL OR app.listing_suspensions.paused_by = 'verification'`,
			request.AccountID, pauseReason); err != nil {
			return fmt.Errorf("pause listings: %w", err)
		}
		return nil
	})
}

func (store *PostgresBanStore) Lift(ctx context.Context, adminID, banID uuid.UUID, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, adminID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		var accountID uuid.UUID
		err := tx.QueryRow(ctx,
			`UPDATE app.account_bans SET lifted_at = $2, lifted_by = $3
			 WHERE id = $1 AND lifted_at IS NULL
			 RETURNING account_id`,
			banID, at, adminID).Scan(&accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBanNotFound
		}
		if err != nil {
			return fmt.Errorf("lift ban: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app.listing_suspensions SET lifted_at = $2, lifted_by = $3
			 WHERE host_id = $1 AND paused_by = 'ban' AND lifted_at IS NULL`,
			accountID, at, adminID); err != nil {
			return fmt.Errorf("reopen listings: %w", err)
		}
		return nil
	})
}

func (store *PostgresBanStore) List(ctx context.Context, adminID uuid.UUID, limit int) ([]Ban, error) {
	found := []Ban{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, account_id, account_name, reason, banned_at, lifted_at
			 FROM app.account_bans WHERE lifted_at IS NULL
			 ORDER BY banned_at DESC, id LIMIT $1`, limit)
		if err != nil {
			return err
		}
		found, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Ban, error) {
			ban := Ban{Bookings: []Booking{}}
			err := row.Scan(&ban.ID, &ban.AccountID, &ban.Name, &ban.Reason, &ban.BannedAt, &ban.LiftedAt)
			return ban, err
		})
		if err != nil {
			return err
		}
		return attachUpcomingBookings(ctx, tx, found)
	})
	if err != nil {
		return nil, fmt.Errorf("select bans: %w", err)
	}
	return found, nil
}

func attachUpcomingBookings(ctx context.Context, tx pgx.Tx, found []Ban) error {
	positions := map[uuid.UUID]int{}
	banned := make([]uuid.UUID, len(found))
	for index, ban := range found {
		positions[ban.AccountID] = index
		banned[index] = ban.AccountID
	}
	rows, err := tx.Query(ctx,
		`SELECT b.id, l.title, b.guest_id, l.host_id, to_char(b.starts_on, 'YYYY-MM-DD'), to_char(b.ends_on, 'YYYY-MM-DD'),
		        b.status, CASE WHEN p.status = 'succeeded' THEN p.amount_cents ELSE 0 END
		 FROM app.bookings b
		 JOIN app.listings l ON l.id = b.listing_id
		 LEFT JOIN app.payments p ON p.booking_id = b.id
		 WHERE (b.guest_id = ANY($1) OR l.host_id = ANY($1))
		   AND b.status IN ('requested', 'pending', 'confirmed')
		   AND b.ends_on >= current_date
		 ORDER BY b.starts_on, b.id`, banned)
	if err != nil {
		return fmt.Errorf("select upcoming bookings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var booking Booking
		var guestID, hostID uuid.UUID
		if err := rows.Scan(&booking.ID, &booking.ListingTitle, &guestID, &hostID,
			&booking.StartsOn, &booking.EndsOn, &booking.Status, &booking.PaidCents); err != nil {
			return fmt.Errorf("scan upcoming booking: %w", err)
		}
		if index, ok := positions[guestID]; ok {
			found[index].Bookings = append(found[index].Bookings, booking)
		}
		if index, ok := positions[hostID]; ok {
			booking.BannedIsHost = true
			found[index].Bookings = append(found[index].Bookings, booking)
		}
	}
	return rows.Err()
}
