package listings

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type screenerStub struct {
	refusal PhotoRefusal
	err     error
	calls   int
}

func (stub *screenerStub) Screen(context.Context, []byte, string) (PhotoRefusal, error) {
	stub.calls++
	return stub.refusal, stub.err
}

var jpegBytes = []byte("\xff\xd8\xff\xe0 pretend photo")

func TestScreenedPhotoIsStoredUnderAKeyTheHostCannotOverwrite(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegBytes
	service.WithScreening(&screenerStub{})
	upload := ownKey(listingID)
	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, ""); err != nil {
		t.Fatal(err)
	}
	stored := store.added[0].ObjectKey
	if stored == upload || !validPhotoKey(stored, listingID) {
		t.Fatalf("stored key = %q", stored)
	}
	if string(objects.written[stored]) != string(jpegBytes) || !slices.Contains(objects.deleted, upload) {
		t.Fatalf("written = %v, deleted = %v", objects.written, objects.deleted)
	}
}

func TestPhotoThatFailsScreeningNeverReachesTheListing(t *testing.T) {
	cases := []struct {
		name       string
		screener   screenerStub
		content    []byte
		size       int64
		want       error
		status     int
		keepUpload bool
	}{
		{"refused by the screener", screenerStub{refusal: RefusedUnrelated}, jpegBytes, 2048, ErrPhotoNotAllowed, http.StatusUnprocessableEntity, false},
		{"screener unavailable", screenerStub{err: errors.New("down")}, jpegBytes, 2048, ErrPhotoCheckFailed, http.StatusServiceUnavailable, true},
		{"bytes are not an image", screenerStub{}, []byte("<html></html>"), 2048, ErrPhotoRejected, http.StatusBadRequest, false},
		{"too large to check", screenerStub{}, jpegBytes, maxScreenedPhotoBytes + 1, ErrPhotoTooLargeToCheck, http.StatusUnprocessableEntity, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, store, objects, listingID := mediaHarness()
			objects.content = test.content
			objects.info.Size = test.size
			screener := test.screener
			service.WithScreening(&screener)
			upload := ownKey(listingID)
			_, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, "")
			if !errors.Is(err, test.want) || mediaErrorStatus(err) != test.status {
				t.Fatalf("error = %v", err)
			}
			if len(store.added) != 0 || len(objects.written) != 0 {
				t.Fatalf("added = %v, written = %v", store.added, objects.written)
			}
			if slices.Contains(objects.deleted, upload) == test.keepUpload {
				t.Fatalf("deleted = %v", objects.deleted)
			}
		})
	}
}

func TestRefusalTellsTheHostWhy(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegBytes
	service.WithScreening(&screenerStub{refusal: RefusedExplicit})
	_, err := service.AddPhoto(context.Background(), store.owner, listingID, ownKey(listingID), "")
	if err == nil || !strings.Contains(err.Error(), string(RefusedExplicit)) {
		t.Fatalf("error = %v", err)
	}
}

func TestAddingAnExistingPhotoAgainLeavesItInPlace(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	screener := &screenerStub{}
	service.WithScreening(screener)
	existing := ownKey(listingID)
	store.setup.Photos = []ListingPhoto{{ObjectKey: existing}}
	_, err := service.AddPhoto(context.Background(), store.owner, listingID, existing, "")
	if !errors.Is(err, ErrPhotoAlreadyAdded) || screener.calls != 0 || len(objects.deleted) != 0 {
		t.Fatalf("error = %v, calls = %d, deleted = %v", err, screener.calls, objects.deleted)
	}
}
