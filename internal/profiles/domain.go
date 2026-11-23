package profiles

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	maxBannerBytes    = 5 << 20
	uploadURLLifetime = 10 * time.Minute
	viewURLLifetime   = time.Hour
	bannerKeyRoot     = "banners/"
)

var (
	ErrUploadsDisabled   = errors.New("uploads are not available")
	ErrBannerRejected    = errors.New("banner must be a JPEG, PNG or WebP image under 5 MB")
	ErrInvalidBannerKey  = errors.New("invalid banner key")
	ErrBannerNotUploaded = errors.New("banner was not uploaded")
)

type ObjectInfo struct {
	Size        int64
	ContentType string
}

type UploadTicket struct {
	UploadURL   string `json:"upload_url"`
	BannerKey   string `json:"banner_key"`
	ContentType string `json:"content_type"`
	ExpiresIn   int    `json:"expires_in_seconds"`
	MaxBytes    int64  `json:"max_bytes"`
}

type Banner struct {
	URL string `json:"url"`
}

type ObjectStore interface {
	PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error)
	PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	Remove(ctx context.Context, key string) error
}

type Store interface {
	BannerKey(ctx context.Context, userID uuid.UUID) (string, error)
	SetBannerKey(ctx context.Context, userID uuid.UUID, key string) error
}

type BannerReader interface {
	Banner(ctx context.Context, userID uuid.UUID) (Banner, error)
}

type UploadRequester interface {
	RequestBannerUpload(ctx context.Context, userID uuid.UUID, contentType string, sizeBytes int64) (UploadTicket, error)
}

type BannerSaver interface {
	SaveBanner(ctx context.Context, userID uuid.UUID, key string) (Banner, error)
}

type BannerRemover interface {
	RemoveBanner(ctx context.Context, userID uuid.UUID) error
}
