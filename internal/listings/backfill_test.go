package listings

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
)

type backfillStoreStub struct {
	photos   []PhotoToResize
	replaced map[uuid.UUID]string
	changed  bool
	err      error
}

func (stub *backfillStoreStub) PublishedPhotos(context.Context) ([]PhotoToResize, error) {
	return stub.photos, nil
}

func (stub *backfillStoreStub) ReplacePhotoKey(_ context.Context, photo PhotoToResize, newKey string) (bool, error) {
	if stub.replaced == nil {
		stub.replaced = map[uuid.UUID]string{}
	}
	stub.replaced[photo.PhotoID] = newKey
	return stub.changed, stub.err
}

func backfillHarness(t *testing.T) (*backfillStoreStub, *photoObjectsStub, PhotoToResize) {
	t.Helper()
	listingID := uuid.New()
	photo := PhotoToResize{HostID: uuid.New(), ListingID: listingID, PhotoID: uuid.New(), ObjectKey: ownKey(listingID)}
	store := &backfillStoreStub{photos: []PhotoToResize{photo}, changed: true}
	return store, &photoObjectsStub{content: jpegOf(t, 2000, 1500)}, photo
}

func TestBackfillPreviewChangesNothing(t *testing.T) {
	store, files, _ := backfillHarness(t)

	report, err := NewCardBackfillService(store, files).Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(files.written) != 0 || len(files.deleted) != 0 || len(store.replaced) != 0 {
		t.Fatalf("preview wrote %d, deleted %v, replaced %v", len(files.written), files.deleted, store.replaced)
	}
	if report.Resizable != 1 || report.Resized != 0 || report.OriginalBytes != len(files.content) || report.CardBytes <= 0 || report.CardBytes >= report.OriginalBytes {
		t.Fatalf("report = %+v", report)
	}
}

func TestBackfillPointsThePhotoAtItsNewFilesAndKeepsTheOriginal(t *testing.T) {
	store, files, photo := backfillHarness(t)

	report, err := NewCardBackfillService(store, files).Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	newKey := store.replaced[photo.PhotoID]
	card, sized := cardKeyOf(newKey)
	if !sized || !validPhotoKey(newKey, photo.ListingID) || len(files.written[newKey]) == 0 || len(files.written[card]) == 0 {
		t.Fatalf("new key %q, written %d", newKey, len(files.written))
	}
	if len(files.deleted) != 0 {
		t.Fatalf("deleted = %v", files.deleted)
	}
	if report.Resized != 1 || !slices.Equal(report.Changes, []PhotoKeyChange{{PhotoID: photo.PhotoID, From: photo.ObjectKey, To: newKey}}) {
		t.Fatalf("report = %+v", report)
	}
}

func TestBackfillSkipsPhotosThatNeedNoCard(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*backfillStoreStub, *photoObjectsStub)
		want    BackfillReport
	}{
		{"already resized", func(store *backfillStoreStub, _ *photoObjectsStub) {
			store.photos[0].ObjectKey = "listings/a/one.sized.jpg"
		}, BackfillReport{AlreadySized: 1}},
		{"too small", func(_ *backfillStoreStub, files *photoObjectsStub) {
			files.content = []byte("not a large picture")
		}, BackfillReport{KeptOriginal: 1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store, files, _ := backfillHarness(t)
			test.prepare(store, files)

			report, err := NewCardBackfillService(store, files).Apply(context.Background())

			if err != nil || report.AlreadySized != test.want.AlreadySized || report.KeptOriginal != test.want.KeptOriginal || report.Resized != 0 || len(files.written) != 0 {
				t.Fatalf("report = %+v, err = %v", report, err)
			}
		})
	}
}

func TestBackfillRemovesNewFilesOnlyWhenThePhotoSurelyDidNotChange(t *testing.T) {
	cases := []struct {
		name    string
		changed bool
		err     error
		removed int
	}{
		{"photo was removed or replaced meanwhile", false, nil, 2},
		{"outcome of the update is unknown", false, errors.New("connection lost"), 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store, files, _ := backfillHarness(t)
			store.changed, store.err = test.changed, test.err

			report, err := NewCardBackfillService(store, files).Apply(context.Background())

			if err != nil || report.Resized != 0 || len(files.deleted) != test.removed {
				t.Fatalf("report = %+v, deleted = %v, err = %v", report, files.deleted, err)
			}
			if test.err != nil && report.Failed != 1 {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}
