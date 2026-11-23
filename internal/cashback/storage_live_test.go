package cashback

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func liveStore(t *testing.T) *S3ObjectStore {
	t.Helper()
	config, enabled, err := StorageConfigFromLookup(os.Getenv)
	if err != nil || !enabled {
		t.Skip("set S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY to run live storage checks")
	}
	store, err := NewS3ObjectStore(config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestLiveBucketUploadStatAndDownload(t *testing.T) {
	store := liveStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := photoKeyPrefix(uuid.New(), "run_dishwasher") + uuid.NewString() + ".jpg"
	payload := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF}, 100)

	uploadURL, err := store.PresignUpload(ctx, key, "image/jpeg", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "image/jpeg")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", response.StatusCode)
	}

	info, err := store.Stat(ctx, key)
	if err != nil || info.Size != int64(len(payload)) || info.ContentType != "image/jpeg" {
		t.Fatalf("info = %+v, err = %v", info, err)
	}

	downloadURL, err := store.PresignDownload(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	download, err := http.Get(downloadURL)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	body, _ := io.ReadAll(download.Body)
	if download.StatusCode != http.StatusOK || !bytes.Equal(body, payload) {
		t.Fatalf("download status = %d, bytes = %d", download.StatusCode, len(body))
	}
}

func TestLiveBucketRejectsUnsignedAndWrongTypeUploads(t *testing.T) {
	store := liveStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := photoKeyPrefix(uuid.New(), "run_dishwasher") + uuid.NewString() + ".jpg"
	uploadURL, err := store.PresignUpload(ctx, key, "image/jpeg", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader([]byte("<html>")))
	request.Header.Set("Content-Type", "text/html")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("upload with a different content type than the one signed must be refused")
	}
}
