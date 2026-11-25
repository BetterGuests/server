package stays

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

type PostgresStayStore struct{ DB *pgxpool.Pool }

func NewPostgresStayStore(pool *pgxpool.Pool) *PostgresStayStore {
	return &PostgresStayStore{DB: pool}
}

func (store *PostgresStayStore) Stay(ctx context.Context, actorID, bookingID uuid.UUID) (Record, error) {
	var record Record
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		var (
			arrival     ArrivalDetails
			hasArrival  bool
			bookingScan = []any{
				&record.BookingID, &record.ListingID, &record.ListingTitle, &record.GuestID, &record.HostID,
				&record.Status, &record.StartsOn, &record.EndsOn,
				&record.Schedule.CheckInTime, &record.Schedule.CheckOutTime, &record.Schedule.TimeZone,
				&record.CheckedInAt, &record.CheckedOutAt, &hasArrival,
				&arrival.EntryInstructions, &arrival.EntryCode, &arrival.WifiName, &arrival.WifiPassword,
				&arrival.CheckoutInstructions,
			}
		)
		err := tx.QueryRow(ctx,
			`SELECT b.id, b.listing_id, l.title, b.guest_id, l.host_id, b.status, b.starts_on, b.ends_on,
			        to_char(l.check_in_time, 'HH24:MI'), to_char(l.check_out_time, 'HH24:MI'), l.time_zone,
			        s.checked_in_at, s.checked_out_at, a.listing_id IS NOT NULL,
			        COALESCE(a.entry_instructions, ''), COALESCE(a.entry_code, ''), COALESCE(a.wifi_name, ''),
			        COALESCE(a.wifi_password, ''), COALESCE(a.checkout_instructions, '')
			 FROM app.bookings b
			 JOIN app.listings l ON l.id = b.listing_id
			 LEFT JOIN app.booking_stays s ON s.booking_id = b.id
			 LEFT JOIN app.listing_arrival_guides a ON a.listing_id = b.listing_id
			 WHERE b.id = $1 AND (b.guest_id = $2 OR l.host_id = $2)`,
			bookingID, actorID).Scan(bookingScan...)
		if err != nil {
			return err
		}
		if hasArrival {
			record.Arrival = &arrival
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrStayNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("select stay: %w", err)
	}
	return record, nil
}

func (store *PostgresStayStore) CheckIn(ctx context.Context, guestID, bookingID uuid.UUID, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, guestID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO app.booking_stays (booking_id, guest_id, checked_in_at)
			 SELECT b.id, b.guest_id, $3 FROM app.bookings b
			 WHERE b.id = $1 AND b.guest_id = $2 AND b.status = 'confirmed'
			 ON CONFLICT (booking_id) DO NOTHING`,
			bookingID, guestID, at)
		if err != nil {
			return fmt.Errorf("record check-in: %w", err)
		}
		return nil
	})
}

func (store *PostgresStayStore) CheckOut(ctx context.Context, guestID, bookingID uuid.UUID, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, guestID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE app.booking_stays SET checked_out_at = $3
			 WHERE booking_id = $1 AND guest_id = $2 AND checked_out_at IS NULL`,
			bookingID, guestID, at)
		if err != nil {
			return fmt.Errorf("record check-out: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrCheckOutClosed
		}
		return nil
	})
}

func (store *PostgresStayStore) CompleteFinished(ctx context.Context, batchSize int) (int, error) {
	var completed int
	if err := store.DB.QueryRow(ctx,
		`SELECT count(*) FROM app.complete_finished_stays($1)`, batchSize,
	).Scan(&completed); err != nil {
		return 0, fmt.Errorf("complete stays: %w", err)
	}
	return completed, nil
}

func (store *PostgresStayStore) Guide(ctx context.Context, hostID, listingID uuid.UUID) (ArrivalGuide, error) {
	guide := ArrivalGuide{ListingID: listingID}
	err := db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT l.title, a.listing_id IS NOT NULL,
			        to_char(l.check_in_time, 'HH24:MI'), to_char(l.check_out_time, 'HH24:MI'), l.time_zone,
			        COALESCE(a.entry_instructions, ''), COALESCE(a.entry_code, ''), COALESCE(a.wifi_name, ''),
			        COALESCE(a.wifi_password, ''), COALESCE(a.checkout_instructions, '')
			 FROM app.listings l
			 LEFT JOIN app.listing_arrival_guides a ON a.listing_id = l.id
			 WHERE l.id = $1 AND l.host_id = $2`,
			listingID, hostID,
		).Scan(
			&guide.ListingTitle, &guide.Saved,
			&guide.CheckInTime, &guide.CheckOutTime, &guide.TimeZone,
			&guide.EntryInstructions, &guide.EntryCode, &guide.WifiName,
			&guide.WifiPassword, &guide.CheckoutInstructions,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArrivalGuide{}, ErrListingNotFound
	}
	if err != nil {
		return ArrivalGuide{}, fmt.Errorf("select arrival guide: %w", err)
	}
	return guide, nil
}

func (store *PostgresStayStore) SaveGuide(ctx context.Context, hostID, listingID uuid.UUID, input GuideInput, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			     SELECT 1 FROM app.bookings b
			     JOIN app.listings l ON l.id = b.listing_id
			     WHERE l.id = $1 AND l.host_id = $2 AND b.status = 'confirmed'
			       AND (now() AT TIME ZONE l.time_zone)::date BETWEEN b.starts_on AND b.ends_on
			       AND (l.check_in_time, l.check_out_time, l.time_zone) IS DISTINCT FROM ($3::time, $4::time, $5::text))`,
			listingID, hostID, input.CheckInTime, input.CheckOutTime, input.TimeZone,
		).Scan(&locked); err != nil {
			return fmt.Errorf("check for a current guest: %w", err)
		}
		if locked {
			return ErrScheduleLocked
		}
		tag, err := tx.Exec(ctx,
			`UPDATE app.listings
			 SET check_in_time = $3::time, check_out_time = $4::time, time_zone = $5, updated_at = $6
			 WHERE id = $1 AND host_id = $2`,
			listingID, hostID, input.CheckInTime, input.CheckOutTime, input.TimeZone, at)
		if err != nil {
			return fmt.Errorf("update listing schedule: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrListingNotFound
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.listing_arrival_guides
			     (listing_id, host_id, entry_instructions, entry_code, wifi_name, wifi_password, checkout_instructions, updated_at)
			 VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), $8)
			 ON CONFLICT (listing_id) DO UPDATE
			 SET entry_instructions = EXCLUDED.entry_instructions, entry_code = EXCLUDED.entry_code,
			     wifi_name = EXCLUDED.wifi_name, wifi_password = EXCLUDED.wifi_password,
			     checkout_instructions = EXCLUDED.checkout_instructions, updated_at = EXCLUDED.updated_at`,
			listingID, hostID, input.EntryInstructions, input.EntryCode, input.WifiName, input.WifiPassword,
			input.CheckoutInstructions, at,
		); err != nil {
			return fmt.Errorf("save arrival guide: %w", err)
		}
		return nil
	})
}
