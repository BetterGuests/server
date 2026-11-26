package verification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusCreated     Status = "created"
	StatusPending     Status = "pending"
	StatusNeedsReview Status = "needs_review"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusDeclined    Status = "declined"
	StatusExpired     Status = "expired"
)

const StatusStale = "stale"

const (
	EventCompleted       = "inquiry.completed"
	EventFailed          = "inquiry.failed"
	EventDeclined        = "inquiry.declined"
	EventExpired         = "inquiry.expired"
	EventMarkedForReview = "inquiry.marked-for-review"
	EventApproved        = "inquiry.approved"
)

type Config struct {
	APIKey        string
	TemplateID    string
	WebhookSecret string
	BaseURL       string

	PropertyTemplateID string
	FingerprintKey     string
}

type ReviewState string

const (
	ReviewPending  ReviewState = "pending"
	ReviewCleared  ReviewState = "cleared"
	ReviewRejected ReviewState = "rejected"
)

type ReviewReason string

const (
	ReasonNameChanged        ReviewReason = "name_changed"
	ReasonNameUnconfirmed    ReviewReason = "name_unconfirmed"
	ReasonUnderAge           ReviewReason = "under_age"
	ReasonDuplicateIdentity  ReviewReason = "duplicate_identity"
	ReasonPreviouslyDeclined ReviewReason = "previously_declined"
)

type Completion struct {
	AccountID   uuid.UUID
	InquiryID   string
	OccurredAt  time.Time
	Name        IDName
	Fingerprint []byte
	UnderAge    bool
}

type History struct {
	PreviousName      IDName
	HasEarlierPass    bool
	HasApprovedProof  bool
	DuplicateIdentity bool
	HasDecline        bool
}

type Judge func(History) []ReviewReason

type Attempt struct {
	InquiryID   string
	Status      Status
	Review      ReviewState
	Revoked     bool
	CompletedAt time.Time
}

type Verification struct {
	ID               uuid.UUID `json:"id"`
	AccountID        uuid.UUID `json:"account_id"`
	PersonaInquiryID string    `json:"persona_inquiry_id"`
	Status           Status    `json:"status"`
	LastEventAt      time.Time `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type IDName struct {
	First  string
	Middle string
	Last   string
	Suffix string
}

type Inquiry struct {
	ID           string
	SessionToken string
	OneTimeLink  string
}

type StartResult struct {
	InquiryID    string `json:"inquiry_id"`
	SessionToken string `json:"session_token"`
	Status       Status `json:"status"`
	OneTimeLink  string `json:"one_time_link,omitempty"`
}

type WebhookEvent struct {
	Name       string
	InquiryID  string
	AccountID  uuid.UUID
	OccurredAt time.Time
	IDName     IDName
	BirthDate  string
}

type InquiryCreator interface {
	CreateInquiry(ctx context.Context, templateID, referenceID string) (Inquiry, error)
}

type HandoffInquiryCreator interface {
	CreateHandoffInquiry(ctx context.Context, templateID, referenceID string) (Inquiry, error)
}

type HandoffStarter interface {
	StartHandoff(ctx context.Context, accountID uuid.UUID, replace bool) (StartResult, error)
}

type Store interface {
	HasCompleted(ctx context.Context, accountID uuid.UUID) (bool, error)
	Create(ctx context.Context, verification Verification) error
	ApplyStatus(ctx context.Context, accountID uuid.UUID, inquiryID string, status Status, occurredAt time.Time) error
	SyncIDName(ctx context.Context, accountID uuid.UUID, inquiryID string, name IDName) error
	Complete(ctx context.Context, completion Completion, judge Judge) ([]ReviewReason, error)
}

type StatusResponse struct {
	Status   string `json:"status"`
	Verified bool   `json:"verified"`
}

type StatusReader interface {
	Attempts(ctx context.Context, accountID uuid.UUID) ([]Attempt, error)
}

type Starter interface {
	StartVerification(ctx context.Context, accountID uuid.UUID, replace bool) (StartResult, error)
}

type WebhookProcessor interface {
	HandleWebhook(ctx context.Context, signatureHeader string, body []byte) error
}

type ConsentChecker interface {
	HasConsent(ctx context.Context, accountID uuid.UUID, kind string) (bool, error)
}

type OtherInquiry struct {
	AccountID uuid.UUID
	InquiryID string
	EventName string
	Body      []byte
}

type InquiryClaimer interface {
	Claim(ctx context.Context, inquiry OtherInquiry) (bool, error)
}
