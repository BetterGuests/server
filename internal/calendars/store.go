package calendars

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

const uniqueViolationCode = "23505"

type PostgresCalendarStore struct{ DB *pgxpool.Pool }

func NewPostgresCalendarStore(pool *pgxpool.Pool) *PostgresCalendarStore {
	return &PostgresCalendarStore{DB: pool}
}

func (store *PostgresCalendarStore) asHost(ctx context.Context, hostID, listingID uuid.UUID, work func(pgx.Tx) error) error {
	return db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.listings WHERE id = $1 AND host_id = $2)`, listingID, hostID).Scan(&owned); err != nil {
			return fmt.Errorf("look up listing: %w", err)
		}
		if !owned {
			return ErrListingNotFound
		}
		return work(tx)
	})
}

func (store *PostgresCalendarStore) Overview(ctx context.Context, hostID, listingID uuid.UUID, newToken string, today time.Time) (Overview, error) {
	overview := Overview{Feeds: []Feed{}, Blocks: []Block{}, Conflicts: []Block{}}
	err := store.asHost(ctx, hostID, listingID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.listing_calendar_links (listing_id, host_id, token)
			VALUES ($1, $2, $3) ON CONFLICT (listing_id) DO NOTHING`, listingID, hostID, newToken); err != nil {
			return fmt.Errorf("create calendar link: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT token FROM app.listing_calendar_links WHERE listing_id = $1`, listingID).Scan(&overview.ExportToken); err != nil {
			return fmt.Errorf("read calendar link: %w", err)
		}
		if err := readFeeds(ctx, tx, listingID, today, &overview); err != nil {
			return err
		}
		var err error
		overview.Blocks, err = readBlocks(ctx, tx, `
			SELECT f.name, b.starts_on, b.ends_on
			FROM app.listing_calendar_blocks b
			JOIN app.listing_calendar_feeds f ON f.id = b.feed_id
			WHERE b.listing_id = $1 AND b.ends_on >= $2
			ORDER BY b.starts_on, f.name
			LIMIT 500`, listingID, today)
		if err != nil {
			return err
		}
		overview.Conflicts, err = readBlocks(ctx, tx, `
			SELECT f.name, b.starts_on, b.ends_on
			FROM app.listing_calendar_blocks b
			JOIN app.listing_calendar_feeds f ON f.id = b.feed_id
			WHERE b.listing_id = $1 AND b.ends_on >= $2
			  AND EXISTS (
			      SELECT 1 FROM app.bookings k
			      WHERE k.listing_id = b.listing_id
			        AND k.status IN ('requested', 'pending', 'confirmed')
			        AND daterange(k.starts_on, k.ends_on, '[)') && daterange(b.starts_on, b.ends_on, '[)')
			        AND (k.starts_on, k.ends_on) <> (b.starts_on, b.ends_on)
			  )
			ORDER BY b.starts_on, f.name
			LIMIT 100`, listingID, today)
		return err
	})
	return overview, err
}

func readFeeds(ctx context.Context, tx pgx.Tx, listingID uuid.UUID, today time.Time, overview *Overview) error {
	rows, err := tx.Query(ctx, `
		SELECT f.id, f.name, f.url, f.last_synced_at, coalesce(f.last_error, ''),
		       coalesce(sum(b.ends_on - greatest(b.starts_on, $2::date)) FILTER (WHERE b.ends_on > $2::date), 0)
		FROM app.listing_calendar_feeds f
		LEFT JOIN app.listing_calendar_blocks b ON b.feed_id = f.id
		WHERE f.listing_id = $1
		GROUP BY f.id
		ORDER BY f.created_at, f.id`, listingID, today)
	if err != nil {
		return fmt.Errorf("list calendars: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var feed Feed
		if err := rows.Scan(&feed.ID, &feed.Name, &feed.URL, &feed.LastSyncedAt, &feed.LastError, &feed.BlockedNights); err != nil {
			return fmt.Errorf("read calendar: %w", err)
		}
		overview.Feeds = append(overview.Feeds, feed)
	}
	return rows.Err()
}

func readBlocks(ctx context.Context, tx pgx.Tx, query string, listingID uuid.UUID, today time.Time) ([]Block, error) {
	rows, err := tx.Query(ctx, query, listingID, today)
	if err != nil {
		return nil, fmt.Errorf("list blocked dates: %w", err)
	}
	defer rows.Close()
	blocks := []Block{}
	for rows.Next() {
		var name string
		var startsOn, endsOn time.Time
		if err := rows.Scan(&name, &startsOn, &endsOn); err != nil {
			return nil, fmt.Errorf("read blocked dates: %w", err)
		}
		blocks = append(blocks, Block{FeedName: name, StartsOn: startsOn.Format(time.DateOnly), EndsOn: endsOn.Format(time.DateOnly)})
	}
	return blocks, rows.Err()
}

func (store *PostgresCalendarStore) ReplaceToken(ctx context.Context, hostID, listingID uuid.UUID, token string) error {
	return store.asHost(ctx, hostID, listingID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listing_calendar_links SET token = $2, created_at = now() WHERE listing_id = $1`, listingID, token)
		return err
	})
}

func (store *PostgresCalendarStore) AddFeed(ctx context.Context, hostID, listingID uuid.UUID, feed Feed, blocks []Range, syncedAt time.Time) error {
	return store.asHost(ctx, hostID, listingID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO app.listing_calendar_feeds (id, listing_id, host_id, name, url, last_attempt_at, last_synced_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)`, feed.ID, listingID, hostID, feed.Name, feed.URL, syncedAt)
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == uniqueViolationCode {
			return ErrDuplicateFeed
		}
		if err != nil {
			return fmt.Errorf("add calendar: %w", err)
		}
		return insertBlocks(ctx, tx, DueFeed{ID: feed.ID, ListingID: listingID, HostID: hostID}, blocks)
	})
}

func insertBlocks(ctx context.Context, tx pgx.Tx, due DueFeed, blocks []Range) error {
	if len(blocks) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(blocks))
	starts := make([]time.Time, len(blocks))
	ends := make([]time.Time, len(blocks))
	for index, block := range blocks {
		ids[index], starts[index], ends[index] = uuid.New(), block.StartsOn, block.EndsOn
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO app.listing_calendar_blocks (id, feed_id, listing_id, host_id, starts_on, ends_on)
		SELECT block.id, $1, $2, $3, block.starts_on, block.ends_on
		FROM unnest($4::uuid[], $5::date[], $6::date[]) AS block (id, starts_on, ends_on)`,
		due.ID, due.ListingID, due.HostID, ids, starts, ends)
	if err != nil {
		return fmt.Errorf("block dates: %w", err)
	}
	return nil
}

func (store *PostgresCalendarStore) Feed(ctx context.Context, hostID, listingID, feedID uuid.UUID) (Feed, error) {
	feed := Feed{ID: feedID}
	err := store.asHost(ctx, hostID, listingID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT name, url FROM app.listing_calendar_feeds WHERE id = $1 AND listing_id = $2`, feedID, listingID).Scan(&feed.Name, &feed.URL)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrFeedNotFound
		}
		return err
	})
	return feed, err
}

func (store *PostgresCalendarStore) RemoveFeed(ctx context.Context, hostID, listingID, feedID uuid.UUID) error {
	return store.asHost(ctx, hostID, listingID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM app.listing_calendar_feeds WHERE id = $1 AND listing_id = $2`, feedID, listingID)
		if err != nil {
			return fmt.Errorf("remove calendar: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrFeedNotFound
		}
		return nil
	})
}

func (store *PostgresCalendarStore) ReplaceBlocks(ctx context.Context, due DueFeed, blocks []Range, syncedAt time.Time) error {
	return db.WithUserTx(ctx, store.DB, due.HostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE app.listing_calendar_feeds
			SET last_attempt_at = $2, last_synced_at = $2, last_error = NULL
			WHERE id = $1`, due.ID, syncedAt)
		if err != nil {
			return fmt.Errorf("mark calendar synced: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.listing_calendar_blocks WHERE feed_id = $1`, due.ID); err != nil {
			return fmt.Errorf("clear blocked dates: %w", err)
		}
		return insertBlocks(ctx, tx, due, blocks)
	})
}

func (store *PostgresCalendarStore) RecordFailure(ctx context.Context, due DueFeed, reason string, attemptedAt time.Time) error {
	return db.WithUserTx(ctx, store.DB, due.HostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listing_calendar_feeds SET last_attempt_at = $2, last_error = $3 WHERE id = $1`, due.ID, attemptedAt, reason)
		return err
	})
}

func (store *PostgresCalendarStore) ClaimDue(ctx context.Context, notAttemptedSince time.Time, limit int) ([]DueFeed, error) {
	due := []DueFeed{}
	rows, err := store.DB.Query(ctx, `SELECT id, listing_id, host_id, url FROM app.claim_due_calendar_feeds($1, $2)`, notAttemptedSince, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var feed DueFeed
		if err := rows.Scan(&feed.ID, &feed.ListingID, &feed.HostID, &feed.URL); err != nil {
			return nil, err
		}
		due = append(due, feed)
	}
	return due, rows.Err()
}

func (store *PostgresCalendarStore) Export(ctx context.Context, token string, earliestCheckout time.Time) ([]Reservation, error) {
	reservations := []Reservation{}
	found := false
	err := db.WithPublicTx(ctx, store.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT booking_id, starts_on, ends_on, created_at FROM app.calendar_export($1, $2)`, token, earliestCheckout)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			found = true
			var id *uuid.UUID
			var startsOn, endsOn, createdAt *time.Time
			if err := rows.Scan(&id, &startsOn, &endsOn, &createdAt); err != nil {
				return err
			}
			if id != nil {
				reservations = append(reservations, Reservation{ID: *id, StartsOn: *startsOn, EndsOn: *endsOn, CreatedAt: *createdAt})
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("export calendar: %w", err)
	}
	if !found {
		return nil, ErrCalendarUnknown
	}
	return reservations, nil
}
