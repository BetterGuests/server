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

type Inquiry struct {
	ID           string
	SessionToken string
}

type StartResult struct {
	InquiryID    string `json:"inquiry_id"`
	SessionToken string `json:"session_token"`
	Status       Status `json:"status"`
}

type WebhookEvent struct {
	Name       string
	InquiryID  string
	AccountID  uuid.UUID
	OccurredAt time.Time
}

type InquiryCreator interface {
	CreateInquiry(ctx context.Context, templateID, referenceID string) (Inquiry, error)
}

type Store interface {
	HasCompleted(ctx context.Context, accountID uuid.UUID) (bool, error)
	Create(ctx context.Context, verification Verification) error
	ApplyStatus(ctx context.Context, accountID uuid.UUID, inquiryID string, status Status, occurredAt time.Time) error
}

type StatusResponse struct {
	Status   string `json:"status"`
	Verified bool   `json:"verified"`
}

type StatusReader interface {
	LatestStatus(ctx context.Context, accountID uuid.UUID) (Status, bool, error)
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
