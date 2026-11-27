package main

import (
	"context"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
)

type cacheableStoreStub struct {
	cashback.ObjectStore
	signed int
}

func (stub *cacheableStoreStub) PresignCacheableDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	stub.signed++
	return "https://storage.test/cacheable/" + key, nil
}

func TestListingPhotosUseOneCacheableLinkPerPhoto(t *testing.T) {
	store := &cacheableStoreStub{}
	photos := listingObjectsFrom(store)

	for range 3 {
		url, err := photos.PresignDownload(context.Background(), "listings/a/0.jpg", time.Hour)
		if err != nil || url != "https://storage.test/cacheable/listings/a/0.jpg" {
			t.Fatalf("url %q, error %v", url, err)
		}
	}

	if store.signed != 1 {
		t.Fatalf("signed %d times", store.signed)
	}
}
