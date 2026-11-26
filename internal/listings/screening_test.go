package listings

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"
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

func onlyPhoto(t *testing.T, store *mediaStoreStub) ListingPhoto {
	t.Helper()
	if len(store.setup.Photos) != 1 {
		t.Fatalf("photos = %+v", store.setup.Photos)
	}
	return store.setup.Photos[0]
}

func TestUploadReturnsBeforeThePhotoIsChecked(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegBytes
	screener := &screenerStub{}
	service.WithScreening(screener)
	var later []func()
	service.inBackground = func(work func()) { later = append(later, work) }
	setup, err := service.AddPhoto(context.Background(), store.owner, listingID, ownKey(listingID), "")
	if err != nil || screener.calls != 0 || len(setup.Photos) != 1 || setup.Photos[0].Screening != ScreeningPending {
		t.Fatalf("setup = %+v, err = %v, calls = %d", setup, err, screener.calls)
	}
	later[0]()
	if screener.calls != 1 || onlyPhoto(t, store).Screening != ScreeningPassed {
		t.Fatalf("calls = %d, photo = %+v", screener.calls, onlyPhoto(t, store))
	}
}

func TestScreenedPhotoIsStoredUnderAKeyTheHostCannotOverwrite(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegBytes
	service.WithScreening(&screenerStub{})
	upload := ownKey(listingID)
	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, ""); err != nil {
		t.Fatal(err)
	}
	stored := onlyPhoto(t, store).ObjectKey
	if stored == upload || !validPhotoKey(stored, listingID) {
		t.Fatalf("stored key = %q", stored)
	}
	if string(objects.written[stored]) != string(jpegBytes) || !slices.Contains(objects.deleted, upload) {
		t.Fatalf("written = %v, deleted = %v", objects.written, objects.deleted)
	}
}

func TestARefusedPhotoStaysOnTheListingWithItsReason(t *testing.T) {
	cases := []struct {
		name     string
		screener screenerStub
		content  []byte
		want     PhotoRefusal
	}{
		{"refused by the screener", screenerStub{refusal: RefusedUnrelated}, jpegBytes, RefusedUnrelated},
		{"bytes are not an image", screenerStub{}, []byte("<html></html>"), RefusedNotAPhoto},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, store, objects, listingID := mediaHarness()
			objects.content = test.content
			screener := test.screener
			service.WithScreening(&screener)
			upload := ownKey(listingID)
			if _, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, ""); err != nil {
				t.Fatal(err)
			}
			photo := onlyPhoto(t, store)
			if photo.Screening != ScreeningRefused || photo.Refusal != test.want || photo.ObjectKey != upload {
				t.Fatalf("photo = %+v", photo)
			}
			if len(objects.written) != 0 || len(objects.deleted) != 0 {
				t.Fatalf("written = %v, deleted = %v", objects.written, objects.deleted)
			}
		})
	}
}

func TestAnUnavailableScreenerLeavesThePhotoPending(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegBytes
	service.WithScreening(&screenerStub{err: errors.New("down")})
	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, ownKey(listingID), ""); err != nil {
		t.Fatal(err)
	}
	if photo := onlyPhoto(t, store); photo.Screening != ScreeningPending {
		t.Fatalf("photo = %+v", photo)
	}
}

func TestAPhotoTooLargeToCheckIsRefusedAtUpload(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.info.Size = maxScreenedPhotoBytes + 1
	service.WithScreening(&screenerStub{})
	upload := ownKey(listingID)
	_, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, "")
	if !errors.Is(err, ErrPhotoTooLargeToCheck) || mediaErrorStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("error = %v", err)
	}
	if len(store.added) != 0 || !slices.Contains(objects.deleted, upload) {
		t.Fatalf("added = %v, deleted = %v", store.added, objects.deleted)
	}
}

func TestPhotosAreAddedCheckedWhenScreeningIsOff(t *testing.T) {
	service, store, _, listingID := mediaHarness()
	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, ownKey(listingID), ""); err != nil {
		t.Fatal(err)
	}
	if photo := onlyPhoto(t, store); photo.Screening != ScreeningPassed {
		t.Fatalf("photo = %+v", photo)
	}
}

func TestPublishChecksPhotosStillWaiting(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegBytes
	screener := &screenerStub{}
	service.WithScreening(screener)
	pending := ListingPhoto{ID: uuid.New(), ObjectKey: ownKey(listingID), Screening: ScreeningPending}
	store.setup.Photos = []ListingPhoto{pending, {ID: uuid.New(), ObjectKey: ownKey(listingID), Screening: ScreeningPassed}}
	if err := service.screenPendingPhotos(context.Background(), store.owner, listingID); err != nil {
		t.Fatal(err)
	}
	if screener.calls != 1 || store.setup.Photos[0].Screening != ScreeningPassed {
		t.Fatalf("calls = %d, photos = %+v", screener.calls, store.setup.Photos)
	}
}

func TestPublishFailsWhenAWaitingPhotoCannotBeChecked(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegBytes
	service.WithScreening(&screenerStub{err: errors.New("down")})
	store.setup.Photos = []ListingPhoto{{ID: uuid.New(), ObjectKey: ownKey(listingID), Screening: ScreeningPending}}
	if err := service.screenPendingPhotos(context.Background(), store.owner, listingID); !errors.Is(err, ErrPhotoCheckFailed) {
		t.Fatalf("error = %v", err)
	}
}

func TestAPhotoRemovedWhileBeingCheckedLeavesNoCopyBehind(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegBytes
	service.WithScreening(&screenerStub{})
	photo := ListingPhoto{ID: uuid.New(), ObjectKey: ownKey(listingID), Screening: ScreeningPending}
	if err := service.screenPhoto(context.Background(), store.owner, listingID, photo); err != nil {
		t.Fatal(err)
	}
	for key := range objects.written {
		if !slices.Contains(objects.deleted, key) {
			t.Fatalf("checked copy %q was left behind", key)
		}
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
