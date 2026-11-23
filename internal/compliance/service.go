package compliance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidDateOfBirth = errors.New("date of birth is invalid")
	ErrUnderage           = errors.New("account holder must be 18 or older")
	ErrInvalidConsent     = errors.New("consent kind or policy version is invalid")
)

const (
	dateLayout           = "2006-01-02"
	earliestBirthYear    = 1900
	maxPolicyVersionSize = 40
)

var consentKinds = map[string]bool{ConsentBiometric: true}

type ComplianceService struct {
	store Store
	now   func() time.Time
	newID func() uuid.UUID
}

func NewComplianceService(store Store) *ComplianceService {
	return &ComplianceService{store: store, now: time.Now, newID: uuid.New}
}

func (service *ComplianceService) ConfirmAge(ctx context.Context, accountID uuid.UUID, dateOfBirth string) error {
	birth, err := time.Parse(dateLayout, dateOfBirth)
	if err != nil || birth.Year() < earliestBirthYear {
		return ErrInvalidDateOfBirth
	}
	now := service.now().UTC()
	if birth.After(now) {
		return ErrInvalidDateOfBirth
	}
	if ageOn(birth, now) < MinimumAge {
		return ErrUnderage
	}
	if err := service.store.SaveAge(ctx, accountID, birth); err != nil {
		return fmt.Errorf("save age confirmation: %w", err)
	}
	return nil
}

func (service *ComplianceService) IsAdult(ctx context.Context, accountID uuid.UUID) (bool, error) {
	adult, err := service.store.HasAge(ctx, accountID)
	if err != nil {
		return false, fmt.Errorf("check age confirmation: %w", err)
	}
	return adult, nil
}

func (service *ComplianceService) GrantConsent(ctx context.Context, accountID uuid.UUID, kind, policyVersion string) error {
	if !consentKinds[kind] || policyVersion == "" || len(policyVersion) > maxPolicyVersionSize {
		return ErrInvalidConsent
	}
	consent := Consent{
		ID:            service.newID(),
		AccountID:     accountID,
		Kind:          kind,
		PolicyVersion: policyVersion,
		GrantedAt:     service.now().UTC(),
	}
	if err := service.store.SaveConsent(ctx, consent); err != nil {
		return fmt.Errorf("save consent: %w", err)
	}
	return nil
}

func (service *ComplianceService) HasConsent(ctx context.Context, accountID uuid.UUID, kind string) (bool, error) {
	granted, err := service.store.HasConsent(ctx, accountID, kind)
	if err != nil {
		return false, fmt.Errorf("check consent: %w", err)
	}
	return granted, nil
}

func (service *ComplianceService) Status(ctx context.Context, accountID uuid.UUID) (Status, error) {
	adult, err := service.IsAdult(ctx, accountID)
	if err != nil {
		return Status{}, err
	}
	biometric, err := service.HasConsent(ctx, accountID, ConsentBiometric)
	if err != nil {
		return Status{}, err
	}
	return Status{AdultConfirmed: adult, BiometricConsent: biometric}, nil
}

func ageOn(birth, today time.Time) int {
	years := today.Year() - birth.Year()
	if today.Month() < birth.Month() || (today.Month() == birth.Month() && today.Day() < birth.Day()) {
		years--
	}
	return years
}
