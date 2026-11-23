package profiles

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubObjects struct {
	info      ObjectInfo
	statErr   error
	removed   []string
	signedKey string
}

func (objects *stubObjects) PresignUpload(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	objects.signedKey = key
	return "https://upload.test/" + key, nil
}

func (objects *stubObjects) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://view.test/" + key, nil
}

func (objects *stubObjects) Stat(context.Context, string) (ObjectInfo, error) {
	return objects.info, objects.statErr
}

func (objects *stubObjects) Remove(_ context.Context, key string) error {
	objects.removed = append(objects.removed, key)
	return nil
}

type stubStore struct{ key string }

func (store *stubStore) BannerKey(context.Context, uuid.UUID) (string, error) {
	return store.key, nil
}

func (store *stubStore) SetBannerKey(_ context.Context, _ uuid.UUID, key string) error {
	store.key = key
	return nil
}

func TestRequestBannerUploadRejectsBadFiles(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		size        int64
	}{
		{"unknown type", "application/pdf", 1000},
		{"gif", "image/gif", 1000},
		{"empty", "image/jpeg", 0},
		{"too large", "image/jpeg", maxBannerBytes + 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service := NewProfileService(&stubStore{}, &stubObjects{})
			_, err := service.RequestBannerUpload(context.Background(), uuid.New(), testCase.contentType, testCase.size)
			if !errors.Is(err, ErrBannerRejected) {
				t.Fatalf("err = %v, want ErrBannerRejected", err)
			}
		})
	}
}

func TestRequestBannerUploadKeyIsScopedToUser(t *testing.T) {
	userID := uuid.New()
	objects := &stubObjects{}
	service := NewProfileService(&stubStore{}, objects)
	ticket, err := service.RequestBannerUpload(context.Background(), userID, "image/jpeg", 2000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ticket.BannerKey, "banners/"+userID.String()+"/") || !strings.HasSuffix(ticket.BannerKey, ".jpg") {
		t.Fatalf("key = %q", ticket.BannerKey)
	}
}

func TestUploadsDisabledWithoutObjectStore(t *testing.T) {
	service := NewProfileService(&stubStore{}, nil)
	if _, err := service.RequestBannerUpload(context.Background(), uuid.New(), "image/jpeg", 10); !errors.Is(err, ErrUploadsDisabled) {
		t.Fatalf("err = %v", err)
	}
	banner, err := service.Banner(context.Background(), uuid.New())
	if err != nil || banner.URL != "" {
		t.Fatalf("banner = %+v, err = %v", banner, err)
	}
}

func TestSaveBannerRejectsAnotherUsersKey(t *testing.T) {
	cases := []string{
		"banners/" + uuid.New().String() + "/a.jpg",
		"banners/",
		"cashback/x/y.jpg",
		"banners/%s/../other.jpg",
		"banners/%s/nested/a.jpg",
	}
	for _, key := range cases {
		userID := uuid.New()
		key = strings.ReplaceAll(key, "%s", userID.String())
		service := NewProfileService(&stubStore{}, &stubObjects{})
		if _, err := service.SaveBanner(context.Background(), userID, key); !errors.Is(err, ErrInvalidBannerKey) {
			t.Fatalf("key %q: err = %v", key, err)
		}
	}
}

func TestSaveBannerRequiresUploadedObject(t *testing.T) {
	userID := uuid.New()
	service := NewProfileService(&stubStore{}, &stubObjects{statErr: errors.New("missing")})
	_, err := service.SaveBanner(context.Background(), userID, bannerKeyPrefix(userID)+"a.jpg")
	if !errors.Is(err, ErrBannerNotUploaded) {
		t.Fatalf("err = %v", err)
	}
}

func TestSaveBannerDiscardsObjectWithWrongTypeOrSize(t *testing.T) {
	cases := []ObjectInfo{
		{Size: 100, ContentType: "text/html"},
		{Size: maxBannerBytes + 1, ContentType: "image/jpeg"},
		{Size: 0, ContentType: "image/jpeg"},
	}
	for _, info := range cases {
		userID := uuid.New()
		objects := &stubObjects{info: info}
		store := &stubStore{}
		service := NewProfileService(store, objects)
		key := bannerKeyPrefix(userID) + "a.jpg"
		if _, err := service.SaveBanner(context.Background(), userID, key); !errors.Is(err, ErrBannerRejected) {
			t.Fatalf("info %+v: err = %v", info, err)
		}
		if store.key != "" || len(objects.removed) != 1 || objects.removed[0] != key {
			t.Fatalf("info %+v: key = %q, removed = %v", info, store.key, objects.removed)
		}
	}
}

func TestSaveBannerReplacesAndRemovesPreviousObject(t *testing.T) {
	userID := uuid.New()
	previous := bannerKeyPrefix(userID) + "old.jpg"
	next := bannerKeyPrefix(userID) + "new.jpg"
	objects := &stubObjects{info: ObjectInfo{Size: 1000, ContentType: "image/jpeg"}}
	store := &stubStore{key: previous}
	service := NewProfileService(store, objects)
	banner, err := service.SaveBanner(context.Background(), userID, next)
	if err != nil {
		t.Fatal(err)
	}
	if store.key != next || banner.URL != "https://view.test/"+next {
		t.Fatalf("key = %q, banner = %+v", store.key, banner)
	}
	if len(objects.removed) != 1 || objects.removed[0] != previous {
		t.Fatalf("removed = %v", objects.removed)
	}
}

func TestRemoveBannerClearsKeyAndObject(t *testing.T) {
	userID := uuid.New()
	key := bannerKeyPrefix(userID) + "a.jpg"
	objects := &stubObjects{}
	store := &stubStore{key: key}
	service := NewProfileService(store, objects)
	if err := service.RemoveBanner(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
	if store.key != "" || len(objects.removed) != 1 {
		t.Fatalf("key = %q, removed = %v", store.key, objects.removed)
	}
	if err := service.RemoveBanner(context.Background(), userID); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestBannerIsEmptyWhenNoneSet(t *testing.T) {
	service := NewProfileService(&stubStore{}, &stubObjects{})
	banner, err := service.Banner(context.Background(), uuid.New())
	if err != nil || banner.URL != "" {
		t.Fatalf("banner = %+v, err = %v", banner, err)
	}
}
