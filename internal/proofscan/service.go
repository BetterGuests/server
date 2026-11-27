package proofscan

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	scanInterval    = 2 * time.Minute
	scanBatch       = 10
	maxDocumentSize = 15 << 20
	excerptRunes    = 500
	errorRunes      = 280
	enabledFlag     = "PROOF_SCAN_ENABLED"
	autoApproveFlag = "PROOF_AUTO_APPROVE"
)

type Due struct {
	ProofID     uuid.UUID
	ObjectKey   string
	ContentType string
	OwnerName   string
	IDName      string
	Address     Address
}

func (due Due) ExpectedName() string {
	if strings.TrimSpace(due.OwnerName) != "" {
		return due.OwnerName
	}
	return due.IDName
}

type Result struct {
	ProofID        uuid.UUID
	FoundName      string
	NameVerdict    NameVerdict
	AddressMatched bool
	Excerpt        string
	Error          string
	ScannedAt      time.Time
	DocumentDate   *time.Time
}

type Store interface {
	DueForScan(ctx context.Context, limit int) ([]Due, error)
	Record(ctx context.Context, result Result) (approved bool, err error)
	ForgetDeletedText(ctx context.Context) (int, error)
}

type DocumentReader interface {
	Read(ctx context.Context, key string, maxBytes int64) ([]byte, error)
}

type Service struct {
	store     Store
	documents DocumentReader
	extractor TextExtractor
	now       func() time.Time
}

func Enabled(lookup func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(lookup(enabledFlag)), "true")
}

func AutoApproveEnabled(lookup func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(lookup(autoApproveFlag)), "true")
}

func NewService(store Store, documents DocumentReader, extractor TextExtractor) *Service {
	return &Service{store: store, documents: documents, extractor: extractor, now: time.Now}
}

func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

func excerptOf(text string) string {
	return clip(strings.Join(strings.Fields(text), " "), excerptRunes)
}

func (service *Service) Scan(ctx context.Context, due Due) (Result, error) {
	data, err := service.documents.Read(ctx, due.ObjectKey, maxDocumentSize)
	if err != nil {
		return Result{}, fmt.Errorf("read document: %w", err)
	}
	result := Result{ProofID: due.ProofID, NameVerdict: NameNotFound}
	text, err := service.extractor.Extract(ctx, data, due.ContentType)
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	result.ScannedAt = service.now()
	if err != nil {
		result.Error = clip(err.Error(), errorRunes)
		return result, nil
	}
	result.FoundName, result.NameVerdict = FindName(text, due.ExpectedName())
	result.AddressMatched = AddressMatches(text, due.Address)
	if date, found := LatestDate(text, result.ScannedAt); found {
		result.DocumentDate = &date
	}
	result.Excerpt = excerptOf(text)
	return result, nil
}

func (service *Service) RunOnce(ctx context.Context) (int, error) {
	due, err := service.store.DueForScan(ctx, scanBatch)
	if err != nil {
		return 0, err
	}
	scanned := 0
	for _, proof := range due {
		if ctx.Err() != nil {
			return scanned, ctx.Err()
		}
		result, err := service.Scan(ctx, proof)
		if ctx.Err() != nil {
			return scanned, ctx.Err()
		}
		if err != nil {
			slog.Warn("document scan will retry", "proof_id", proof.ProofID, "error", err)
			continue
		}
		approved, err := service.store.Record(ctx, result)
		if err != nil {
			slog.Error("document scan not recorded and will retry", "proof_id", proof.ProofID, "error", err)
			continue
		}
		if approved {
			slog.Info("property proof approved automatically", "proof_id", proof.ProofID)
		}
		scanned++
	}
	return scanned, nil
}

func (service *Service) runCycle(ctx context.Context) {
	if cleared, err := service.store.ForgetDeletedText(ctx); err != nil {
		slog.Error("scan text for deleted documents was not cleared", "error", err)
	} else if cleared > 0 {
		slog.Info("scan text cleared for deleted documents", "cleared", cleared)
	}
	for ctx.Err() == nil {
		scanned, err := service.RunOnce(ctx)
		if err != nil {
			slog.Error("document scan cycle stopped", "error", err)
			return
		}
		if scanned > 0 {
			slog.Info("ownership documents scanned", "scanned", scanned)
		}
		if scanned < scanBatch {
			return
		}
	}
}

func (service *Service) Run(ctx context.Context) {
	service.runCycle(ctx)
	ticker := time.NewTicker(scanInterval)
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
