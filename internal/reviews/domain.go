package reviews

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleGuest Role = "guest"
	RoleHost  Role = "host"
)

const (
	ReviewWindow        = 14 * 24 * time.Hour
	minRating           = 1
	maxRating           = 5
	maxCommentRunes     = 4000
	maxCreateBodyBytes  = 32 << 10
	maxListedReviews    = 100
	maxPendingReviews   = 50
	missingGuestName    = "Guest"
	missingHostName     = "Host"
	statusCompleted     = "completed"
	uniqueViolationCode = "23505"
)

type SummarySubject string

const (
	SummaryOfListing SummarySubject = "listing"
	SummaryOfGuest   SummarySubject = "guest"
)

const (
	MinSummarizedReviews = 3
	MaxSummaryRunes      = 600
	summaryDeadline      = 45 * time.Second
)

type Categories struct {
	Cleanliness   *int `json:"cleanliness,omitempty"`
	Accuracy      *int `json:"accuracy,omitempty"`
	CheckIn       *int `json:"check_in,omitempty"`
	Communication *int `json:"communication,omitempty"`
	Location      *int `json:"location,omitempty"`
	Value         *int `json:"value,omitempty"`
}

type CategoryAverages struct {
	Cleanliness   *float64 `json:"cleanliness,omitempty"`
	Accuracy      *float64 `json:"accuracy,omitempty"`
	CheckIn       *float64 `json:"check_in,omitempty"`
	Communication *float64 `json:"communication,omitempty"`
	Location      *float64 `json:"location,omitempty"`
	Value         *float64 `json:"value,omitempty"`
}

type Review struct {
	ID           uuid.UUID   `json:"id"`
	BookingID    uuid.UUID   `json:"-"`
	ListingID    uuid.UUID   `json:"listing_id"`
	ListingTitle string      `json:"listing_title,omitempty"`
	AuthorRole   Role        `json:"author_role"`
	AuthorName   string      `json:"author_name"`
	Rating       int         `json:"rating"`
	Comment      string      `json:"comment"`
	Categories   *Categories `json:"categories,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	PublishedAt  *time.Time  `json:"published_at,omitempty"`
}

type CreateReviewInput struct {
	BookingID  uuid.UUID   `json:"booking_id"`
	Rating     int         `json:"rating"`
	Comment    string      `json:"comment"`
	Categories *Categories `json:"categories,omitempty"`
}

type Booking struct {
	BookingID    uuid.UUID
	ListingID    uuid.UUID
	ListingTitle string
	GuestID      uuid.UUID
	HostID       uuid.UUID
	GuestName    string
	HostName     string
	Status       string
	CompletedAt  *time.Time
}

type NewReview struct {
	Review
	GuestID uuid.UUID
	HostID  uuid.UUID
}

type BookingReviews struct {
	BookingID      uuid.UUID  `json:"booking_id"`
	ListingTitle   string     `json:"listing_title"`
	Role           Role       `json:"role"`
	SubjectName    string     `json:"subject_name"`
	CanReview      bool       `json:"can_review"`
	WindowClosesAt *time.Time `json:"window_closes_at,omitempty"`
	Mine           *Review    `json:"mine,omitempty"`
	Theirs         *Review    `json:"theirs,omitempty"`
}

type ListingReviews struct {
	Count      int              `json:"count"`
	Average    float64          `json:"average"`
	Categories CategoryAverages `json:"categories"`
	Reviews    []Review         `json:"reviews"`
}

type GuestReviews struct {
	GuestName string   `json:"guest_name,omitempty"`
	Count     int      `json:"count"`
	Average   float64  `json:"average"`
	Reviews   []Review `json:"reviews"`
}

type PendingReview struct {
	BookingID      uuid.UUID `json:"booking_id"`
	ListingTitle   string    `json:"listing_title"`
	Role           Role      `json:"role"`
	SubjectName    string    `json:"subject_name"`
	WindowClosesAt time.Time `json:"window_closes_at"`
}

type ReviewSummary struct {
	Summary     string     `json:"summary,omitempty"`
	ReviewCount int        `json:"review_count"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

type CachedSummary struct {
	Fingerprint string
	ReviewCount int
	Summary     string
	UpdatedAt   time.Time
	CheckedAt   time.Time
}

type SummaryState struct {
	Fingerprint string
	ReviewCount int
	Cached      *CachedSummary
}

type SummaryStore interface {
	SummaryState(ctx context.Context, actorID uuid.UUID, subject SummarySubject, subjectID uuid.UUID) (SummaryState, error)
	SaveSummary(ctx context.Context, actorID uuid.UUID, subject SummarySubject, subjectID uuid.UUID, summary CachedSummary) error
}

type SummaryRequest struct {
	Subject      SummarySubject
	Reviews      []Review
	Current      string
	CurrentSince *time.Time
}

type SummaryAnswer struct {
	Summary       string
	StillAccurate bool
}

type Summarizer interface {
	Summarize(ctx context.Context, request SummaryRequest) (SummaryAnswer, error)
}

type SummaryReader interface {
	ListingSummary(ctx context.Context, listingID uuid.UUID) (ReviewSummary, error)
	BookingGuestSummary(ctx context.Context, hostID, bookingID uuid.UUID) (ReviewSummary, error)
}

type Store interface {
	Booking(ctx context.Context, actorID, bookingID uuid.UUID) (Booking, error)
	Insert(ctx context.Context, actorID uuid.UUID, review NewReview) error
	ForBooking(ctx context.Context, actorID, bookingID uuid.UUID) ([]Review, error)
	ForListing(ctx context.Context, listingID uuid.UUID, limit int) (ListingReviews, error)
	AboutGuest(ctx context.Context, actorID, guestID uuid.UUID, limit int) (GuestReviews, error)
	Pending(ctx context.Context, actorID uuid.UUID, completedAfter time.Time, limit int) ([]PendingReview, error)
	PublishDue(ctx context.Context) (int, error)
}

type Nudger interface {
	Nudge()
}

type Creator interface {
	CreateReview(ctx context.Context, actorID uuid.UUID, input CreateReviewInput) (Review, error)
}

type Reader interface {
	ForBooking(ctx context.Context, actorID, bookingID uuid.UUID) (BookingReviews, error)
	ForListing(ctx context.Context, listingID uuid.UUID) (ListingReviews, error)
	AboutMe(ctx context.Context, actorID uuid.UUID) (GuestReviews, error)
	AboutBookingGuest(ctx context.Context, hostID, bookingID uuid.UUID) (GuestReviews, error)
	Pending(ctx context.Context, actorID uuid.UUID) ([]PendingReview, error)
}
