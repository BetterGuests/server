package proofscan

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type storeStub struct {
	due      []Due
	recorded []Result
	failRead error
}

func (store *storeStub) DueForScan(context.Context, int) ([]Due, error) {
	return store.due, store.failRead
}

func (store *storeStub) Record(_ context.Context, result Result) error {
	store.recorded = append(store.recorded, result)
	return nil
}

func (store *storeStub) ForgetDeletedText(context.Context) (int, error) { return 0, nil }

type readerStub struct {
	files map[string][]byte
}

func (reader readerStub) Read(_ context.Context, key string, _ int64) ([]byte, error) {
	data, ok := reader.files[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return data, nil
}

type extractorStub struct {
	err error
}

func (extractor extractorStub) Extract(_ context.Context, data []byte, _ string) (string, error) {
	return string(data), extractor.err
}

var scannedAt = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func newTestService(store *storeStub, files map[string][]byte, extractor TextExtractor) *Service {
	service := NewService(store, readerStub{files: files}, extractor)
	service.now = func() time.Time { return scannedAt }
	return service
}

func phoenixProof(ownerName string) Due {
	return Due{
		ProofID:     uuid.New(),
		ObjectKey:   "property-proofs/a/bill.pdf",
		ContentType: "application/pdf",
		OwnerName:   ownerName,
		IDName:      "Jason Coawette",
		Address:     Address{Line1: "4122 East Cathedral Rock Drive", City: "Phoenix", PostalCode: "85044"},
	}
}

func TestRunOnceRecordsTheNameAndAddressFoundInTheDocument(t *testing.T) {
	proof := phoenixProof("")
	store := &storeStub{due: []Due{proof}}
	service := newTestService(store, map[string][]byte{proof.ObjectKey: []byte(utilityBill)}, extractorStub{})

	scanned, err := service.RunOnce(context.Background())
	if err != nil || scanned != 1 {
		t.Fatalf("scanned %d, err %v", scanned, err)
	}
	got := store.recorded[0]
	if got.FoundName != "JASON M. COAWETTE" || got.NameVerdict != NameMatch || !got.AddressMatched {
		t.Fatalf("unexpected result %+v", got)
	}
	if !got.ScannedAt.Equal(scannedAt) || got.Error != "" || !strings.HasPrefix(got.Excerpt, "Juniper Valley") {
		t.Fatalf("unexpected result %+v", got)
	}
}

func TestTheOwnerNameIsExpectedWhenTheHostListsWithPermission(t *testing.T) {
	proof := phoenixProof("Maria Lopez")
	store := &storeStub{due: []Due{proof}}
	service := newTestService(store, map[string][]byte{proof.ObjectKey: []byte(utilityBill)}, extractorStub{})

	if _, err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.recorded[0].NameVerdict; got != NameNotFound {
		t.Fatalf("expected the owner's name to be missing, got %q", got)
	}
}

func TestAnUnreadableFileIsRecordedWithItsError(t *testing.T) {
	proof := phoenixProof("")
	store := &storeStub{due: []Due{proof}}
	service := newTestService(store, map[string][]byte{proof.ObjectKey: []byte("x")}, extractorStub{err: ErrUnsupportedType})

	if _, err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store.recorded[0]
	if got.Error != ErrUnsupportedType.Error() || got.NameVerdict != NameNotFound || got.Excerpt != "" {
		t.Fatalf("unexpected result %+v", got)
	}
}

func TestADocumentThatCannotBeDownloadedIsRetriedLater(t *testing.T) {
	store := &storeStub{due: []Due{phoenixProof("")}}
	service := newTestService(store, map[string][]byte{}, extractorStub{})

	scanned, err := service.RunOnce(context.Background())
	if err != nil || scanned != 0 || len(store.recorded) != 0 {
		t.Fatalf("scanned %d, recorded %d, err %v", scanned, len(store.recorded), err)
	}
}

func TestTheExcerptIsShortAndOnOneLine(t *testing.T) {
	long := strings.Repeat("word\n", 400)
	if got := excerptOf(long); len([]rune(got)) != excerptRunes || strings.Contains(got, "\n") {
		t.Fatalf("excerpt was %d runes", len([]rune(got)))
	}
}

func TestTheJobIsOffUnlessTheFlagIsTrue(t *testing.T) {
	cases := map[string]bool{"": false, "false": false, "1": false, "true": true, " TRUE ": true}
	for value, want := range cases {
		lookup := func(string) string { return value }
		if got := Enabled(lookup); got != want {
			t.Fatalf("Enabled(%q) = %v, want %v", value, got, want)
		}
	}
}
