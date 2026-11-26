package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	authTimeout           = 10 * time.Second
	pingInterval          = 30 * time.Second
	pingTimeout           = 10 * time.Second
	writeTimeout          = 10 * time.Second
	maxConnectionAge      = time.Hour
	maxInboundFrameBytes  = 24 << 10
	receiveTimeout        = 5 * time.Second
	inboundWindow         = 5 * time.Second
	maxInboundPerWindow   = 60
	outboundBuffer        = 32
	maxConnectionsPerUser = 8
	maxTopicBytes         = 120
	focusNoticeTimeout    = 5 * time.Second
)

const (
	FrameAuth  = "auth"
	FrameReady = "ready"
	FrameFocus = "focus"
)

var (
	ErrTooManyConnections = errors.New("too many open connections for this account")
	errBadAuthFrame       = errors.New("first frame must be an auth frame with a token")
)

type Frame struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type inboundFrame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type inboundBudget struct {
	windowStart time.Time
	used        int
}

type authFrame struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type Connection struct {
	userID   uuid.UUID
	outbound chan []byte
	focus    string
}

type focusFrame struct {
	Topic string `json:"topic"`
}

type focusChange struct {
	userID  uuid.UUID
	topic   string
	present bool
}

type FocusListener interface {
	FocusChanged(ctx context.Context, userID uuid.UUID, topic string, present bool)
}

type Receiver interface {
	Receive(ctx context.Context, userID uuid.UUID, data json.RawMessage) error
}

type Authenticator interface {
	Authenticate(ctx context.Context, authorization string) (uuid.UUID, error)
}
