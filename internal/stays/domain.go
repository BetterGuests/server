package stays

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

type Phase string

const (
	PhaseAwaitingConfirmation Phase = "awaiting_confirmation"
	PhaseUpcoming             Phase = "upcoming"
	PhaseArrivalReady         Phase = "arrival_ready"
	PhaseCheckInOpen          Phase = "check_in_open"
	PhaseCheckedIn            Phase = "checked_in"
	PhaseCheckedOut           Phase = "checked_out"
	PhaseCompleted            Phase = "completed"
	PhaseCancelled            Phase = "cancelled"
)

const (
	statusRequested = "requested"
	statusPending   = "pending"
	statusConfirmed = "confirmed"
	statusCompleted = "completed"
)

const (
	ArrivalLeadTime         = 48 * time.Hour
	DefaultCheckInTime      = "16:00"
	DefaultCheckOutTime     = "11:00"
	DefaultTimeZone         = "America/New_York"
	timeOfDayLayout         = "15:04"
	maxInstructionRunes     = 4000
	maxEntryCodeRunes       = 60
	maxWifiRunes            = 100
	maxCheckoutRunes        = 2000
	completionBatchSize     = 200
	completionBatchAttempts = 10
)

type Schedule struct {
	CheckInTime  string `json:"check_in_time"`
	CheckOutTime string `json:"check_out_time"`
	TimeZone     string `json:"time_zone"`
}

type ArrivalDetails struct {
	EntryInstructions    string `json:"entry_instructions"`
	EntryCode            string `json:"entry_code"`
	WifiName             string `json:"wifi_name"`
	WifiPassword         string `json:"wifi_password"`
	CheckoutInstructions string `json:"checkout_instructions"`
}

type ArrivalGuide struct {
	ListingID    uuid.UUID `json:"listing_id"`
	ListingTitle string    `json:"listing_title"`
	Saved        bool      `json:"saved"`
	Schedule
	ArrivalDetails
}

type GuideInput struct {
	Schedule
	ArrivalDetails
}

type Record struct {
	BookingID    uuid.UUID
	ListingID    uuid.UUID
	ListingTitle string
	GuestID      uuid.UUID
	HostID       uuid.UUID
	Status       string
	StartsOn     time.Time
	EndsOn       time.Time
	Schedule     Schedule
	CheckedInAt  *time.Time
	CheckedOutAt *time.Time
	Arrival      *ArrivalDetails
}

type View struct {
	BookingID      uuid.UUID `json:"booking_id"`
	ListingID      uuid.UUID `json:"listing_id"`
	ListingTitle   string    `json:"listing_title"`
	Role           Role      `json:"role"`
	Phase          Phase     `json:"phase"`
	StartsOn       string    `json:"starts_on"`
	EndsOn         string    `json:"ends_on"`
	CheckInAt      time.Time `json:"check_in_at"`
	CheckOutAt     time.Time `json:"check_out_at"`
	ArrivalOpensAt time.Time `json:"arrival_opens_at"`
	Schedule
	CheckedInAt  *time.Time      `json:"checked_in_at,omitempty"`
	CheckedOutAt *time.Time      `json:"checked_out_at,omitempty"`
	CanCheckIn   bool            `json:"can_check_in"`
	CanCheckOut  bool            `json:"can_check_out"`
	Arrival      *ArrivalDetails `json:"arrival,omitempty"`
}

type Store interface {
	Stay(ctx context.Context, actorID, bookingID uuid.UUID) (Record, error)
	CheckIn(ctx context.Context, guestID, bookingID uuid.UUID, at time.Time) error
	CheckOut(ctx context.Context, guestID, bookingID uuid.UUID, at time.Time) error
	CompleteFinished(ctx context.Context, batchSize int) (int, error)
}

type GuideStore interface {
	Guide(ctx context.Context, hostID, listingID uuid.UUID) (ArrivalGuide, error)
	SaveGuide(ctx context.Context, hostID, listingID uuid.UUID, input GuideInput, at time.Time) error
}

type Nudger interface {
	Nudge()
}

type StayManager interface {
	Stay(ctx context.Context, actorID, bookingID uuid.UUID) (View, error)
	CheckIn(ctx context.Context, actorID, bookingID uuid.UUID) (View, error)
	CheckOut(ctx context.Context, actorID, bookingID uuid.UUID) (View, error)
}

type GuideManager interface {
	Guide(ctx context.Context, hostID, listingID uuid.UUID) (ArrivalGuide, error)
	SaveGuide(ctx context.Context, hostID, listingID uuid.UUID, input GuideInput) (ArrivalGuide, error)
}
