package teamevents

import (
	"context"
	"fmt"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresTeamEventsStore struct {
	DB *pgxpool.Pool
}

func NewPostgresTeamEventsStore(pool *pgxpool.Pool) *PostgresTeamEventsStore {
	return &PostgresTeamEventsStore{DB: pool}
}

const eventsSQL = `
WITH feed AS (
	(SELECT 'booking_requested' AS kind, b.created_at AS at, b.id AS ref_id, l.id AS listing_id,
	        l.title, l.city, l.latitude, l.longitude, 0 AS amount_cents, '' AS currency,
	        b.guest_id, l.host_id, b.status AS detail
	 FROM app.bookings b JOIN app.listings l ON l.id = b.listing_id
	 WHERE b.created_at > $1 ORDER BY b.created_at DESC LIMIT $3)
	UNION ALL
	(SELECT CASE p.status WHEN 'succeeded' THEN 'payment_succeeded'
	                      WHEN 'authorized' THEN 'payment_held' ELSE 'payment_failed' END,
	        COALESCE(p.last_event_at, p.updated_at), p.id, l.id,
	        l.title, l.city, l.latitude, l.longitude, p.amount_cents, p.currency,
	        p.guest_id, p.host_id, ''
	 FROM app.payments p
	 JOIN app.bookings b ON b.id = p.booking_id
	 JOIN app.listings l ON l.id = b.listing_id
	 WHERE p.status IN ('authorized', 'succeeded', 'failed') AND p.updated_at > $1
	 ORDER BY p.updated_at DESC LIMIT $3)
	UNION ALL
	(SELECT 'booking_cancelled', c.created_at, c.booking_id, l.id,
	        l.title, l.city, l.latitude, l.longitude, c.amount_cents, '',
	        c.guest_id, c.host_id, c.cancelled_by
	 FROM app.booking_cancellations c JOIN app.listings l ON l.id = c.listing_id
	 WHERE c.created_at > $1 ORDER BY c.created_at DESC LIMIT $3)
	UNION ALL
	(SELECT 'stay_reported', r.created_at, r.booking_id, l.id,
	        l.title, l.city, l.latitude, l.longitude, r.amount_cents, '',
	        r.guest_id, r.host_id, r.status
	 FROM app.stay_reports r
	 JOIN app.bookings b ON b.id = r.booking_id
	 JOIN app.listings l ON l.id = b.listing_id
	 WHERE r.created_at > $1 ORDER BY r.created_at DESC LIMIT $3)
	UNION ALL
	(SELECT 'review_posted', v.created_at, v.id, l.id,
	        l.title, l.city, l.latitude, l.longitude, 0, '',
	        v.guest_id, l.host_id, v.rating::text
	 FROM app.listing_reviews v JOIN app.listings l ON l.id = v.listing_id
	 WHERE v.created_at > $1 ORDER BY v.created_at DESC LIMIT $3)
	UNION ALL
	(SELECT 'listing_published', l.published_at, l.id, l.id,
	        l.title, l.city, l.latitude, l.longitude, 0, '',
	        NULL::uuid, l.host_id, ''
	 FROM app.listings l
	 WHERE l.published_at > $1 ORDER BY l.published_at DESC LIMIT $3)
	UNION ALL
	(SELECT 'listing_paused', s.created_at, s.listing_id, l.id,
	        l.title, l.city, l.latitude, l.longitude, 0, '',
	        NULL::uuid, s.host_id, s.reason
	 FROM app.listing_suspensions s JOIN app.listings l ON l.id = s.listing_id
	 WHERE s.created_at > $1 ORDER BY s.created_at DESC LIMIT $3)
	UNION ALL
	(SELECT 'document_sent', d.created_at, d.id, l.id,
	        l.title, l.city, l.latitude, l.longitude, 0, '',
	        NULL::uuid, d.host_id, d.document_type
	 FROM app.property_proofs d JOIN app.listings l ON l.id = d.listing_id
	 WHERE d.created_at > $1 ORDER BY d.created_at DESC LIMIT $3)
)
SELECT kind, at, ref_id, listing_id, title, city, latitude, longitude, amount_cents, currency,
       guest_id, host_id, detail
FROM feed
WHERE kind = ANY($2)
ORDER BY at DESC, ref_id
LIMIT $3`

func (store *PostgresTeamEventsStore) Events(ctx context.Context, adminID uuid.UUID, query Query) ([]Event, error) {
	kinds := make([]string, len(query.Kinds))
	for index, kind := range query.Kinds {
		kinds[index] = string(kind)
	}
	events := []Event{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, eventsSQL, query.Since, kinds, query.Limit)
		if err != nil {
			return fmt.Errorf("query team events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var event Event
			var kind string
			if err := rows.Scan(
				&kind, &event.At, &event.RefID, &event.ListingID, &event.ListingTitle, &event.City,
				&event.Latitude, &event.Longitude, &event.AmountCents, &event.Currency,
				&event.GuestAccountID, &event.HostAccountID, &event.Detail,
			); err != nil {
				return fmt.Errorf("scan team event: %w", err)
			}
			event.Kind = Kind(kind)
			events = append(events, event)
		}
		return rows.Err()
	})
	return events, err
}
