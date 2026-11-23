package verification

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const ReverifyAfter = 365 * 24 * time.Hour

type CompletedVerification struct {
	ID          uuid.UUID
	CompletedAt time.Time
}

type CompletedReader interface {
	LatestCompleted(ctx context.Context, accountID uuid.UUID) (CompletedVerification, bool, error)
}

type CurrentChecker interface {
	CurrentVerificationID(ctx context.Context, accountID uuid.UUID) (uuid.UUID, bool, error)
}

type CurrentVerifier struct {
	reader CompletedReader
	now    func() time.Time
}

func NewCurrentVerifier(reader CompletedReader) *CurrentVerifier {
	return &CurrentVerifier{reader: reader, now: time.Now}
}

func (verifier *CurrentVerifier) CurrentVerificationID(ctx context.Context, accountID uuid.UUID) (uuid.UUID, bool, error) {
	completed, found, err := verifier.reader.LatestCompleted(ctx, accountID)
	if err != nil || !found {
		return uuid.Nil, false, err
	}
	if verifier.now().Sub(completed.CompletedAt) > ReverifyAfter {
		return uuid.Nil, false, nil
	}
	return completed.ID, true, nil
}
