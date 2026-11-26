package proofs

import (
	"context"
	"log/slog"
	"time"
)

const (
	retentionInterval = 6 * time.Hour
	retentionBatch    = 100
)

type FileRetentionService struct {
	store FileRetentionStore
	files FileRemover
}

func NewFileRetentionService(store FileRetentionStore, files FileRemover) *FileRetentionService {
	return &FileRetentionService{store: store, files: files}
}

func (service *FileRetentionService) RunOnce(ctx context.Context) (int, error) {
	due, err := service.store.FilesDueForDeletion(ctx, retentionBatch)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, file := range due {
		if err := service.files.Remove(ctx, file.ObjectKey); err != nil {
			slog.Error("document deletion failed and will retry", "proof_id", file.ProofID, "error", err)
			continue
		}
		if err := service.store.MarkFileDeleted(ctx, file.ProofID); err != nil {
			slog.Error("document deletion not recorded and will retry", "proof_id", file.ProofID, "error", err)
			continue
		}
		deleted++
	}
	return deleted, nil
}

func (service *FileRetentionService) endExpiredAuthority(ctx context.Context) {
	expired, err := service.store.ExpireAuthority(ctx, time.Now())
	if err != nil {
		slog.Error("expired listing permissions were not withdrawn", "error", err)
		return
	}
	if expired > 0 {
		slog.Info("expired listing permissions withdrawn", "withdrawn", expired)
	}
}

func (service *FileRetentionService) runCycle(ctx context.Context) {
	service.endExpiredAuthority(ctx)
	if service.files == nil {
		return
	}
	for ctx.Err() == nil {
		deleted, err := service.RunOnce(ctx)
		if err != nil {
			slog.Error("document deletion cycle stopped", "error", err)
			return
		}
		if deleted > 0 {
			slog.Info("stored ownership documents deleted", "deleted", deleted)
		}
		if deleted < retentionBatch {
			return
		}
	}
}

func (service *FileRetentionService) Run(ctx context.Context) {
	service.runCycle(ctx)
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.runCycle(ctx)
		}
	}
}
