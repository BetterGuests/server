package realtime

import (
	"context"
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
	maxInboundFrameBytes  = 8 << 10
	outboundBuffer        = 32
	maxConnectionsPerUser = 8
)

const (
	FrameAuth  = "auth"
	FrameReady = "ready"
)

var (
	ErrTooManyConnections = errors.New("too many open connections for this account")
	errBadAuthFrame       = errors.New("first frame must be an auth frame with a token")
)

type Frame struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type authFrame struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type Connection struct {
	userID   uuid.UUID
	outbound chan []byte
}

type Authenticator interface {
	Authenticate(ctx context.Context, authorization string) (uuid.UUID, error)
}
