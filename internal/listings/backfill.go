package listings

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type CardBackfillService struct {
	store CardBackfillStore
	files CardBackfillFiles
}

func NewCardBackfillService(store CardBackfillStore, files CardBackfillFiles) *CardBackfillService {
	return &CardBackfillService{store: store, files: files}
}

func (service *CardBackfillService) Preview(ctx context.Context) (BackfillReport, error) {
	return service.run(ctx, func(context.Context, PhotoToResize, []byte, []byte, *BackfillReport) error { return nil })
}

func (service *CardBackfillService) Apply(ctx context.Context) (BackfillReport, error) {
	return service.run(ctx, service.replace)
}

type resizeStep func(ctx context.Context, photo PhotoToResize, original, card []byte, report *BackfillReport) error

func (service *CardBackfillService) run(ctx context.Context, step resizeStep) (BackfillReport, error) {
	photos, err := service.store.PublishedPhotos(ctx)
	if err != nil {
		return BackfillReport{}, fmt.Errorf("list published photos: %w", err)
	}
	report := BackfillReport{Photos: len(photos)}
	for _, photo := range photos {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if strings.HasSuffix(photo.ObjectKey, sizedPhotoSuffix) {
			report.AlreadySized++
			continue
		}
		original, err := service.files.Read(ctx, photo.ObjectKey, maxPhotoBytes)
		if err != nil {
			slog.Warn("Read listing photo", "photo", photo.PhotoID, "error", err)
			report.Failed++
			continue
		}
		card := boundedCardVersionOf(original)
		if card == nil {
			report.KeptOriginal++
			continue
		}
		report.Resizable++
		report.OriginalBytes += len(original)
		report.CardBytes += len(card)
		if err := step(ctx, photo, original, card, &report); err != nil {
			slog.Warn("Resize listing photo", "photo", photo.PhotoID, "error", err)
			report.Failed++
		}
	}
	return report, nil
}

func (service *CardBackfillService) replace(ctx context.Context, photo PhotoToResize, original, card []byte, report *BackfillReport) error {
	sizedKey := serverPhotoBase(photo.ListingID, photo.ObjectKey) + sizedPhotoSuffix
	if err := writeSizedPhoto(ctx, service.files, sizedKey, original, card); err != nil {
		return err
	}
	changed, err := service.store.ReplacePhotoKey(ctx, photo, sizedKey)
	if err != nil {
		return fmt.Errorf("point photo at %s: %w", sizedKey, err)
	}
	if !changed {
		cardKey, _ := cardKeyOf(sizedKey)
		removePhotoFiles(ctx, service.files, sizedKey, cardKey)
		return nil
	}
	report.Resized++
	report.Changes = append(report.Changes, PhotoKeyChange{PhotoID: photo.PhotoID, From: photo.ObjectKey, To: sizedKey})
	return nil
}
