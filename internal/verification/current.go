package verification

import (
	"context"
	"slices"
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

func summarize(attempts []Attempt, inquiryID string, now time.Time) (StatusResponse, bool) {
	response := StatusResponse{Status: "none", Verified: slices.ContainsFunc(attempts, func(attempt Attempt) bool {
		return effectiveStatus(attempt, now) == string(StatusCompleted)
	})}
	if inquiryID != "" {
		index := slices.IndexFunc(attempts, func(attempt Attempt) bool { return attempt.InquiryID == inquiryID })
		if index < 0 {
			return StatusResponse{}, false
		}
		response.Status = effectiveStatus(attempts[index], now)
		return response, true
	}
	if len(attempts) == 0 {
		return response, true
	}
	latest := max(slices.IndexFunc(attempts, wentSomewhere), 0)
	response.Status = effectiveStatus(attempts[latest], now)
	return response, true
}

func wentSomewhere(attempt Attempt) bool {
	return attempt.Status != StatusCreated && attempt.Status != StatusExpired
}

func effectiveStatus(attempt Attempt, now time.Time) string {
	switch {
	case attempt.Status != StatusCompleted:
		return string(attempt.Status)
	case attempt.Revoked || attempt.Review == ReviewRejected:
		return string(StatusDeclined)
	case attempt.Review == ReviewPending:
		return string(StatusNeedsReview)
	case now.Sub(attempt.CompletedAt) > ReverifyAfter:
		return StatusStale
	}
	return string(StatusCompleted)
}
