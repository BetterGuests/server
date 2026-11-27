package notifications

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

type Kind string

const (
	KindBookingRequested   Kind = "booking_requested"
	KindBookingConfirmed   Kind = "booking_confirmed"
	KindBookingDeclined    Kind = "booking_declined"
	KindBookingCancelled   Kind = "booking_cancelled"
	KindArrivalGuideNeeded Kind = "arrival_guide_needed"
	KindArrivalReady       Kind = "arrival_ready"
	KindCheckInOpen        Kind = "check_in_open"
	KindGuestCheckedIn     Kind = "guest_checked_in"
	KindCheckOutReminder   Kind = "check_out_reminder"
	KindGuestCheckedOut    Kind = "guest_checked_out"
	KindReviewInvite       Kind = "review_invite"
	KindReviewPublished    Kind = "review_published"
	KindBookingExpired     Kind = "booking_expired"
	KindProblemReported    Kind = "problem_reported"
	KindProblemResolved    Kind = "problem_resolved"
)

const (
	NotificationEventKind      = "notification"
	RoleTeam              Role = "team"
	messageEventKind           = "message"
	systemSenderRole           = "system"
)

const (
	statusRequested   = "requested"
	statusDeclined    = "declined"
	statusConfirmed   = "confirmed"
	statusCancelled   = "cancelled"
	statusCompleted   = "completed"
	paymentAuthorized = "authorized"
	cancelledByGuest  = "guest"
	cancelledByHost   = "host"
	cancelledByTeam   = "team"
	cancelledExpired  = "expired"
	reportOpen        = "open"
	reportResolved    = "resolved"
)

const (
	checkOutReminderLead = 18 * time.Hour
	sweepInterval        = time.Minute
	sweepTimeout         = 45 * time.Second
	journeyPageSize      = 200
	maxListed            = 50
	emailBatchSize       = 50
	reportsPath          = "/admin/stay-reports"
	userReportsPath      = "/admin/user-reports"
	adminListingsPath    = "/admin/listings"
	hostingPath          = "/hosting"
	verifyPath           = "/verify"
	identityReviewsPath  = "/admin/identity-reviews"
	kindIDExpiring       = "id_expiring"
	kindIdentityInReview = "identity_in_review"
	kindIdentityReview   = "identity_review"
	tripPathPrefix       = "/trips/"
	kindReportFiled      = "report_filed"
	kindUserReportFiled  = "user_report_filed"
	kindListingPaused    = "listing_paused"
	kindListingUnpaused  = "listing_unpaused"
	missingGuestName     = "Your guest"
	dayLayout            = "Jan 2"
)

type Notification struct {
	ID        uuid.UUID  `json:"id"`
	BookingID uuid.UUID  `json:"booking_id"`
	Role      Role       `json:"role"`
	Kind      Kind       `json:"kind"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
}

type Inbox struct {
	Notifications []Notification `json:"notifications"`
	UnreadCount   int            `json:"unread_count"`
}

type Journey struct {
	BookingID            uuid.UUID
	ListingTitle         string
	Status               string
	PaymentStatus        string
	GuestID              uuid.UUID
	HostID               uuid.UUID
	GuestName            string
	StartsOn             time.Time
	EndsOn               time.Time
	CheckInTime          string
	CheckOutTime         string
	TimeZone             string
	CompletedAt          *time.Time
	CheckedInAt          *time.Time
	CheckedOutAt         *time.Time
	HasArrivalGuide      bool
	GuestReviewed        bool
	HostReviewed         bool
	GuestReviewPublished bool
	HostReviewPublished  bool
	SentNotifications    []string
	PostedEvents         []string
	CancelledBy          string
	CancelRefundCents    int
	ReportStatus         string
	ReportRefundCents    int
}

type Alert struct {
	Kind      Kind
	Recipient uuid.UUID
	Title     string
	Body      string
}

type ThreadNote struct {
	Kind Kind
	Body string
}

type Plan struct {
	Alerts []Alert
	Notes  []ThreadNote
}

type planner struct {
	journey Journey
	plan    Plan
}

type Posted struct {
	ConversationID uuid.UUID
	GuestID        uuid.UUID
	HostID         uuid.UUID
	CreatedAt      time.Time
}

type AlertEvent struct {
	BookingID uuid.UUID `json:"booking_id"`
	Kind      Kind      `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Role      Role      `json:"role,omitempty"`
}

type ThreadMessage struct {
	ID             uuid.UUID `json:"id"`
	ConversationID uuid.UUID `json:"conversation_id"`
	SenderRole     string    `json:"sender_role"`
	Body           string    `json:"body"`
	BookingID      uuid.UUID `json:"booking_id"`
	CreatedAt      time.Time `json:"created_at"`
}

type ThreadEvent struct {
	ConversationID uuid.UUID     `json:"conversation_id"`
	Message        ThreadMessage `json:"message"`
}

type InboxStore interface {
	List(ctx context.Context, actorID uuid.UUID, limit int) (Inbox, error)
	MarkRead(ctx context.Context, actorID, notificationID uuid.UUID, at time.Time) error
	MarkAllRead(ctx context.Context, actorID uuid.UUID, at time.Time) error
}

type JourneyStore interface {
	Journeys(ctx context.Context, after uuid.UUID, limit int) ([]Journey, error)
	DeliverAlert(ctx context.Context, bookingID, notificationID uuid.UUID, alert Alert) (bool, error)
	PostNote(ctx context.Context, bookingID, messageID, conversationID uuid.UUID, note ThreadNote) (Posted, bool, error)
}

type StayCompleter interface {
	CompleteFinished(ctx context.Context) (int, error)
}

type ReviewPublisher interface {
	PublishDue(ctx context.Context) (int, error)
}

type TeamAlert struct {
	Recipient uuid.UUID
	BookingID uuid.UUID
	Kind      Kind
	Title     string
	Body      string
	ForTeam   bool
}

type PendingEmail struct {
	NotificationID uuid.UUID
	AccountID      uuid.UUID
	BookingID      uuid.UUID
	Kind           Kind
	Title          string
	Body           string
	ForTeam        bool
	TeamEmail      string
}

type Email struct {
	To             string
	Subject        string
	Text           string
	IdempotencyKey string
}

type TeamAlertQueue interface {
	QueueTeamAlerts(ctx context.Context) ([]TeamAlert, error)
}

type EmailQueue interface {
	AwaitingEmail(ctx context.Context, limit int) ([]PendingEmail, error)
	RecordEmailAttempt(ctx context.Context, notificationID uuid.UUID, delivered bool) error
}

type EmailSender interface {
	Send(ctx context.Context, email Email) error
}

type EmailAddresses interface {
	VerifiedEmail(ctx context.Context, accountID uuid.UUID) (string, error)
}

type OutboundMail interface {
	SendDue(ctx context.Context) (int, error)
}

type AutomaticMessages interface {
	SendDue(ctx context.Context) (int, error)
}

type BookingUpkeep interface {
	ExpireRequests(ctx context.Context) (int, error)
	ReleaseStrandedHolds(ctx context.Context) (int, error)
	SendPendingRefunds(ctx context.Context) (int, error)
}

type Publisher interface {
	Publish(userID uuid.UUID, kind string, payload any)
}

type InboxReader interface {
	List(ctx context.Context, actorID uuid.UUID) (Inbox, error)
	MarkRead(ctx context.Context, actorID, notificationID uuid.UUID) error
	MarkAllRead(ctx context.Context, actorID uuid.UUID) error
}
