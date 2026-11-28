package calendars

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type Feed struct {
	ID            uuid.UUID  `json:"id"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	LastSyncedAt  *time.Time `json:"last_synced_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	BlockedNights int        `json:"blocked_nights"`
}

type Range struct {
	StartsOn time.Time
	EndsOn   time.Time
}

type Block struct {
	FeedName string `json:"feed_name"`
	StartsOn string `json:"starts_on"`
	EndsOn   string `json:"ends_on"`
}

type Overview struct {
	ExportToken string  `json:"export_token"`
	Feeds       []Feed  `json:"feeds"`
	Blocks      []Block `json:"blocks"`
	Conflicts   []Block `json:"conflicts"`
}

type Reservation struct {
	ID        uuid.UUID
	StartsOn  time.Time
	EndsOn    time.Time
	CreatedAt time.Time
}

type DueFeed struct {
	ID        uuid.UUID
	ListingID uuid.UUID
	HostID    uuid.UUID
	URL       string
}

type Store interface {
	Overview(ctx context.Context, hostID, listingID uuid.UUID, newToken string, today time.Time) (Overview, error)
	ReplaceToken(ctx context.Context, hostID, listingID uuid.UUID, token string) error
	AddFeed(ctx context.Context, hostID, listingID uuid.UUID, feed Feed, blocks []Range, syncedAt time.Time) error
	Feed(ctx context.Context, hostID, listingID, feedID uuid.UUID) (Feed, error)
	RemoveFeed(ctx context.Context, hostID, listingID, feedID uuid.UUID) error
	ReplaceBlocks(ctx context.Context, due DueFeed, blocks []Range, syncedAt time.Time) error
	RecordFailure(ctx context.Context, due DueFeed, reason string, attemptedAt time.Time) error
	ClaimDue(ctx context.Context, notAttemptedSince time.Time, limit int) ([]DueFeed, error)
	Export(ctx context.Context, token string, earliestCheckout time.Time) ([]Reservation, error)
}

type Fetcher interface {
	Fetch(ctx context.Context, address string) ([]byte, error)
}

type Manager interface {
	Overview(ctx context.Context, hostID, listingID uuid.UUID) (Overview, error)
	AddFeed(ctx context.Context, hostID, listingID uuid.UUID, name, address string) (Overview, error)
	Refresh(ctx context.Context, hostID, listingID, feedID uuid.UUID) (Overview, error)
	RemoveFeed(ctx context.Context, hostID, listingID, feedID uuid.UUID) (Overview, error)
	ResetLink(ctx context.Context, hostID, listingID uuid.UUID) (Overview, error)
	Export(ctx context.Context, token string) ([]byte, error)
}

const (
	maxFeedsPerListing = 10
	maxNameLength      = 60
	maxAddressLength   = 2000
	maxFeedBytes       = 2 << 20
	maxBlocksPerFeed   = 1000
	horizonYears       = 2
	tokenBytes         = 32
	syncInterval       = 15 * time.Minute
	sweepInterval      = time.Minute
	sweepTimeout       = 5 * time.Minute
	syncBatch          = 25
	parallelSyncs      = 5
	westernmostOffset  = -12 * time.Hour
	ownEventSuffix     = "@better-guests.com"
)

var (
	ErrListingNotFound = errors.New("listing not found")
	ErrFeedNotFound    = errors.New("calendar not found")
	ErrCalendarUnknown = errors.New("calendar link not found")
	ErrInvalidName     = errors.New("name the calendar in 60 characters or fewer")
	ErrInvalidAddress  = errors.New("paste a calendar link that starts with https or webcal")
	ErrOwnCalendar     = errors.New("that is this app's own calendar link, paste the one from the other site")
	ErrDuplicateFeed   = errors.New("that calendar is already connected")
	ErrTooManyFeeds    = errors.New("a listing can connect up to 10 calendars")
	ErrUnreachable     = errors.New("we could not open that calendar link")
	ErrNotACalendar    = errors.New("that link is not a calendar we can read")
	ErrTooLarge        = errors.New("that calendar is too large")
)
