package automessages

import (
	"context"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/google/uuid"
)

type Trigger string

const (
	TriggerBookingConfirmed Trigger = "booking_confirmed"
	TriggerBeforeCheckIn    Trigger = "before_check_in"
	TriggerCheckInDay       Trigger = "check_in_day"
	TriggerCheckOutEve      Trigger = "check_out_eve"
	TriggerCheckOutDay      Trigger = "check_out_day"
)

const (
	FillGuestName            = "{guest_name}"
	FillListingTitle         = "{listing_title}"
	FillCheckInTime          = "{check_in_time}"
	FillCheckOutTime         = "{check_out_time}"
	FillEntryInstructions    = "{entry_instructions}"
	FillEntryCode            = "{entry_code}"
	FillWifiName             = "{wifi_name}"
	FillWifiPassword         = "{wifi_password}"
	FillCheckoutInstructions = "{checkout_instructions}"
)

const PrivateNotice = "Your host sent private stay details. They show here from 48 hours before check-in until checkout."

const emptyPrivateDetails = "Your host has not added these details yet. Ask them here."

const (
	checkInDayHour    = "08:00"
	checkOutEveHour   = "18:00"
	checkOutDayLead   = 3 * time.Hour
	maxNameRunes      = 60
	maxBodyRunes      = 2000
	MaxPerListing     = 10
	candidatePageSize = 200
	clockLayout       = "3:04 PM"
	timeOfDayLayout   = "15:04"
	missingGuestName  = "there"
	statusConfirmed   = "confirmed"
	paymentSucceeded  = "succeeded"
)

type Input struct {
	Name    string  `json:"name"`
	Trigger Trigger `json:"trigger"`
	Body    string  `json:"body"`
	Private bool    `json:"private"`
	Enabled bool    `json:"enabled"`
}

type AutoMessage struct {
	ID        uuid.UUID `json:"id"`
	ListingID uuid.UUID `json:"listing_id"`
	Input
	UpdatedAt time.Time `json:"updated_at"`
}

type Overview struct {
	ListingTitle string        `json:"listing_title"`
	Messages     []AutoMessage `json:"messages"`
	Suggestions  []Input       `json:"suggestions"`
}

type Candidate struct {
	BookingID       uuid.UUID
	AutoMessageID   uuid.UUID
	Trigger         Trigger
	Body            string
	Private         bool
	QuotesSecret    bool
	HasArrivalGuide bool
	ActiveSince     time.Time
	UpdatedAt       time.Time
	ConfirmedAt     time.Time
	StartsOn        time.Time
	EndsOn          time.Time
	Schedule        stays.Schedule
	GuestName       string
	ListingTitle    string
}

type Outgoing struct {
	MessageID      uuid.UUID
	ConversationID uuid.UUID
	Body           string
	PrivateBody    string
}

type Posted struct {
	ConversationID uuid.UUID
	GuestID        uuid.UUID
	HostID         uuid.UUID
	CreatedAt      time.Time
}

type Sealed struct {
	MessageID     uuid.UUID
	Template      string
	ViewerIsHost  bool
	BookingStatus string
	PaymentStatus string
	StartsOn      time.Time
	EndsOn        time.Time
	Schedule      stays.Schedule
	CheckedOutAt  *time.Time
	GuestName     string
	ListingTitle  string
	Arrival       stays.ArrivalDetails
}

type Store interface {
	Overview(ctx context.Context, hostID, listingID uuid.UUID) (Overview, error)
	Create(ctx context.Context, hostID uuid.UUID, message AutoMessage, at time.Time) error
	Update(ctx context.Context, hostID uuid.UUID, message AutoMessage, at time.Time) error
	Delete(ctx context.Context, hostID, listingID, messageID uuid.UUID) error
}

type Outbox interface {
	Candidates(ctx context.Context, afterBooking, afterMessage uuid.UUID, limit int) ([]Candidate, error)
	Post(ctx context.Context, candidate Candidate, outgoing Outgoing) (Posted, bool, error)
}

type Vault interface {
	Sealed(ctx context.Context, viewerID uuid.UUID, messageIDs []uuid.UUID) ([]Sealed, error)
}

type Publisher interface {
	Publish(userID uuid.UUID, kind string, payload any)
}

type Nudger interface {
	Nudge()
}

type Manager interface {
	Overview(ctx context.Context, hostID, listingID uuid.UUID) (Overview, error)
	Create(ctx context.Context, hostID, listingID uuid.UUID, input Input) (AutoMessage, error)
	Update(ctx context.Context, hostID, listingID, messageID uuid.UUID, input Input) (AutoMessage, error)
	Delete(ctx context.Context, hostID, listingID, messageID uuid.UUID) error
}
