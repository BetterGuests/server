package cancellations

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

type CancelledBy string

const (
	ByGuest   CancelledBy = "guest"
	ByHost    CancelledBy = "host"
	ByExpired CancelledBy = "expired"
)

type RefundSource string

const (
	SourceCancellation RefundSource = "cancellation"
	SourceReport       RefundSource = "report"
)

type RefundStatus string

const (
	RefundNone      RefundStatus = "none"
	RefundPending   RefundStatus = "pending"
	RefundSucceeded RefundStatus = "succeeded"
)

type ReportStatus string

const (
	ReportOpen     ReportStatus = "open"
	ReportResolved ReportStatus = "resolved"
)

const (
	FullRefundNotice        = 5 * 24 * time.Hour
	FreeAfterApproval       = 4 * time.Hour
	ReportWindow            = 24 * time.Hour
	HostCancellationLimit   = 3
	HostCancellationPeriod  = 365 * 24 * time.Hour
	lateRefundDivisor       = 2
	maxReportRunes          = 4000
	maxNoteRunes            = 1000
	maxListedReports        = 100
	maxListedListings       = 200
	maxPopularListings      = 10
	maxPopularCities        = 200
	maxPopularCityRunes     = 120
	PopularityPeriod        = 90 * 24 * time.Hour
	coverPhotoURLLifetime   = time.Hour
	maxPauseReasonRunes     = 200
	listingEventKind        = "listing"
	jobBatchSize            = 100
	bookingEventKind        = "booking"
	statusConfirmed         = "confirmed"
	statusCompleted         = "completed"
	statusCancelled         = "cancelled"
	paymentSucceeded        = "succeeded"
	suspensionReason        = "Three host cancellations in 12 months"
	uniqueViolationCode     = "23505"
	maxRequestBodyBytes     = 32 << 10
	refundMetadataSuffix    = "_booking_id"
	refundIdempotencyPrefix = "booking-refund-"
)

type Booking struct {
	BookingID       uuid.UUID
	ListingID       uuid.UUID
	ListingTitle    string
	GuestID         uuid.UUID
	HostID          uuid.UUID
	Status          string
	StartsOn        time.Time
	CheckInTime     string
	TimeZone        string
	ApprovedAt      *time.Time
	AmountCents     int
	GuestFeeCents   int
	Currency        string
	PaymentStatus   string
	PaymentIntentID string
	Cancellation    *Cancellation
	Report          *Report
}

type Cancellation struct {
	BookingID       uuid.UUID    `json:"-"`
	GuestID         uuid.UUID    `json:"-"`
	HostID          uuid.UUID    `json:"-"`
	ListingID       uuid.UUID    `json:"-"`
	CancelledBy     CancelledBy  `json:"cancelled_by"`
	AmountCents     int          `json:"-"`
	RefundCents     int          `json:"refund_cents"`
	PaymentIntentID string       `json:"-"`
	RefundStatus    RefundStatus `json:"refund_status"`
}

type Report struct {
	BookingID       uuid.UUID    `json:"booking_id"`
	GuestID         uuid.UUID    `json:"-"`
	HostID          uuid.UUID    `json:"-"`
	ListingTitle    string       `json:"listing_title"`
	Details         string       `json:"details"`
	AmountCents     int          `json:"amount_cents"`
	PaymentIntentID string       `json:"-"`
	Status          ReportStatus `json:"status"`
	RefundCents     int          `json:"refund_cents"`
	RefundStatus    RefundStatus `json:"refund_status"`
	Note            string       `json:"note,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
}

type Options struct {
	BookingID      uuid.UUID     `json:"booking_id"`
	Role           Role          `json:"role"`
	CanCancel      bool          `json:"can_cancel"`
	AmountCents    int           `json:"amount_cents"`
	RefundCents    int           `json:"refund_cents"`
	Currency       string        `json:"currency"`
	FreeUntil      *time.Time    `json:"free_until,omitempty"`
	CanReport      bool          `json:"can_report"`
	ReportClosesAt *time.Time    `json:"report_closes_at,omitempty"`
	Cancellation   *Cancellation `json:"cancellation,omitempty"`
	Report         *Report       `json:"report,omitempty"`
}

type Decision struct {
	RefundCents int    `json:"refund_cents"`
	Note        string `json:"note"`
}

type AdminListing struct {
	ListingID         uuid.UUID  `json:"listing_id"`
	Title             string     `json:"title"`
	HostName          string     `json:"host_name"`
	City              string     `json:"city"`
	Status            string     `json:"status"`
	Paused            bool       `json:"paused"`
	Reason            string     `json:"reason,omitempty"`
	PausedAt          *time.Time `json:"paused_at,omitempty"`
	HostCancellations int        `json:"host_cancellations"`
	CoverPhotoKey     string     `json:"-"`
	CoverPhotoURL     string     `json:"cover_photo_url,omitempty"`
}

type PopularListing struct {
	ListingID     uuid.UUID `json:"listing_id"`
	Title         string    `json:"title"`
	HostName      string    `json:"host_name"`
	City          string    `json:"city"`
	Bookings      int       `json:"bookings"`
	Reviews       int       `json:"reviews"`
	Rating        float64   `json:"rating"`
	CoverPhotoKey string    `json:"-"`
	CoverPhotoURL string    `json:"cover_photo_url,omitempty"`
}

type ListingEvent struct {
	ListingID uuid.UUID `json:"listing_id"`
	Paused    bool      `json:"paused"`
}

type PendingRefund struct {
	BookingID       uuid.UUID
	GuestID         uuid.UUID
	Source          RefundSource
	PaymentIntentID string
	AmountCents     int
}

type ExpiredRequest struct {
	BookingID uuid.UUID
	GuestID   uuid.UUID
}

type StrandedHold struct {
	BookingID uuid.UUID
	GuestID   uuid.UUID
}

type BookingEvent struct {
	BookingID uuid.UUID `json:"booking_id"`
	Status    string    `json:"status"`
}

type Store interface {
	Booking(ctx context.Context, actorID, bookingID uuid.UUID) (Booking, error)
	Cancel(ctx context.Context, actorID uuid.UUID, cancellation Cancellation, hostCancellationsSince time.Time) error
	ExpiredRequests(ctx context.Context, limit int) ([]ExpiredRequest, error)
	Expire(ctx context.Context, request ExpiredRequest) error
	StrandedHolds(ctx context.Context, limit int) ([]StrandedHold, error)
	CreateReport(ctx context.Context, report Report) error
	IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error)
	OpenReports(ctx context.Context, adminID uuid.UUID, limit int) ([]Report, error)
	Resolve(ctx context.Context, adminID, bookingID uuid.UUID, decision Decision, at time.Time) (Report, error)
	PendingRefunds(ctx context.Context, limit int) ([]PendingRefund, error)
	MarkRefunded(ctx context.Context, refund PendingRefund, refundID string) error
	AdminListings(ctx context.Context, adminID uuid.UUID, cancellationsSince time.Time, limit int) ([]AdminListing, error)
	PopularListings(ctx context.Context, adminID uuid.UUID, bookedSince time.Time, cities []string, limit int) ([]PopularListing, error)
	PauseListing(ctx context.Context, adminID, listingID uuid.UUID, reason string) (uuid.UUID, error)
	UnpauseListing(ctx context.Context, adminID, listingID uuid.UUID, at time.Time) (uuid.UUID, error)
}

type Gateway interface {
	Refund(ctx context.Context, refund PendingRefund) (string, error)
}

type HoldReleaser interface {
	Release(ctx context.Context, actorID, bookingID uuid.UUID) error
}

type PhotoSigner interface {
	PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error)
}

type Publisher interface {
	Publish(userID uuid.UUID, kind string, payload any)
}

type Canceller interface {
	Options(ctx context.Context, actorID, bookingID uuid.UUID) (Options, error)
	Cancel(ctx context.Context, actorID, bookingID uuid.UUID, expectedRefundCents int) (Options, error)
	Report(ctx context.Context, guestID, bookingID uuid.UUID, details string) (Options, error)
}

type ListingDesk interface {
	AdminListings(ctx context.Context, adminID uuid.UUID) ([]AdminListing, error)
	PopularListings(ctx context.Context, adminID uuid.UUID, cities []string) ([]PopularListing, error)
	PauseListing(ctx context.Context, adminID, listingID uuid.UUID, reason string) error
	UnpauseListing(ctx context.Context, adminID, listingID uuid.UUID) error
}

type ReportDesk interface {
	OpenReports(ctx context.Context, adminID uuid.UUID) ([]Report, error)
	Resolve(ctx context.Context, adminID, bookingID uuid.UUID, decision Decision) (Report, error)
}
