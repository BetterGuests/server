package teamevents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotAdmin    = errors.New("not found")
	ErrInvalidKind = errors.New("unknown event kind")
)

const (
	feedLimit    = 150
	feedLookback = 30 * 24 * time.Hour
)

var AllKinds = []Kind{
	BookingRequested, PaymentHeld, PaymentSucceeded, PaymentFailed, BookingCancelled, StayReported,
	ReviewPosted, ListingPublished, ListingPaused, DocumentSent,
}

type TeamEventsService struct {
	store  Store
	admins AdminChecker
	now    func() time.Time
}

func NewTeamEventsService(store Store, admins AdminChecker) *TeamEventsService {
	return &TeamEventsService{store: store, admins: admins, now: time.Now}
}

func (service *TeamEventsService) Feed(ctx context.Context, adminID uuid.UUID, since time.Time, kinds []Kind) ([]Event, error) {
	admin, err := service.admins.IsAdmin(ctx, adminID)
	if err != nil {
		return nil, fmt.Errorf("check admin: %w", err)
	}
	if !admin {
		return nil, ErrNotAdmin
	}
	chosen, err := validKinds(kinds)
	if err != nil {
		return nil, err
	}
	earliest := service.now().Add(-feedLookback)
	if since.Before(earliest) {
		since = earliest
	}
	events, err := service.store.Events(ctx, adminID, Query{Since: since, Kinds: chosen, Limit: feedLimit})
	if err != nil {
		return nil, fmt.Errorf("read team events: %w", err)
	}
	return events, nil
}

func validKinds(kinds []Kind) ([]Kind, error) {
	if len(kinds) == 0 {
		return AllKinds, nil
	}
	known := map[Kind]bool{}
	for _, kind := range AllKinds {
		known[kind] = true
	}
	for _, kind := range kinds {
		if !known[kind] {
			return nil, fmt.Errorf("%w: %s", ErrInvalidKind, kind)
		}
	}
	return kinds, nil
}
