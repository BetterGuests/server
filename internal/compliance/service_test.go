package compliance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubStore struct {
	savedBirth   time.Time
	ageSaved     bool
	consents     []Consent
	hasAge       bool
	hasConsent   bool
	saveAgeError error
}

func (store *stubStore) SaveAge(_ context.Context, _ uuid.UUID, dateOfBirth time.Time) error {
	store.savedBirth = dateOfBirth
	store.ageSaved = store.saveAgeError == nil
	return store.saveAgeError
}

func (store *stubStore) HasAge(context.Context, uuid.UUID) (bool, error) { return store.hasAge, nil }

func (store *stubStore) SaveConsent(_ context.Context, consent Consent) error {
	store.consents = append(store.consents, consent)
	return nil
}

func (store *stubStore) HasConsent(context.Context, uuid.UUID, string) (bool, error) {
	return store.hasConsent, nil
}

func newTestService(store *stubStore) *ComplianceService {
	service := NewComplianceService(store)
	service.now = func() time.Time { return time.Date(2026, time.November, 22, 12, 0, 0, 0, time.UTC) }
	return service
}

func TestConfirmAge(t *testing.T) {
	tests := []struct {
		name      string
		birth     string
		wantErr   error
		wantSaved bool
	}{
		{name: "adult", birth: "1990-05-01", wantSaved: true},
		{name: "turns eighteen today", birth: "2008-11-22", wantSaved: true},
		{name: "turns eighteen tomorrow", birth: "2008-11-23", wantErr: ErrUnderage},
		{name: "child", birth: "2015-06-15", wantErr: ErrUnderage},
		{name: "future date", birth: "2030-01-01", wantErr: ErrInvalidDateOfBirth},
		{name: "before 1900", birth: "1850-01-01", wantErr: ErrInvalidDateOfBirth},
		{name: "not a date", birth: "yesterday", wantErr: ErrInvalidDateOfBirth},
		{name: "impossible day", birth: "1990-02-30", wantErr: ErrInvalidDateOfBirth},
		{name: "empty", birth: "", wantErr: ErrInvalidDateOfBirth},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &stubStore{}
			err := newTestService(store).ConfirmAge(context.Background(), uuid.New(), test.birth)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if store.ageSaved != test.wantSaved {
				t.Fatalf("saved = %v, want %v", store.ageSaved, test.wantSaved)
			}
		})
	}
}

func TestUnderageDateOfBirthIsNeverStored(t *testing.T) {
	store := &stubStore{}
	_ = newTestService(store).ConfirmAge(context.Background(), uuid.New(), "2015-06-15")
	if !store.savedBirth.IsZero() {
		t.Fatalf("stored %v for a minor", store.savedBirth)
	}
}

func TestGrantConsentValidatesInput(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		version string
		wantErr error
	}{
		{name: "valid", kind: ConsentBiometric, version: "2026-11-22"},
		{name: "unknown kind", kind: "marketing", version: "1", wantErr: ErrInvalidConsent},
		{name: "empty version", kind: ConsentBiometric, version: "", wantErr: ErrInvalidConsent},
		{name: "oversized version", kind: ConsentBiometric, version: "12345678901234567890123456789012345678901", wantErr: ErrInvalidConsent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &stubStore{}
			err := newTestService(store).GrantConsent(context.Background(), uuid.New(), test.kind, test.version)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if wantRecords := map[bool]int{true: 0, false: 1}[test.wantErr != nil]; len(store.consents) != wantRecords {
				t.Fatalf("consent records = %d, want %d", len(store.consents), wantRecords)
			}
		})
	}
}
