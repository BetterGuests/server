package waitlist

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresWaitlistStore struct{ DB *pgxpool.Pool }

func NewPostgresWaitlistStore(pool *pgxpool.Pool) *PostgresWaitlistStore {
	return &PostgresWaitlistStore{DB: pool}
}

func (store *PostgresWaitlistStore) Add(ctx context.Context, signup Signup) error {
	_, err := store.DB.Exec(ctx,
		`INSERT INTO app.waitlist_signups (id, email, country) VALUES ($1, $2, NULLIF($3, '')) ON CONFLICT DO NOTHING`,
		signup.ID, signup.Email, signup.Country)
	if err != nil {
		return fmt.Errorf("insert waitlist signup: %w", err)
	}
	return nil
}
