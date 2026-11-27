package cashback

import (
	"context"
	"net/url"
	"testing"
	"time"
)

func TestCacheableDownloadAsksStorageToAllowPrivateBrowserCaching(t *testing.T) {
	store, err := NewS3ObjectStore(StorageConfig{
		Endpoint: "storage.test", Bucket: "photos", Region: "auto",
		AccessKeyID: "test-key", SecretAccessKey: "test-secret", PathStyle: true, UseSSL: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	signed, err := store.PresignCacheableDownload(context.Background(), "listings/a/0.jpg", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("response-cache-control"); got != "private, max-age=3600" {
		t.Fatalf("response-cache-control = %q", got)
	}
}
