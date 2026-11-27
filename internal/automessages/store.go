package automessages

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

type PostgresAutoMessageStore struct{ DB *pgxpool.Pool }

func NewPostgresAutoMessageStore(pool *pgxpool.Pool) *PostgresAutoMessageStore {
	return &PostgresAutoMessageStore{DB: pool}
}

func (store *PostgresAutoMessageStore) Overview(ctx context.Context, hostID, listingID uuid.UUID) (Overview, error) {
	var overview Overview
	err := db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT title FROM app.listings WHERE id = $1 AND host_id = $2`, listingID, hostID,
		).Scan(&overview.ListingTitle)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrListingNotFound
		}
		if err != nil {
			return fmt.Errorf("select listing: %w", err)
		}
		rows, err := tx.Query(ctx,
			`SELECT id, listing_id, name, trigger, body, private, enabled, updated_at
			 FROM app.listing_auto_messages
			 WHERE listing_id = $1 AND host_id = $2
			 ORDER BY array_position(
			     ARRAY['booking_confirmed', 'before_check_in', 'check_in_day', 'check_out_eve', 'check_out_day'], trigger),
			     created_at, id`,
			listingID, hostID)
		if err != nil {
			return fmt.Errorf("list automatic messages: %w", err)
		}
		overview.Messages, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AutoMessage, error) {
			var message AutoMessage
			err := row.Scan(&message.ID, &message.ListingID, &message.Name, &message.Trigger, &message.Body,
				&message.Private, &message.Enabled, &message.UpdatedAt)
			return message, err
		})
		if err != nil {
			return fmt.Errorf("list automatic messages: %w", err)
		}
		return nil
	})
	if err != nil {
		return Overview{}, err
	}
	return overview, nil
}

func (store *PostgresAutoMessageStore) Create(ctx context.Context, hostID uuid.UUID, message AutoMessage, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		var locked uuid.UUID
		err := tx.QueryRow(ctx,
			`SELECT id FROM app.listings WHERE id = $1 AND host_id = $2 FOR UPDATE`, message.ListingID, hostID,
		).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrListingNotFound
		}
		if err != nil {
			return fmt.Errorf("lock listing: %w", err)
		}
		var existing int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM app.listing_auto_messages WHERE listing_id = $1`, message.ListingID,
		).Scan(&existing); err != nil {
			return fmt.Errorf("count automatic messages: %w", err)
		}
		if existing >= MaxPerListing {
			return ErrTooMany
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.listing_auto_messages
			     (id, listing_id, host_id, name, trigger, body, private, enabled, active_since, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $9)`,
			message.ID, message.ListingID, hostID, message.Name, string(message.Trigger), message.Body,
			message.Private, message.Enabled, at); err != nil {
			return fmt.Errorf("insert automatic message: %w", err)
		}
		return nil
	})
}

func (store *PostgresAutoMessageStore) Update(ctx context.Context, hostID uuid.UUID, message AutoMessage, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE app.listing_auto_messages
			 SET active_since = CASE WHEN trigger <> $5 OR ($8 AND NOT enabled) THEN $9 ELSE active_since END,
			     name = $4, trigger = $5, body = $6, private = $7, enabled = $8, updated_at = $9
			 WHERE id = $1 AND listing_id = $2 AND host_id = $3`,
			message.ID, message.ListingID, hostID, message.Name, string(message.Trigger), message.Body,
			message.Private, message.Enabled, at)
		if err != nil {
			return fmt.Errorf("update automatic message: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrMessageNotFound
		}
		return nil
	})
}

func (store *PostgresAutoMessageStore) Delete(ctx context.Context, hostID, listingID, messageID uuid.UUID) error {
	return db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM app.listing_auto_messages WHERE id = $1 AND listing_id = $2 AND host_id = $3`,
			messageID, listingID, hostID)
		if err != nil {
			return fmt.Errorf("delete automatic message: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrMessageNotFound
		}
		return nil
	})
}

func (store *PostgresAutoMessageStore) Candidates(ctx context.Context, afterBooking, afterMessage uuid.UUID, limit int) ([]Candidate, error) {
	rows, err := store.DB.Query(ctx,
		`SELECT booking_id, auto_message_id, trigger, body, private, quotes_secret, has_arrival_guide,
		        active_since, updated_at, confirmed_at, starts_on, ends_on,
		        check_in_time, check_out_time, time_zone, guest_name, listing_title
		 FROM app.auto_message_candidates($1, $2, $3)`, afterBooking, afterMessage, limit)
	if err != nil {
		return nil, fmt.Errorf("query candidates: %w", err)
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Candidate, error) {
		var c Candidate
		err := row.Scan(&c.BookingID, &c.AutoMessageID, &c.Trigger, &c.Body, &c.Private, &c.QuotesSecret,
			&c.HasArrivalGuide, &c.ActiveSince, &c.UpdatedAt, &c.ConfirmedAt, &c.StartsOn, &c.EndsOn,
			&c.Schedule.CheckInTime, &c.Schedule.CheckOutTime, &c.Schedule.TimeZone, &c.GuestName, &c.ListingTitle)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("read candidates: %w", err)
	}
	return candidates, nil
}

func (store *PostgresAutoMessageStore) Post(ctx context.Context, candidate Candidate, outgoing Outgoing) (Posted, bool, error) {
	var posted Posted
	err := store.DB.QueryRow(ctx,
		`SELECT conversation_id, guest_id, host_id, created_at
		 FROM app.post_auto_message($1, $2, $3, $4, $5, $6, NULLIF($7, ''))`,
		candidate.BookingID, candidate.AutoMessageID, candidate.UpdatedAt,
		outgoing.MessageID, outgoing.ConversationID, outgoing.Body, outgoing.PrivateBody,
	).Scan(&posted.ConversationID, &posted.GuestID, &posted.HostID, &posted.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Posted{}, false, nil
	}
	if err != nil {
		return Posted{}, false, fmt.Errorf("post automatic message: %w", err)
	}
	return posted, true, nil
}

func (store *PostgresAutoMessageStore) Sealed(ctx context.Context, viewerID uuid.UUID, messageIDs []uuid.UUID) ([]Sealed, error) {
	var sealed []Sealed
	err := db.WithUserTx(ctx, store.DB, viewerID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT p.message_id, p.body, c.host_id = $1, b.status, COALESCE(pay.status, ''), b.starts_on, b.ends_on,
			        to_char(l.check_in_time, 'HH24:MI'), to_char(l.check_out_time, 'HH24:MI'), l.time_zone,
			        s.checked_out_at, COALESCE(n.display_name, ''), l.title,
			        COALESCE(a.entry_instructions, ''), COALESCE(a.entry_code, ''), COALESCE(a.wifi_name, ''),
			        COALESCE(a.wifi_password, ''), COALESCE(a.checkout_instructions, '')
			 FROM app.message_private_bodies p
			 JOIN app.messages m ON m.id = p.message_id
			 JOIN app.conversations c ON c.id = m.conversation_id
			 JOIN app.bookings b ON b.id = p.booking_id AND b.guest_id = c.guest_id AND b.listing_id = c.listing_id
			 JOIN app.listings l ON l.id = b.listing_id
			 LEFT JOIN app.payments pay ON pay.booking_id = b.id AND c.guest_id = $1
			 LEFT JOIN app.booking_stays s ON s.booking_id = b.id
			 LEFT JOIN app.account_names n ON n.account_id = b.guest_id
			 LEFT JOIN app.listing_arrival_guides a ON a.listing_id = l.id
			 WHERE p.message_id = ANY($2) AND (c.guest_id = $1 OR c.host_id = $1)`,
			viewerID, messageIDs)
		if err != nil {
			return err
		}
		sealed, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Sealed, error) {
			var item Sealed
			err := row.Scan(&item.MessageID, &item.Template, &item.ViewerIsHost, &item.BookingStatus, &item.PaymentStatus,
				&item.StartsOn, &item.EndsOn,
				&item.Schedule.CheckInTime, &item.Schedule.CheckOutTime, &item.Schedule.TimeZone,
				&item.CheckedOutAt, &item.GuestName, &item.ListingTitle,
				&item.Arrival.EntryInstructions, &item.Arrival.EntryCode, &item.Arrival.WifiName,
				&item.Arrival.WifiPassword, &item.Arrival.CheckoutInstructions)
			return item, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("select private details: %w", err)
	}
	return sealed, nil
}
