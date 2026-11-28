package calendars

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type CalendarService struct {
	store   Store
	fetcher Fetcher
	now     func() time.Time
}

func NewCalendarService(store Store, fetcher Fetcher) *CalendarService {
	return &CalendarService{store: store, fetcher: fetcher, now: time.Now}
}

func (service *CalendarService) today() time.Time {
	return service.now().UTC().Add(westernmostOffset).Truncate(24 * time.Hour)
}

func (service *CalendarService) Overview(ctx context.Context, hostID, listingID uuid.UUID) (Overview, error) {
	return service.store.Overview(ctx, hostID, listingID, newToken(), service.today())
}

func newToken() string {
	random := make([]byte, tokenBytes)
	_, _ = rand.Read(random)
	return base64.RawURLEncoding.EncodeToString(random)
}

func (service *CalendarService) AddFeed(ctx context.Context, hostID, listingID uuid.UUID, name, address string) (Overview, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLength {
		return Overview{}, ErrInvalidName
	}
	address, err := cleanAddress(address)
	if err != nil {
		return Overview{}, err
	}
	current, err := service.Overview(ctx, hostID, listingID)
	if err != nil {
		return Overview{}, err
	}
	if len(current.Feeds) >= maxFeedsPerListing {
		return Overview{}, ErrTooManyFeeds
	}
	if strings.Contains(address, current.ExportToken) {
		return Overview{}, ErrOwnCalendar
	}
	blocks, err := service.read(ctx, address)
	if err != nil {
		return Overview{}, err
	}
	feed := Feed{ID: uuid.New(), Name: name, URL: address}
	if err := service.store.AddFeed(ctx, hostID, listingID, feed, blocks, service.now().UTC()); err != nil {
		return Overview{}, err
	}
	return service.Overview(ctx, hostID, listingID)
}

func cleanAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if rest, found := strings.CutPrefix(address, "webcal://"); found {
		address = "https://" + rest
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || len(address) > maxAddressLength {
		return "", ErrInvalidAddress
	}
	return address, nil
}

func (service *CalendarService) read(ctx context.Context, address string) ([]Range, error) {
	data, err := service.fetcher.Fetch(ctx, address)
	if err != nil {
		return nil, err
	}
	return parseBusyRanges(data, service.today())
}

func (service *CalendarService) Refresh(ctx context.Context, hostID, listingID, feedID uuid.UUID) (Overview, error) {
	feed, err := service.store.Feed(ctx, hostID, listingID, feedID)
	if err != nil {
		return Overview{}, err
	}
	if err := service.sync(ctx, DueFeed{ID: feed.ID, ListingID: listingID, HostID: hostID, URL: feed.URL}); err != nil {
		return Overview{}, err
	}
	return service.Overview(ctx, hostID, listingID)
}

func (service *CalendarService) sync(ctx context.Context, due DueFeed) error {
	blocks, err := service.read(ctx, due.URL)
	if err != nil {
		if recordErr := service.store.RecordFailure(ctx, due, reasonFor(err), service.now().UTC()); recordErr != nil {
			return fmt.Errorf("record calendar failure: %w", recordErr)
		}
		return nil
	}
	return service.store.ReplaceBlocks(ctx, due, blocks, service.now().UTC())
}

func reasonFor(err error) string {
	for _, known := range []error{ErrNotACalendar, ErrTooLarge, ErrInvalidAddress} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return ErrUnreachable.Error()
}

func (service *CalendarService) RemoveFeed(ctx context.Context, hostID, listingID, feedID uuid.UUID) (Overview, error) {
	if err := service.store.RemoveFeed(ctx, hostID, listingID, feedID); err != nil {
		return Overview{}, err
	}
	return service.Overview(ctx, hostID, listingID)
}

func (service *CalendarService) ResetLink(ctx context.Context, hostID, listingID uuid.UUID) (Overview, error) {
	if _, err := service.Overview(ctx, hostID, listingID); err != nil {
		return Overview{}, err
	}
	if err := service.store.ReplaceToken(ctx, hostID, listingID, newToken()); err != nil {
		return Overview{}, err
	}
	return service.Overview(ctx, hostID, listingID)
}

func (service *CalendarService) Export(ctx context.Context, token string) ([]byte, error) {
	reservations, err := service.store.Export(ctx, token, service.today())
	if err != nil {
		return nil, err
	}
	return writeCalendar(reservations), nil
}

func (service *CalendarService) Run(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		sweep, cancel := context.WithTimeout(ctx, sweepTimeout)
		if err := service.Sweep(sweep); err != nil && ctx.Err() == nil {
			slog.Error("Calendar sync", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (service *CalendarService) Sweep(ctx context.Context) error {
	due, err := service.store.ClaimDue(ctx, service.now().UTC().Add(-syncInterval), syncBatch)
	if err != nil {
		return fmt.Errorf("claim due calendars: %w", err)
	}
	var group errgroup.Group
	group.SetLimit(parallelSyncs)
	for _, feed := range due {
		group.Go(func() error {
			return service.sync(ctx, feed)
		})
	}
	return group.Wait()
}
