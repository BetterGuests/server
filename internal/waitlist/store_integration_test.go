package waitlist

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresWaitlistStoreIsWriteOnlyAndIgnoresDuplicates(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run waitlist database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store := NewPostgresWaitlistStore(pool)
	email := uuid.NewString() + "@example.test"
	for range 2 {
		if err := store.Add(ctx, Signup{ID: uuid.New(), Email: email}); err != nil {
			t.Fatalf("add signup: %v", err)
		}
	}

	var found int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app.waitlist_signups").Scan(&found); err == nil {
		t.Fatalf("the app role could read %d waitlist rows", found)
	}
}
