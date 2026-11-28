package cashback

import (
	"context"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/chores"
	"github.com/google/uuid"
)

type TaskStatus string

const (
	TaskTodo     TaskStatus = "todo"
	TaskChecking TaskStatus = "checking"
	TaskApproved TaskStatus = "approved"
	TaskRetake   TaskStatus = "retake"
	TaskRejected TaskStatus = "rejected"
)

type RefundStatus string

const (
	RefundPending   RefundStatus = "pending"
	RefundSucceeded RefundStatus = "succeeded"
	RefundFailed    RefundStatus = "failed"
)

type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

const (
	CapPercent      = 25
	CheckoutHourUTC = 18
	PhotoWindow     = 24 * time.Hour
	SettlementDelay = 72 * time.Hour
)

type PlanView struct {
	Groups chores.Plan `json:"groups"`
}

type PlanStore interface {
	ListingPlan(ctx context.Context, hostID, listingID uuid.UUID) ([]byte, error)
	SaveListingPlan(ctx context.Context, hostID, listingID uuid.UUID, plan []byte) error
}

type PlanEditor interface {
	ListingPlan(ctx context.Context, hostID, listingID uuid.UUID) (PlanView, error)
	SaveListingPlan(ctx context.Context, hostID, listingID uuid.UUID, plan chores.Plan) (PlanView, error)
	RequestExampleUpload(ctx context.Context, hostID, listingID uuid.UUID, contentType string, sizeBytes int64) (UploadTicket, error)
}

type Submission struct {
	ID         uuid.UUID  `json:"id"`
	BookingID  uuid.UUID  `json:"booking_id"`
	GuestID    uuid.UUID  `json:"guest_id"`
	TaskKey    string     `json:"task_key"`
	PhotoURL   string     `json:"photo_url"`
	Status     TaskStatus `json:"status"`
	Note       string     `json:"note,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type Refund struct {
	ID             uuid.UUID    `json:"id"`
	BookingID      uuid.UUID    `json:"booking_id"`
	GuestID        uuid.UUID    `json:"guest_id"`
	BundleKey      string       `json:"bundle_key"`
	AmountCents    int          `json:"amount_cents"`
	Status         RefundStatus `json:"status"`
	StripeRefundID string       `json:"-"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

type BookingState struct {
	BookingID       uuid.UUID
	BookingStatus   string
	PaymentIntentID string
	PaymentStatus   string
	AmountCents     int
	EndsOn          time.Time
}

type ReviewTarget struct {
	Submission Submission
	HostID     uuid.UUID
	EndsOn     time.Time
}

type SettlementState struct {
	Status        string
	RefundedCents int
}

type Schedule struct {
	PhotosOpenAt  time.Time `json:"photos_open_at"`
	PhotosCloseAt time.Time `json:"photos_close_at"`
	SettlesAt     time.Time `json:"settles_at"`
}

type TaskView struct {
	Instructions string     `json:"instructions,omitempty"`
	ExampleURL   string     `json:"example_url,omitempty"`
	SubmissionID *uuid.UUID `json:"submission_id,omitempty"`
	Key          string     `json:"key"`
	Label        string     `json:"label"`
	Hint         string     `json:"hint"`
	Status       TaskStatus `json:"status"`
	Note         string     `json:"note,omitempty"`
	PhotoURL     string     `json:"photo_url,omitempty"`
}

type BundleView struct {
	Key          string       `json:"key"`
	Label        string       `json:"label"`
	AmountCents  int          `json:"amount_cents"`
	Status       TaskStatus   `json:"status"`
	RefundStatus RefundStatus `json:"refund_status,omitempty"`
	Tasks        []TaskView   `json:"tasks"`
}

type TripView struct {
	BookingID   uuid.UUID    `json:"booking_id"`
	Verified    bool         `json:"verified"`
	CanEarn     bool         `json:"can_earn"`
	EarnedCents int          `json:"earned_cents"`
	Bundles     []BundleView `json:"bundles"`
	Money
}

type Money struct {
	Schedule
	CapCents         int    `json:"cap_cents"`
	ExpectedCents    int    `json:"expected_cents"`
	SettlementStatus string `json:"settlement_status,omitempty"`
}

type PendingSubmission struct {
	Submission   Submission `json:"submission"`
	TaskLabel    string     `json:"task_label"`
	ListingTitle string     `json:"listing_title"`
	GuestName    string     `json:"guest_name"`
	StartsOn     string     `json:"starts_on"`
	EndsOn       string     `json:"ends_on"`
}

type WaitingPhoto struct {
	Submission   Submission
	ListingTitle string
	GuestName    string
	StartsOn     time.Time
	EndsOn       time.Time
}

type HostTripView struct {
	BookingID    uuid.UUID    `json:"booking_id"`
	GuestID      uuid.UUID    `json:"guest_id"`
	ListingTitle string       `json:"listing_title"`
	EarnedCents  int          `json:"earned_cents"`
	Bundles      []BundleView `json:"bundles"`
	Money
}

type StorageConfig struct {
	Endpoint        string
	Bucket          string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	UseSSL          bool
	PathStyle       bool
}

type ObjectInfo struct {
	Size        int64
	ContentType string
}

type UploadTicket struct {
	UploadURL   string `json:"upload_url"`
	PhotoKey    string `json:"photo_key"`
	ContentType string `json:"content_type"`
	ExpiresIn   int    `json:"expires_in_seconds"`
	MaxBytes    int64  `json:"max_bytes"`
}

type ObjectStore interface {
	PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error)
	PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
}

type UploadRequester interface {
	RequestUpload(ctx context.Context, guestID, bookingID uuid.UUID, taskKey, contentType string, sizeBytes int64) (UploadTicket, error)
}

type VerificationChecker interface {
	HasCompleted(ctx context.Context, accountID uuid.UUID) (bool, error)
}

type Store interface {
	BookingState(ctx context.Context, actingID, bookingID uuid.UUID) (BookingState, error)
	SaveSubmission(ctx context.Context, submission Submission) (Submission, error)
	ListSubmissions(ctx context.Context, actingID, bookingID uuid.UUID) ([]Submission, error)
	ListRefunds(ctx context.Context, actingID, bookingID uuid.UUID) ([]Refund, error)
	HostBooking(ctx context.Context, hostID, bookingID uuid.UUID) (HostBooking, error)
	ListPendingForHost(ctx context.Context, hostID uuid.UUID, earliestEndsOn time.Time) ([]WaitingPhoto, error)
	LoadReviewTarget(ctx context.Context, actingID, submissionID uuid.UUID) (ReviewTarget, error)
	ApplyReview(ctx context.Context, actingID, submissionID uuid.UUID, status TaskStatus, note string, reviewedAt time.Time) error
	Settlement(ctx context.Context, actingID, bookingID uuid.UUID) (SettlementState, error)
	BookingPlan(ctx context.Context, actingID, bookingID uuid.UUID) ([]byte, error)
	PlanStore
}

type HostBooking struct {
	BookingID    uuid.UUID
	GuestID      uuid.UUID
	ListingTitle string
	AmountCents  int
	EndsOn       time.Time
}

type HostReader interface {
	HostTrip(ctx context.Context, hostID, bookingID uuid.UUID) (HostTripView, error)
	PendingForHost(ctx context.Context, hostID uuid.UUID) ([]PendingSubmission, error)
}

type TripReader interface {
	Trip(ctx context.Context, guestID, bookingID uuid.UUID) (TripView, error)
}

type PhotoSubmitter interface {
	SubmitPhoto(ctx context.Context, guestID, bookingID uuid.UUID, taskKey, photoURL string) (Submission, error)
}

type Reviewer interface {
	Review(ctx context.Context, hostID, submissionID uuid.UUID, decision Decision, note string) (Submission, error)
}
