package verification

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	LookupInterval     = 10 * time.Second
	OpenInquiryReuse   = 20 * time.Hour
	lookupGateCapacity = 1024
)

var inquiryStatusEvents = map[string]string{
	"completed":    EventCompleted,
	"approved":     EventApproved,
	"failed":       EventFailed,
	"declined":     EventDeclined,
	"expired":      EventExpired,
	"needs_review": EventMarkedForReview,
}

var openInquiryStatuses = map[string]bool{"created": true, "pending": true}

type InquiryReader interface {
	GetInquiry(ctx context.Context, inquiryID string) (InquirySnapshot, error)
}

type InquiryResumer interface {
	ResumeInquiry(ctx context.Context, inquiryID string) (Inquiry, error)
}

type OpenInquiryFinder interface {
	LatestOpenInquiry(ctx context.Context, accountID uuid.UUID, since time.Time) (string, bool, error)
}

type lookupGate struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (gate *lookupGate) allow(inquiryID string, now time.Time) bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.last == nil {
		gate.last = map[string]time.Time{}
	}
	if previous, seen := gate.last[inquiryID]; seen && now.Sub(previous) < LookupInterval {
		return false
	}
	if len(gate.last) >= lookupGateCapacity {
		for id, at := range gate.last {
			if now.Sub(at) >= LookupInterval {
				delete(gate.last, id)
			}
		}
	}
	gate.last[inquiryID] = now
	return true
}

func (service *VerificationService) Reconcile(ctx context.Context, accountID uuid.UUID, inquiryID string) error {
	reader, ok := service.inquiries.(InquiryReader)
	if !ok || !service.lookups.allow(inquiryID, service.now()) {
		return nil
	}
	snapshot, err := reader.GetInquiry(ctx, inquiryID)
	if err != nil {
		return fmt.Errorf("look up inquiry %s: %w", inquiryID, err)
	}
	_, err = service.applySnapshot(ctx, accountID, snapshot)
	return err
}

func (service *VerificationService) applySnapshot(ctx context.Context, accountID uuid.UUID, snapshot InquirySnapshot) (bool, error) {
	if snapshot.ReferenceID != accountID.String() {
		return false, fmt.Errorf("%w: inquiry %s belongs to another reference", ErrInquiryNotFound, snapshot.ID)
	}
	name, settled := inquiryStatusEvents[snapshot.Status]
	if !settled {
		return false, nil
	}
	occurredAt := snapshot.ChangedAt
	if occurredAt.IsZero() {
		occurredAt = service.now().UTC()
	}
	event := WebhookEvent{Name: name, InquiryID: snapshot.ID, AccountID: accountID, OccurredAt: occurredAt}
	if eventStatuses[name] == StatusCompleted {
		event.IDName, event.BirthDate = passedGovernmentID(snapshot.Included)
	}
	if err := service.apply(ctx, event); err != nil {
		return false, err
	}
	slog.Info("Applied ID check status from Persona lookup", "inquiry_id", snapshot.ID, "status", snapshot.Status)
	return true, nil
}

func (service *VerificationService) openInquiry(ctx context.Context, accountID uuid.UUID) string {
	finder, findable := service.store.(OpenInquiryFinder)
	reader, readable := service.inquiries.(InquiryReader)
	_, resumable := service.inquiries.(InquiryResumer)
	if !findable || !readable || !resumable {
		return ""
	}
	inquiryID, found, err := finder.LatestOpenInquiry(ctx, accountID, service.now().Add(-OpenInquiryReuse))
	if err != nil {
		slog.Warn("Find open ID check", "error", err)
		return ""
	}
	if !found {
		return ""
	}
	snapshot, err := reader.GetInquiry(ctx, inquiryID)
	if err != nil {
		slog.Warn("Look up open ID check", "inquiry_id", inquiryID, "error", err)
		return ""
	}
	if openInquiryStatuses[snapshot.Status] {
		if snapshot.ReferenceID != accountID.String() {
			return ""
		}
		return inquiryID
	}
	if _, err := service.applySnapshot(ctx, accountID, snapshot); err != nil {
		slog.Warn("Apply open ID check status", "inquiry_id", inquiryID, "error", err)
	}
	return ""
}

func (service *VerificationService) resume(ctx context.Context, inquiryID string) (StartResult, bool) {
	resumer, ok := service.inquiries.(InquiryResumer)
	if inquiryID == "" || !ok {
		return StartResult{}, false
	}
	inquiry, err := resumer.ResumeInquiry(ctx, inquiryID)
	if err != nil {
		slog.Warn("Resume open ID check", "inquiry_id", inquiryID, "error", err)
		return StartResult{}, false
	}
	return StartResult{InquiryID: inquiryID, SessionToken: inquiry.SessionToken, Status: StatusCreated}, true
}
