package waitlist

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

const (
	maxEmailBytes = 254
	maxBodyBytes  = 1 << 10
)

var (
	ErrInvalidEmail   = errors.New("enter a valid email address")
	ErrInvalidCountry = errors.New("invalid country code")
)

type Signup struct {
	ID      uuid.UUID
	Email   string
	Country string
}

type Store interface {
	Add(ctx context.Context, signup Signup) error
}

type Joiner interface {
	Join(ctx context.Context, email, country string) error
}
