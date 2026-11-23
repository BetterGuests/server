package compliance

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	MinimumAge = 18

	ConsentBiometric = "biometric"

	CodeAgeConfirmationRequired = "age_confirmation_required"
	CodeUnderage                = "underage"
)

type Consent struct {
	ID            uuid.UUID
	AccountID     uuid.UUID
	Kind          string
	PolicyVersion string
	GrantedAt     time.Time
}

type Status struct {
	AdultConfirmed   bool `json:"adult_confirmed"`
	BiometricConsent bool `json:"biometric_consent"`
}

type Store interface {
	SaveAge(ctx context.Context, accountID uuid.UUID, dateOfBirth time.Time) error
	HasAge(ctx context.Context, accountID uuid.UUID) (bool, error)
	SaveConsent(ctx context.Context, consent Consent) error
	HasConsent(ctx context.Context, accountID uuid.UUID, kind string) (bool, error)
}

type AgeConfirmer interface {
	ConfirmAge(ctx context.Context, accountID uuid.UUID, dateOfBirth string) error
	IsAdult(ctx context.Context, accountID uuid.UUID) (bool, error)
}

type ConsentRecorder interface {
	GrantConsent(ctx context.Context, accountID uuid.UUID, kind, policyVersion string) error
	HasConsent(ctx context.Context, accountID uuid.UUID, kind string) (bool, error)
}

type StatusReader interface {
	Status(ctx context.Context, accountID uuid.UUID) (Status, error)
}
