package verification

import (
	"context"
	"log/slog"
	"time"
)

const (
	PauseGrace    = 30 * 24 * time.Hour
	pauseInterval = time.Hour
	pauseTimeout  = 2 * time.Minute
)

type PauseApplier interface {
	ApplyVerificationPauses(ctx context.Context, validDays, graceDays int) error
}

type VerificationPauseService struct {
	store PauseApplier
}

func NewVerificationPauseService(store PauseApplier) *VerificationPauseService {
	return &VerificationPauseService{store: store}
}

func wholeDays(span time.Duration) int {
	return int(span / (24 * time.Hour))
}

func (service *VerificationPauseService) RunOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pauseTimeout)
	defer cancel()
	return service.store.ApplyVerificationPauses(ctx, wholeDays(ReverifyAfter), wholeDays(PauseGrace))
}

func (service *VerificationPauseService) runCycle(ctx context.Context) {
	if err := service.RunOnce(ctx); err != nil && ctx.Err() == nil {
		slog.Error("verification pauses not applied and will retry", "error", err)
	}
}

func (service *VerificationPauseService) Run(ctx context.Context) {
	service.runCycle(ctx)
	ticker := time.NewTicker(pauseInterval)
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
