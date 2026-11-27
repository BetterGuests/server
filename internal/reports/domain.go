package reports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Target string

const (
	TargetListing Target = "listing"
	TargetHost    Target = "host"
	TargetGuest   Target = "guest"
)

type Reason string

const (
	ReasonInaccurate         Reason = "inaccurate"
	ReasonScam               Reason = "scam"
	ReasonUnsafe             Reason = "unsafe"
	ReasonOffensive          Reason = "offensive"
	ReasonOffPlatformPayment Reason = "off_platform_payment"
	ReasonHarassment         Reason = "harassment"
	ReasonDiscrimination     Reason = "discrimination"
	ReasonPropertyDamage     Reason = "property_damage"
	ReasonOther              Reason = "other"
)

type Status string

const (
	StatusOpen      Status = "open"
	StatusActioned  Status = "actioned"
	StatusDismissed Status = "dismissed"
)

const (
	MaxDetailsRunes     = 4000
	MaxNoteRunes        = 1000
	listLimit           = 100
	maxRequestBodyBytes = 32 << 10
)

type Submission struct {
	Target         Target    `json:"target"`
	Reason         Reason    `json:"reason"`
	Details        string    `json:"details"`
	ListingID      uuid.UUID `json:"listing_id"`
	BookingID      uuid.UUID `json:"booking_id"`
	ConversationID uuid.UUID `json:"conversation_id"`
}

type Receipt struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type Report struct {
	ID               uuid.UUID  `json:"id"`
	Target           Target     `json:"target"`
	Reason           Reason     `json:"reason"`
	Details          string     `json:"details"`
	Status           Status     `json:"status"`
	ReporterID       uuid.UUID  `json:"reporter_id"`
	ReporterName     string     `json:"reporter_name"`
	SubjectID        uuid.UUID  `json:"subject_id"`
	SubjectName      string     `json:"subject_name"`
	SubjectReports   int        `json:"subject_reports"`
	ListingID        *uuid.UUID `json:"listing_id,omitempty"`
	ListingTitle     string     `json:"listing_title"`
	BookingID        *uuid.UUID `json:"booking_id,omitempty"`
	FromConversation bool       `json:"from_conversation"`
	Note             string     `json:"note,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
}

type Decision struct {
	Outcome Status `json:"outcome"`
	Note    string `json:"note"`
}

type Store interface {
	Create(ctx context.Context, reporterID, reportID uuid.UUID, submission Submission, at time.Time) error
	List(ctx context.Context, adminID uuid.UUID, open bool, limit int) ([]Report, error)
	Resolve(ctx context.Context, adminID, reportID uuid.UUID, decision Decision, at time.Time) error
}

type AdminChecker interface {
	IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error)
}

type Filer interface {
	File(ctx context.Context, reporterID uuid.UUID, submission Submission) (Receipt, error)
}

type Desk interface {
	List(ctx context.Context, adminID uuid.UUID, open bool) ([]Report, error)
	Resolve(ctx context.Context, adminID, reportID uuid.UUID, decision Decision) error
}
