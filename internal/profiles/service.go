package profiles

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

var allowedBannerTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

type ProfileService struct {
	store   Store
	objects ObjectStore
	newID   func() uuid.UUID
}

func NewProfileService(store Store, objects ObjectStore) *ProfileService {
	return &ProfileService{store: store, objects: objects, newID: uuid.New}
}

func bannerKeyPrefix(userID uuid.UUID) string {
	return bannerKeyRoot + userID.String() + "/"
}

func validBannerKey(key string, userID uuid.UUID) bool {
	rest, owned := strings.CutPrefix(key, bannerKeyPrefix(userID))
	if !owned || rest == "" || strings.Contains(rest, "/") || strings.Contains(rest, "..") {
		return false
	}
	return true
}

func (service *ProfileService) RequestBannerUpload(ctx context.Context, userID uuid.UUID, contentType string, sizeBytes int64) (UploadTicket, error) {
	if service.objects == nil {
		return UploadTicket{}, ErrUploadsDisabled
	}
	extension, allowed := allowedBannerTypes[contentType]
	if !allowed || sizeBytes <= 0 || sizeBytes > maxBannerBytes {
		return UploadTicket{}, ErrBannerRejected
	}
	key := bannerKeyPrefix(userID) + service.newID().String() + extension
	url, err := service.objects.PresignUpload(ctx, key, contentType, uploadURLLifetime)
	if err != nil {
		return UploadTicket{}, fmt.Errorf("presign banner upload: %w", err)
	}
	return UploadTicket{
		UploadURL:   url,
		BannerKey:   key,
		ContentType: contentType,
		ExpiresIn:   int(uploadURLLifetime.Seconds()),
		MaxBytes:    maxBannerBytes,
	}, nil
}

func (service *ProfileService) SaveBanner(ctx context.Context, userID uuid.UUID, key string) (Banner, error) {
	if service.objects == nil {
		return Banner{}, ErrUploadsDisabled
	}
	if !validBannerKey(key, userID) {
		return Banner{}, ErrInvalidBannerKey
	}
	info, err := service.objects.Stat(ctx, key)
	if err != nil {
		return Banner{}, ErrBannerNotUploaded
	}
	if _, allowed := allowedBannerTypes[info.ContentType]; !allowed || info.Size <= 0 || info.Size > maxBannerBytes {
		service.discard(ctx, key)
		return Banner{}, ErrBannerRejected
	}
	previous, err := service.store.BannerKey(ctx, userID)
	if err != nil {
		return Banner{}, err
	}
	if err := service.store.SetBannerKey(ctx, userID, key); err != nil {
		return Banner{}, err
	}
	if previous != "" && previous != key {
		service.discard(ctx, previous)
	}
	return service.signed(ctx, key)
}

func (service *ProfileService) Banner(ctx context.Context, userID uuid.UUID) (Banner, error) {
	if service.objects == nil {
		return Banner{}, nil
	}
	key, err := service.store.BannerKey(ctx, userID)
	if err != nil {
		return Banner{}, err
	}
	if key == "" {
		return Banner{}, nil
	}
	return service.signed(ctx, key)
}

func (service *ProfileService) RemoveBanner(ctx context.Context, userID uuid.UUID) error {
	previous, err := service.store.BannerKey(ctx, userID)
	if err != nil {
		return err
	}
	if previous == "" {
		return nil
	}
	if err := service.store.SetBannerKey(ctx, userID, ""); err != nil {
		return err
	}
	if service.objects != nil {
		service.discard(ctx, previous)
	}
	return nil
}

func (service *ProfileService) signed(ctx context.Context, key string) (Banner, error) {
	url, err := service.objects.PresignDownload(ctx, key, viewURLLifetime)
	if err != nil {
		return Banner{}, fmt.Errorf("presign banner download: %w", err)
	}
	return Banner{URL: url}, nil
}

func (service *ProfileService) discard(ctx context.Context, key string) {
	if err := service.objects.Remove(ctx, key); err != nil {
		slog.Warn("Remove banner object", "error", err)
	}
}
