package cashback

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3ObjectStore struct {
	client *minio.Client
	bucket string
}

func StorageConfigFromLookup(lookup func(string) string) (StorageConfig, bool, error) {
	config := StorageConfig{
		Endpoint:        strings.TrimSpace(lookup("S3_ENDPOINT")),
		Bucket:          strings.TrimSpace(lookup("S3_BUCKET")),
		Region:          strings.TrimSpace(lookup("S3_REGION")),
		AccessKeyID:     strings.TrimSpace(lookup("S3_ACCESS_KEY_ID")),
		SecretAccessKey: strings.TrimSpace(lookup("S3_SECRET_ACCESS_KEY")),
		PathStyle:       strings.EqualFold(strings.TrimSpace(lookup("S3_URL_STYLE")), "path"),
	}
	values := []string{config.Endpoint, config.Bucket, config.AccessKeyID, config.SecretAccessKey}
	filled := 0
	for _, value := range values {
		if value != "" {
			filled++
		}
	}
	if filled == 0 {
		return StorageConfig{}, false, nil
	}
	if filled != len(values) {
		return StorageConfig{}, false, fmt.Errorf("S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must all be set together")
	}
	if config.Region == "" {
		config.Region = "auto"
	}
	endpoint := config.Endpoint
	config.UseSSL = true
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		config.UseSSL = parsed.Scheme != "http"
		config.Endpoint = parsed.Host
	}
	return config, true, nil
}

func NewObjectStoreFromEnv() (ObjectStore, error) {
	config, enabled, err := StorageConfigFromLookup(os.Getenv)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	return NewS3ObjectStore(config)
}

func NewS3ObjectStore(config StorageConfig) (*S3ObjectStore, error) {
	lookup := minio.BucketLookupDNS
	if config.PathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(config.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(config.AccessKeyID, config.SecretAccessKey, ""),
		Secure:       config.UseSSL,
		Region:       config.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("create storage client: %w", err)
	}
	return &S3ObjectStore{client: client, bucket: config.Bucket}, nil
}

func (store *S3ObjectStore) PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error) {
	headers := make(map[string][]string)
	headers["Content-Type"] = []string{contentType}
	signed, err := store.client.PresignHeader(ctx, "PUT", store.bucket, key, expires, nil, headers)
	if err != nil {
		return "", fmt.Errorf("presign put: %w", err)
	}
	return signed.String(), nil
}

func (store *S3ObjectStore) PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error) {
	signed, err := store.client.PresignedGetObject(ctx, store.bucket, key, expires, nil)
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return signed.String(), nil
}

func (store *S3ObjectStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := store.client.StatObject(ctx, store.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat object: %w", err)
	}
	return ObjectInfo{Size: info.Size, ContentType: info.ContentType}, nil
}
