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

const MessageEventKind = "message"

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

type Store interface {
	Open(ctx context.Context, actorID, newID uuid.UUID, target OpenTarget, now time.Time) (uuid.UUID, error)
	Get(ctx context.Context, actorID, conversationID uuid.UUID) (Summary, error)
	List(ctx context.Context, actorID uuid.UUID) ([]Summary, error)
	Messages(ctx context.Context, actorID, conversationID uuid.UUID, before *time.Time, limit int) ([]Message, error)
	Insert(ctx context.Context, actorID, conversationID, messageID uuid.UUID, body string, now time.Time) (Message, Participants, error)
	MarkRead(ctx context.Context, actorID, conversationID uuid.UUID, now time.Time) error
}

type Publisher interface {
	Publish(userID uuid.UUID, kind string, payload any)
}
