package availability

import (
	"context"
	"fmt"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresAvailabilityStore struct{ DB *pgxpool.Pool }

func NewPostgresAvailabilityStore(pool *pgxpool.Pool) *PostgresAvailabilityStore {
	return &PostgresAvailabilityStore{DB: pool}
}

func (store *PostgresAvailabilityStore) BookedRanges(ctx context.Context, listingID uuid.UUID, earliestCheckout time.Time, limit int) ([]BookedRange, error) {
	ranges := []BookedRange{}
	err := db.WithPublicTx(ctx, store.DB, func(tx pgx.Tx) error {
		var published bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM app.listings WHERE id = $1 AND status = 'published')`,
			listingID,
		).Scan(&published); err != nil {
			return fmt.Errorf("look up listing: %w", err)
		}
		if !published {
			return ErrListingNotFound
		}

		rows, err := tx.Query(ctx,
			`SELECT starts_on, ends_on FROM app.listing_booked_dates($1, $2, $3)`,
			listingID, earliestCheckout, limit)
		if err != nil {
			return fmt.Errorf("query booked dates: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var startsOn, endsOn time.Time
			if err := rows.Scan(&startsOn, &endsOn); err != nil {
				return fmt.Errorf("scan booked dates: %w", err)
			}
			ranges = append(ranges, BookedRange{
				StartsOn: startsOn.Format(time.DateOnly),
				EndsOn:   endsOn.Format(time.DateOnly),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return ranges, nil
}
