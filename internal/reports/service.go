package reports

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalidReport   = errors.New("that report is not valid")
	ErrInvalidDetails  = errors.New("tell us what happened in 4000 characters or fewer")
	ErrOwnReport       = errors.New("you cannot report yourself or your own listing")
	ErrTargetNotFound  = errors.New("we could not find what you are reporting")
	ErrAlreadyReported = errors.New("you already reported this and our team is reviewing it")
	ErrInvalidDecision = errors.New("choose an outcome and keep the note to 1000 characters or fewer")
	ErrReportNotFound  = errors.New("that report is not open")
	ErrNotAdmin        = errors.New("not found")
)

var reasonsByTarget = map[Target][]Reason{
	TargetListing: {ReasonInaccurate, ReasonScam, ReasonUnsafe, ReasonOffensive, ReasonOther},
	TargetHost: {
		ReasonScam, ReasonOffPlatformPayment, ReasonHarassment, ReasonDiscrimination, ReasonUnsafe, ReasonOther,
	},
	TargetGuest: {
		ReasonPropertyDamage, ReasonScam, ReasonOffPlatformPayment, ReasonHarassment, ReasonDiscrimination,
		ReasonUnsafe, ReasonOther,
	},
}

type ReportService struct {
	store  Store
	admins AdminChecker
	now    func() time.Time
	newID  func() uuid.UUID
}

func NewReportService(store Store, admins AdminChecker) *ReportService {
	return &ReportService{store: store, admins: admins, now: time.Now, newID: uuid.New}
}

func (service *ReportService) File(ctx context.Context, reporterID uuid.UUID, submission Submission) (Receipt, error) {
	submission.Details = strings.TrimSpace(submission.Details)
	if err := validate(submission); err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{ID: service.newID(), CreatedAt: service.now().UTC()}
	if err := service.store.Create(ctx, reporterID, receipt.ID, submission, receipt.CreatedAt); err != nil {
		return Receipt{}, fmt.Errorf("file report: %w", err)
	}
	return receipt, nil
}

func (service *ReportService) List(ctx context.Context, adminID uuid.UUID, open bool) ([]Report, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}
	found, err := service.store.List(ctx, adminID, open, listLimit)
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	return found, nil
}

func (service *ReportService) Resolve(ctx context.Context, adminID, reportID uuid.UUID, decision Decision) error {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return err
	}
	decision.Note = strings.TrimSpace(decision.Note)
	closes := decision.Outcome == StatusActioned || decision.Outcome == StatusDismissed
	if !closes || utf8.RuneCountInString(decision.Note) > MaxNoteRunes {
		return ErrInvalidDecision
	}
	if err := service.store.Resolve(ctx, adminID, reportID, decision, service.now().UTC()); err != nil {
		return fmt.Errorf("resolve report: %w", err)
	}
	return nil
}

func (service *ReportService) requireAdmin(ctx context.Context, accountID uuid.UUID) error {
	admin, err := service.admins.IsAdmin(ctx, accountID)
	if err != nil {
		return fmt.Errorf("check admin: %w", err)
	}
	if !admin {
		return ErrNotAdmin
	}
	return nil
}

func validate(submission Submission) error {
	if !slices.Contains(reasonsByTarget[submission.Target], submission.Reason) {
		return ErrInvalidReport
	}
	if submission.Details == "" || utf8.RuneCountInString(submission.Details) > MaxDetailsRunes {
		return ErrInvalidDetails
	}
	if !namesOneSubject(submission) {
		return ErrInvalidReport
	}
	return nil
}

func namesOneSubject(submission Submission) bool {
	hasListing := submission.ListingID != uuid.Nil
	hasBooking := submission.BookingID != uuid.Nil
	hasConversation := submission.ConversationID != uuid.Nil
	switch submission.Target {
	case TargetListing:
		return hasListing && !hasBooking && !hasConversation
	case TargetHost:
		return countTrue(hasListing, hasBooking, hasConversation) == 1
	case TargetGuest:
		return !hasListing && countTrue(hasBooking, hasConversation) == 1
	default:
		return false
	}
}

func countTrue(flags ...bool) int {
	count := 0
	for _, flag := range flags {
		if flag {
			count++
		}
	}
	return count
}
