package availability

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type recordingStore struct {
	earliestCheckout time.Time
	limit            int
}

func (store *recordingStore) BookedRanges(_ context.Context, _ uuid.UUID, earliestCheckout time.Time, limit int) ([]BookedRange, error) {
	store.earliestCheckout = earliestCheckout
	store.limit = limit
	return nil, nil
}

func TestStaysStillRunningWestOfUTCAreIncluded(t *testing.T) {
	store := &recordingStore{}
	service := NewAvailabilityService(store)
	service.now = func() time.Time { return time.Date(2026, 12, 10, 3, 0, 0, 0, time.UTC) }

	if _, err := service.BookedRanges(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}

	if got := store.earliestCheckout.Format(time.DateOnly); got != "2026-12-09" {
		t.Fatalf("expected the earliest checkout to be 2026-12-09, got %s", got)
	}
	if store.limit != maxBookedRanges {
		t.Fatalf("expected the range limit %d, got %d", maxBookedRanges, store.limit)
	}
}
