package listings

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type mediaStoreStub struct {
	owner   uuid.UUID
	setup   Setup
	added   []ListingPhoto
	price   int
	removed string
}

func (stub *mediaStoreStub) Setup(_ context.Context, userID string, _ uuid.UUID) (Setup, error) {
	if userID != stub.owner.String() {
		return Setup{}, ErrNotListingOwner
	}
	setup := stub.setup
	setup.Photos = slices.Clone(stub.setup.Photos)
	return setup, nil
}

func (stub *mediaStoreStub) AddPhoto(_ context.Context, _ string, _ uuid.UUID, photo ListingPhoto) error {
	stub.added = append(stub.added, photo)
	stub.setup.Photos = append(stub.setup.Photos, photo)
	return nil
}

func (stub *mediaStoreStub) RemovePhoto(context.Context, string, uuid.UUID, uuid.UUID) (string, error) {
	return stub.removed, nil
}

func (stub *mediaStoreStub) SetPrice(_ context.Context, _ string, _ uuid.UUID, nightlyCents int) error {
	stub.price = nightlyCents
	return nil
}

type photoObjectsStub struct {
	info    ObjectInfo
	missing bool
	deleted []string
}

func (stub *photoObjectsStub) PresignUpload(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return "https://storage.test/put/" + key, nil
}

func (stub *photoObjectsStub) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.test/get/" + key, nil
}

func (stub *photoObjectsStub) Stat(context.Context, string) (ObjectInfo, error) {
	if stub.missing {
		return ObjectInfo{}, errors.New("missing")
	}
	return stub.info, nil
}

func (stub *photoObjectsStub) Remove(_ context.Context, key string) error {
	stub.deleted = append(stub.deleted, key)
	return nil
}

func mediaHarness() (*ListingService, *mediaStoreStub, *photoObjectsStub, uuid.UUID) {
	store := &mediaStoreStub{owner: uuid.New(), setup: Setup{Status: Draft}}
	objects := &photoObjectsStub{info: ObjectInfo{Size: 2048, ContentType: "image/jpeg"}}
	return NewListingService(nil).WithMedia(store, objects), store, objects, uuid.New()
}

func ownKey(listingID uuid.UUID) string {
	return photoKeyPrefix(listingID) + uuid.NewString() + ".jpg"
}

func TestPhotoUploadTicketIsScopedToTheListing(t *testing.T) {
	service, store, _, listingID := mediaHarness()
	ticket, err := service.RequestPhotoUpload(context.Background(), store.owner, listingID, "image/png", 4096)
	if err != nil || !strings.HasPrefix(ticket.PhotoKey, "listings/"+listingID.String()+"/") || !strings.HasSuffix(ticket.PhotoKey, ".png") {
		t.Fatalf("ticket = %+v, err = %v", ticket, err)
	}
}

func TestPhotoUploadRefusesStrangersAndBadFiles(t *testing.T) {
	service, store, _, listingID := mediaHarness()
	ctx := context.Background()
	if _, err := service.RequestPhotoUpload(ctx, uuid.New(), listingID, "image/png", 10); !errors.Is(err, ErrNotListingOwner) {
		t.Fatalf("stranger error = %v", err)
	}
	for name, test := range map[string]struct {
		contentType string
		size        int64
	}{"pdf": {"application/pdf", 10}, "svg": {"image/svg+xml", 10}, "empty": {"image/png", 0}, "huge": {"image/png", maxPhotoBytes + 1}} {
		if _, err := service.RequestPhotoUpload(ctx, store.owner, listingID, test.contentType, test.size); !errors.Is(err, ErrPhotoRejected) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestOnlyThisListingsOwnUploadsCanBeAttached(t *testing.T) {
	service, store, _, listingID := mediaHarness()
	for name, key := range map[string]string{
		"another listing":     ownKey(uuid.New()),
		"a property proof":    "property-proofs/" + listingID.String() + "/" + uuid.NewString() + ".jpg",
		"a guest chore photo": "cashback/" + uuid.NewString() + "/strip_beds/" + uuid.NewString() + ".jpg",
		"path traversal":      photoKeyPrefix(listingID) + "../secret.jpg",
		"empty":               "",
	} {
		if _, err := service.AddPhoto(context.Background(), store.owner, listingID, key, ""); !errors.Is(err, ErrInvalidPhotoKey) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
	if len(store.added) != 0 {
		t.Fatalf("added = %+v", store.added)
	}
}

func TestAddingAPhotoChecksTheStoredFileAndReturnsSignedURLs(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	ctx := context.Background()
	objects.missing = true
	if _, err := service.AddPhoto(ctx, store.owner, listingID, ownKey(listingID), ""); !errors.Is(err, ErrPhotoNotUploaded) {
		t.Fatalf("missing error = %v", err)
	}
	objects.missing = false
	objects.info = ObjectInfo{Size: 10, ContentType: "text/html"}
	disguised := ownKey(listingID)
	if _, err := service.AddPhoto(ctx, store.owner, listingID, disguised, ""); !errors.Is(err, ErrPhotoRejected) || !slices.Contains(objects.deleted, disguised) {
		t.Fatalf("disguised error = %v, deleted = %v", err, objects.deleted)
	}
	objects.info = ObjectInfo{Size: 10, ContentType: "image/jpeg"}
	setup, err := service.AddPhoto(ctx, store.owner, listingID, ownKey(listingID), "  Front porch  ")
	if err != nil || len(setup.Photos) != 1 || setup.Photos[0].AltText != "Front porch" || !strings.HasPrefix(setup.Photos[0].URL, "https://storage.test/get/listings/") {
		t.Fatalf("setup = %+v, err = %v", setup, err)
	}
}

func TestRemovingAPhotoDeletesOnlyThisListingsFiles(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	ctx := context.Background()
	store.removed = "legacy/shared.jpg"
	if _, err := service.RemovePhoto(ctx, store.owner, listingID, uuid.New()); err != nil || len(objects.deleted) != 0 {
		t.Fatalf("err = %v, deleted = %v", err, objects.deleted)
	}
	store.removed = ownKey(listingID)
	if _, err := service.RemovePhoto(ctx, store.owner, listingID, uuid.New()); err != nil || !slices.Contains(objects.deleted, store.removed) {
		t.Fatalf("err = %v, deleted = %v", err, objects.deleted)
	}
}

func TestNightlyPriceMustBeWithinBounds(t *testing.T) {
	service, store, _, listingID := mediaHarness()
	for _, cents := range []int{-1, 0, 99, 10000001} {
		if _, err := service.SetPrice(context.Background(), store.owner, listingID, cents); !errors.Is(err, ErrInvalidPrice) {
			t.Fatalf("%d: error = %v", cents, err)
		}
	}
	if _, err := service.SetPrice(context.Background(), store.owner, listingID, 12500); err != nil || store.price != 12500 {
		t.Fatalf("price = %d, err = %v", store.price, err)
	}
}

func TestADraftCannotNameStorageObjectsAtCreation(t *testing.T) {
	input := validCreateInput()
	input.Photos = []CreateListingPhotoInput{{ObjectKey: "property-proofs/x/y.pdf"}}
	if _, err := NewListingService(nil).CreateListing(context.Background(), uuid.New(), input); !errors.Is(err, ErrInvalidListing) {
		t.Fatalf("error = %v", err)
	}
}
