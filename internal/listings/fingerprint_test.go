package listings

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func scene(width, height int, shade func(x, y float64) uint8, quality int) []byte {
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			level := shade(float64(x)/float64(width), float64(y)/float64(height))
			picture.Set(x, y, color.RGBA{R: level, G: level / 2, B: 255 - level, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: quality}); err != nil {
		panic(err)
	}
	return encoded.Bytes()
}

func porch(x, y float64) uint8 {
	if y > 0.6 {
		return uint8(40 + 80*x)
	}
	if x > 0.3 && x < 0.7 && y > 0.25 {
		return 220
	}
	return uint8(150 - 100*y)
}

func kitchen(x, y float64) uint8 {
	if (int(x*6)+int(y*4))%2 == 0 {
		return 230
	}
	return uint8(30 + 60*y)
}

func TestTheSamePhotoResavedAtAnotherSizeMatches(t *testing.T) {
	original := fingerprintOf(scene(1200, 900, porch, 90))
	resaved := fingerprintOf(scene(640, 480, porch, 60))
	if original == noFingerprint || !original.sameImageAs(resaved) {
		t.Fatalf("original %064b and resaved %064b should match", original, resaved)
	}
}

func TestDifferentPhotosDoNotMatch(t *testing.T) {
	if fingerprintOf(scene(800, 600, porch, 85)).sameImageAs(fingerprintOf(scene(800, 600, kitchen, 85))) {
		t.Fatal("porch and kitchen should not match")
	}
}

func TestUnreadablePhotosNeverMatch(t *testing.T) {
	unreadable := fingerprintOf([]byte("not an image"))
	if unreadable != noFingerprint || unreadable.sameImageAs(unreadable) {
		t.Fatalf("unreadable fingerprint = %d", unreadable)
	}
}

func TestAPhotoListedBeforeFingerprintsStillCountsAsADuplicate(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	store.setup.Photos = []ListingPhoto{{ID: uuid.New(), ObjectKey: ownKey(listingID), Screening: ScreeningPassed}}
	objects.content = scene(1000, 750, porch, 80)
	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, ownKey(listingID), ""); !errors.Is(err, ErrDuplicatePhoto) {
		t.Fatalf("error = %v", err)
	}
}

func TestALookalikeAddedWhileTheCheckRanIsRefusedAndDiscarded(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = scene(1200, 900, porch, 90)
	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, ownKey(listingID), ""); err != nil {
		t.Fatalf("first upload error = %v", err)
	}
	listedDuringCheck := store.setup.Photos
	store.setup.Photos = nil
	objects.content = scene(900, 675, porch, 75)
	racing := ownKey(listingID)
	store.racingPhotos = listedDuringCheck
	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, racing, ""); !errors.Is(err, ErrDuplicatePhoto) || !slices.Contains(objects.deleted, racing) {
		t.Fatalf("racing duplicate error = %v, deleted = %v", err, objects.deleted)
	}
}

func TestADuplicatePhotoIsRefusedAndItsUploadDiscarded(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	ctx := context.Background()
	objects.content = scene(1200, 900, porch, 90)
	if _, err := service.AddPhoto(ctx, store.owner, listingID, ownKey(listingID), ""); err != nil {
		t.Fatalf("first upload error = %v", err)
	}
	objects.content = scene(800, 600, porch, 70)
	again := ownKey(listingID)
	if _, err := service.AddPhoto(ctx, store.owner, listingID, again, ""); !errors.Is(err, ErrDuplicatePhoto) || !slices.Contains(objects.deleted, again) {
		t.Fatalf("duplicate error = %v, deleted = %v", err, objects.deleted)
	}
	objects.content = scene(1200, 900, kitchen, 90)
	setup, err := service.AddPhoto(ctx, store.owner, listingID, ownKey(listingID), "")
	if err != nil || len(setup.Photos) != 2 {
		t.Fatalf("different photo: setup = %+v, err = %v", setup, err)
	}
}
