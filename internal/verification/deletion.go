package verification

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

type RequestedDeletion struct {
	ID        uuid.UUID
	InquiryID string
	Held      bool
}

type DeletionStore interface {
	RequestDeletion(ctx context.Context, accountID uuid.UUID) ([]RequestedDeletion, error)
	MarkRedacted(ctx context.Context, id uuid.UUID) error
}

type DeletionResult struct {
	Deleted  int `json:"deleted"`
	Waiting  int `json:"waiting"`
	Retrying int `json:"retrying"`
}

type DeletionService struct {
	store    DeletionStore
	redactor InquiryRedactor
}

func NewDeletionService(store DeletionStore, redactor InquiryRedactor) *DeletionService {
	return &DeletionService{store: store, redactor: redactor}
}

func (service *DeletionService) DeleteIDData(ctx context.Context, accountID uuid.UUID) (DeletionResult, error) {
	requested, err := service.store.RequestDeletion(ctx, accountID)
	if err != nil {
		return DeletionResult{}, err
	}
	var result DeletionResult
	for _, item := range requested {
		if item.Held {
			result.Waiting++
			continue
		}
		if err := service.redactor.RedactInquiry(ctx, item.InquiryID); err != nil {
			result.Retrying++
			slog.Error("Requested ID deletion failed and will retry", "verification_id", item.ID, "inquiry_id", item.InquiryID, "error", err)
			continue
		}
		if err := service.store.MarkRedacted(ctx, item.ID); err != nil {
			result.Retrying++
			slog.Error("Requested ID deletion not recorded and will retry", "verification_id", item.ID, "inquiry_id", item.InquiryID, "error", err)
			continue
		}
		result.Deleted++
	}
	return result, nil
}
