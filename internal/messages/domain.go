package messages

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleGuest  Role = "guest"
	RoleHost   Role = "host"
	RoleSystem Role = "system"
)

const (
	MessageEventKind        = "message"
	MessageDeletedEventKind = "message_deleted"
	ReactionsEventKind      = "reactions"
	TypingEventKind         = "typing"
	PresenceEventKind       = "presence"
)

const conversationTopicPrefix = "conversation:"

const (
	typingThrottle      = 100 * time.Millisecond
	typingMembershipTTL = 30 * time.Second
)

const maxTrackedTypists = 10_000

const (
	maxBodyRunes         = 4000
	defaultPageSize      = 50
	maxPageSize          = 100
	previewRunes         = 200
	maxListedThreads     = 100
	missingGuestName     = "Guest"
	missingHostName      = "Host"
	missingTargetMessage = "booking_id or listing_id is required"
)

type Message struct {
	ID             uuid.UUID  `json:"id"`
	ConversationID uuid.UUID  `json:"conversation_id"`
	SenderID       uuid.UUID  `json:"-"`
	SenderRole     Role       `json:"sender_role"`
	Body           string     `json:"body"`
	CreatedAt      time.Time  `json:"created_at"`
	BookingID      *uuid.UUID `json:"booking_id,omitempty"`
	ReplyTo        *Reply     `json:"reply_to,omitempty"`
	Reactions      []Reaction `json:"reactions"`
}

type Reply struct {
	ID         uuid.UUID `json:"id"`
	SenderRole Role      `json:"sender_role"`
	Body       string    `json:"body"`
}

type Reaction struct {
	Emoji string `json:"emoji"`
	Role  Role   `json:"role"`
}

type Draft struct {
	ID        uuid.UUID
	Body      string
	ReplyToID uuid.UUID
}

type Participants struct {
	GuestID uuid.UUID
	HostID  uuid.UUID
}

type Summary struct {
	ID                    uuid.UUID  `json:"id"`
	ListingID             uuid.UUID  `json:"listing_id"`
	ListingTitle          string     `json:"listing_title"`
	Role                  Role       `json:"role"`
	CounterpartyName      string     `json:"counterparty_name"`
	CounterpartyAvatarURL string     `json:"counterparty_avatar_url,omitempty"`
	LastMessageAt         *time.Time `json:"last_message_at,omitempty"`
	LastMessagePreview    string     `json:"last_message_preview,omitempty"`
	UnreadCount           int        `json:"unread_count"`
	CounterpartyReplyMins *int       `json:"counterparty_reply_minutes,omitempty"`
	Participants          `json:"-"`
}

type OpenTarget struct {
	BookingID uuid.UUID
	ListingID uuid.UUID
}

type MessagePage struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"has_more"`
}

type MessageEvent struct {
	ConversationID uuid.UUID `json:"conversation_id"`
	Message        Message   `json:"message"`
}

type MessageDeletedEvent struct {
	ConversationID uuid.UUID `json:"conversation_id"`
	MessageID      uuid.UUID `json:"message_id"`
}

type ReactionsEvent struct {
	ConversationID uuid.UUID  `json:"conversation_id"`
	MessageID      uuid.UUID  `json:"message_id"`
	Reactions      []Reaction `json:"reactions"`
}

type TypingEvent struct {
	ConversationID uuid.UUID `json:"conversation_id"`
	Role           Role      `json:"role"`
	Draft          string    `json:"draft,omitempty"`
	Idle           bool      `json:"idle,omitempty"`
}

type PresenceEvent struct {
	ConversationID uuid.UUID `json:"conversation_id"`
	Role           Role      `json:"role"`
	Here           bool      `json:"here"`
}

type TypingUpdate struct {
	Draft string
	Idle  bool
}

type typist struct {
	actorID        uuid.UUID
	conversationID uuid.UUID
}

type typingSeen struct {
	lastSent     time.Time
	participants Participants
	checkedAt    time.Time
}

type Store interface {
	Open(ctx context.Context, actorID, newID uuid.UUID, target OpenTarget, now time.Time) (uuid.UUID, error)
	Get(ctx context.Context, actorID, conversationID uuid.UUID) (Summary, error)
	List(ctx context.Context, actorID uuid.UUID) ([]Summary, error)
	Messages(ctx context.Context, actorID, conversationID uuid.UUID, before *time.Time, limit int) ([]Message, error)
	Insert(ctx context.Context, actorID, conversationID uuid.UUID, draft Draft, now time.Time) (Message, Participants, bool, error)
	Delete(ctx context.Context, actorID, conversationID, messageID uuid.UUID) (Participants, error)
	ToggleReaction(ctx context.Context, actorID, conversationID, messageID, reactionID uuid.UUID, emoji string, now time.Time) ([]Reaction, Participants, error)
	Participants(ctx context.Context, actorID, conversationID uuid.UUID) (Participants, error)
	MarkRead(ctx context.Context, actorID, conversationID uuid.UUID, now time.Time) error
}

type Publisher interface {
	Publish(userID uuid.UUID, kind string, payload any)
}

type FocusTracker interface {
	PublishFocused(userID uuid.UUID, topic, kind string, payload any)
	Focused(userID uuid.UUID, topic string) bool
}
