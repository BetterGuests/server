package availability

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrListingNotFound = errors.New("listing not found")

type AvailabilityService struct {
	store Store
	now   func() time.Time
}

func NewAvailabilityService(store Store) *AvailabilityService {
	return &AvailabilityService{store: store, now: time.Now}
}

func (service *AvailabilityService) BookedRanges(ctx context.Context, listingID uuid.UUID) ([]BookedRange, error) {
	earliestCheckout := service.now().UTC().Add(westernmostOffset)
	return service.store.BookedRanges(ctx, listingID, earliestCheckout, maxBookedRanges)
}
