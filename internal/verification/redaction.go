package verification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultRetentionDays   = 30
	DefaultHoldCapDays     = 1095
	DefaultPurgeInterval   = 6 * time.Hour
	DefaultPurgeBatch      = 50
	maxRetentionDays       = 1095
	maxPurgeBatch          = 500
	minPurgeInterval       = time.Minute
	redactionPauseBetween  = 250 * time.Millisecond
	maxDrainPassesPerCycle = 20
)

type RedactionConfig struct {
	Enabled       bool
	RetentionDays int
	HoldCapDays   int
	Interval      time.Duration
	BatchSize     int
}

type DueVerification struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	InquiryID string
}

type RedactionStore interface {
	ListDueForRedaction(ctx context.Context, retentionDays, holdCapDays, batchSize int) ([]DueVerification, error)
	MarkRedacted(ctx context.Context, id uuid.UUID) error
}

type InquiryRedactor interface {
	RedactInquiry(ctx context.Context, inquiryID string) error
}

type RedactionReport struct {
	Due      int
	Redacted int
	Failed   int
}

type RedactionService struct {
	store    RedactionStore
	redactor InquiryRedactor
	config   RedactionConfig
	pause    time.Duration
}

func RedactionConfigFromEnv() (RedactionConfig, error) {
	return redactionConfigFromLookup(func(name string) string { return strings.TrimSpace(os.Getenv(name)) })
}

func redactionConfigFromLookup(lookup func(string) string) (RedactionConfig, error) {
	config := RedactionConfig{
		Enabled:       true,
		RetentionDays: DefaultRetentionDays,
		HoldCapDays:   DefaultHoldCapDays,
		Interval:      DefaultPurgeInterval,
		BatchSize:     DefaultPurgeBatch,
	}
	if value := lookup("ID_PURGE_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return RedactionConfig{}, fmt.Errorf("ID_PURGE_ENABLED must be true or false")
		}
		config.Enabled = enabled
	}
	var err error
	if config.RetentionDays, err = intSetting(lookup, "ID_RETENTION_DAYS", config.RetentionDays, 1, maxRetentionDays); err != nil {
		return RedactionConfig{}, err
	}
	if config.HoldCapDays, err = intSetting(lookup, "ID_HOLD_CAP_DAYS", config.HoldCapDays, 1, maxRetentionDays); err != nil {
		return RedactionConfig{}, err
	}
	if config.BatchSize, err = intSetting(lookup, "ID_PURGE_BATCH", config.BatchSize, 1, maxPurgeBatch); err != nil {
		return RedactionConfig{}, err
	}
	if value := lookup("ID_PURGE_INTERVAL"); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil || interval < minPurgeInterval {
			return RedactionConfig{}, fmt.Errorf("ID_PURGE_INTERVAL must be a duration of at least %s", minPurgeInterval)
		}
		config.Interval = interval
	}
	if config.HoldCapDays < config.RetentionDays {
		return RedactionConfig{}, errors.New("ID_HOLD_CAP_DAYS must be at least ID_RETENTION_DAYS")
	}
	return config, nil
}

func intSetting(lookup func(string) string, name string, fallback, min, max int) (int, error) {
	value := lookup(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s must be a whole number from %d to %d", name, min, max)
	}
	return parsed, nil
}

func NewRedactionService(store RedactionStore, redactor InquiryRedactor, config RedactionConfig) *RedactionService {
	return &RedactionService{store: store, redactor: redactor, config: config, pause: redactionPauseBetween}
}

func (service *RedactionService) Enabled() bool { return service.config.Enabled }

func (service *RedactionService) RunOnce(ctx context.Context) (RedactionReport, error) {
	due, err := service.store.ListDueForRedaction(ctx, service.config.RetentionDays, service.config.HoldCapDays, service.config.BatchSize)
	if err != nil {
		return RedactionReport{}, err
	}
	report := RedactionReport{Due: len(due)}
	for index, item := range due {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if index > 0 {
			time.Sleep(service.pause)
		}
		if err := service.redactor.RedactInquiry(ctx, item.InquiryID); err != nil {
			report.Failed++
			slog.Error("ID redaction failed", "verification_id", item.ID, "inquiry_id", item.InquiryID, "error", err)
			continue
		}
		if err := service.store.MarkRedacted(ctx, item.ID); err != nil {
			report.Failed++
			slog.Error("ID redaction not recorded", "verification_id", item.ID, "inquiry_id", item.InquiryID, "error", err)
			continue
		}
		report.Redacted++
	}
	return report, nil
}

func (service *RedactionService) RunCycle(ctx context.Context) RedactionReport {
	var total RedactionReport
	for pass := 0; pass < maxDrainPassesPerCycle; pass++ {
		report, err := service.RunOnce(ctx)
		total.Due += report.Due
		total.Redacted += report.Redacted
		total.Failed += report.Failed
		if err != nil {
			slog.Error("ID redaction cycle stopped", "error", err)
			break
		}
		if report.Due < service.config.BatchSize || report.Redacted == 0 {
			break
		}
	}
	if total.Due > 0 {
		slog.Info("ID redaction cycle finished", "due", total.Due, "redacted", total.Redacted, "failed", total.Failed)
	}
	return total
}

func (service *RedactionService) Run(ctx context.Context) {
	if !service.config.Enabled {
		slog.Warn("ID redaction is disabled; stored IDs will not be deleted on schedule")
		return
	}
	service.RunCycle(ctx)
	ticker := time.NewTicker(service.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.RunCycle(ctx)
		}
	}
}
