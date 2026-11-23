package verification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type completedStub struct {
	completed CompletedVerification
	found     bool
	err       error
}

func (stub completedStub) LatestCompleted(context.Context, uuid.UUID) (CompletedVerification, bool, error) {
	return stub.completed, stub.found, stub.err
}

func TestCurrentVerificationID(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	id := uuid.New()
	tests := []struct {
		name      string
		stub      completedStub
		wantOK    bool
		wantError bool
	}{
		{"never verified", completedStub{}, false, false},
		{"verified yesterday", completedStub{completed: CompletedVerification{ID: id, CompletedAt: now.Add(-24 * time.Hour)}, found: true}, true, false},
		{"verified just inside the window", completedStub{completed: CompletedVerification{ID: id, CompletedAt: now.Add(-ReverifyAfter + time.Hour)}, found: true}, true, false},
		{"verified just outside the window", completedStub{completed: CompletedVerification{ID: id, CompletedAt: now.Add(-ReverifyAfter - time.Hour)}, found: true}, false, false},
		{"store failure", completedStub{err: errors.New("boom")}, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier := NewCurrentVerifier(test.stub)
			verifier.now = func() time.Time { return now }
			got, ok, err := verifier.CurrentVerificationID(context.Background(), uuid.New())
			if (err != nil) != test.wantError || ok != test.wantOK {
				t.Fatalf("ok = %v err = %v, want ok %v error %v", ok, err, test.wantOK, test.wantError)
			}
			if test.wantOK && got != id {
				t.Fatalf("id = %s, want %s", got, id)
			}
		})
	}
}
