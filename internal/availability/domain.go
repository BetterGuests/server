package availability

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	maxBookedRanges   = 500
	westernmostOffset = -12 * time.Hour
)

type BookedRange struct {
	StartsOn string `json:"starts_on"`
	EndsOn   string `json:"ends_on"`
}

type Store interface {
	BookedRanges(ctx context.Context, listingID uuid.UUID, earliestCheckout time.Time, limit int) ([]BookedRange, error)
}

type Reader interface {
	BookedRanges(ctx context.Context, listingID uuid.UUID) ([]BookedRange, error)
}
