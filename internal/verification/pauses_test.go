package verification

import (
	"context"
	"errors"
	"testing"
)

type recordedPauses struct {
	validDays, graceDays int
	err                  error
}

func (stub *recordedPauses) ApplyVerificationPauses(_ context.Context, validDays, graceDays int) error {
	stub.validDays, stub.graceDays = validDays, graceDays
	return stub.err
}

func TestPauseJobUsesTheSameWindowAsTheIDCheck(t *testing.T) {
	stub := &recordedPauses{}
	if err := NewVerificationPauseService(stub).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stub.validDays != 365 || stub.graceDays != 30 {
		t.Fatalf("valid = %d grace = %d", stub.validDays, stub.graceDays)
	}
}

func TestPauseJobReportsAFailedRun(t *testing.T) {
	failure := errors.New("statement timeout")
	err := NewVerificationPauseService(&recordedPauses{err: failure}).RunOnce(context.Background())
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}
}
