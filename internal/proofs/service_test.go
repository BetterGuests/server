package proofs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type storeStub struct {
	owner     uuid.UUID
	ownerErr  error
	saved     []Proof
	saveErr   error
	admin     bool
	items     []ReviewItem
	decided   Status
	reason    string
	decideErr error
}

func (store *storeStub) ListingHost(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, error) {
	return store.owner, store.ownerErr
}

func (store *storeStub) Save(_ context.Context, proof Proof) error {
	store.saved = append(store.saved, proof)
	return store.saveErr
}

func (store *storeStub) Latest(context.Context, uuid.UUID, uuid.UUID) (Proof, bool, error) {
	return Proof{}, false, nil
}

func (store *storeStub) IsAdmin(context.Context, uuid.UUID) (bool, error) { return store.admin, nil }

func (store *storeStub) ListSubmitted(context.Context, uuid.UUID) ([]ReviewItem, error) {
	return store.items, nil
}

func (store *storeStub) ListApproved(context.Context, uuid.UUID) ([]ReviewItem, error) {
	return store.items, nil
}

func (store *storeStub) Revoke(_ context.Context, _, _ uuid.UUID, reason string, _ time.Time) error {
	store.reason = reason
	return store.decideErr
}

func (store *storeStub) Decide(_ context.Context, _, _ uuid.UUID, status Status, reason string, _ time.Time) error {
	store.decided, store.reason = status, reason
	return store.decideErr
}

type objectsStub struct {
	info       ObjectInfo
	statErr    error
	uploadKey  string
	uploadType string
	removed    []string
}

func (objects *objectsStub) PresignUpload(_ context.Context, key, contentType string, _ time.Duration) (string, error) {
	objects.uploadKey, objects.uploadType = key, contentType
	return "https://storage.test/upload/" + key, nil
}

func (objects *objectsStub) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.test/download/" + key, nil
}

func (objects *objectsStub) Stat(context.Context, string) (ObjectInfo, error) {
	return objects.info, objects.statErr
}

func (objects *objectsStub) Remove(_ context.Context, key string) error {
	objects.removed = append(objects.removed, key)
	return nil
}

func ownedBy(hostID uuid.UUID) *storeStub { return &storeStub{owner: hostID} }

func TestRequestUpload(t *testing.T) {
	hostID, listingID := uuid.New(), uuid.New()
	cases := []struct {
		name        string
		store       *storeStub
		objects     ObjectStore
		contentType string
		size        int64
		wantErr     error
	}{
		{"pdf is accepted", ownedBy(hostID), &objectsStub{}, "application/pdf", 1000, nil},
		{"image is accepted", ownedBy(hostID), &objectsStub{}, "image/jpeg", 1000, nil},
		{"executables are refused", ownedBy(hostID), &objectsStub{}, "application/x-msdownload", 1000, ErrDocumentRejected},
		{"svg is refused", ownedBy(hostID), &objectsStub{}, "image/svg+xml", 1000, ErrDocumentRejected},
		{"empty file is refused", ownedBy(hostID), &objectsStub{}, "application/pdf", 0, ErrDocumentRejected},
		{"oversized file is refused", ownedBy(hostID), &objectsStub{}, "application/pdf", maxDocumentBytes + 1, ErrDocumentRejected},
		{"another host's listing is refused", ownedBy(uuid.New()), &objectsStub{}, "application/pdf", 1000, ErrNotListingOwner},
		{"missing listing is reported", &storeStub{ownerErr: ErrListingNotFound}, &objectsStub{}, "application/pdf", 1000, ErrListingNotFound},
		{"storage disabled", ownedBy(hostID), nil, "application/pdf", 1000, ErrUploadsDisabled},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service := NewProofService(testCase.store, testCase.objects)

			ticket, err := service.RequestUpload(context.Background(), hostID, listingID, testCase.contentType, testCase.size)

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
			if testCase.wantErr == nil {
				if !strings.HasPrefix(ticket.ObjectKey, "property-proofs/"+listingID.String()+"/") {
					t.Fatalf("key is not scoped to the listing: %s", ticket.ObjectKey)
				}
				if ticket.ExpiresIn != 300 || ticket.MaxBytes != maxDocumentBytes {
					t.Fatalf("unexpected ticket limits: %+v", ticket)
				}
			}
		})
	}
}

func TestSubmit(t *testing.T) {
	hostID, listingID := uuid.New(), uuid.New()
	validKey := keyPrefix(listingID) + uuid.NewString() + ".pdf"
	otherListingKey := keyPrefix(uuid.New()) + uuid.NewString() + ".pdf"
	traversalKey := keyPrefix(listingID) + "../" + uuid.NewString() + ".pdf"

	cases := []struct {
		name        string
		store       *storeStub
		objects     *objectsStub
		documentTyp DocumentType
		key         string
		wantErr     error
		wantSaved   int
		wantRemoved int
	}{
		{"valid upload is saved", ownedBy(hostID), &objectsStub{info: ObjectInfo{Size: 500, ContentType: "application/pdf"}}, Deed, validKey, nil, 1, 0},
		{"unknown document type", ownedBy(hostID), &objectsStub{}, "passport", validKey, ErrInvalidDocument, 0, 0},
		{"key from another listing", ownedBy(hostID), &objectsStub{}, Deed, otherListingKey, ErrInvalidObjectKey, 0, 0},
		{"path traversal in key", ownedBy(hostID), &objectsStub{}, Deed, traversalKey, ErrInvalidObjectKey, 0, 0},
		{"not the host", ownedBy(uuid.New()), &objectsStub{}, Deed, validKey, ErrNotListingOwner, 0, 0},
		{"nothing was uploaded", ownedBy(hostID), &objectsStub{statErr: errors.New("missing")}, Deed, validKey, ErrNotUploaded, 0, 0},
		{"wrong content type is deleted", ownedBy(hostID), &objectsStub{info: ObjectInfo{Size: 500, ContentType: "text/html"}}, Deed, validKey, ErrDocumentRejected, 0, 1},
		{"oversized upload is deleted", ownedBy(hostID), &objectsStub{info: ObjectInfo{Size: maxDocumentBytes + 1, ContentType: "application/pdf"}}, Deed, validKey, ErrDocumentRejected, 0, 1},
		{"pending proof surfaces as conflict", &storeStub{owner: hostID, saveErr: ErrProofPending}, &objectsStub{info: ObjectInfo{Size: 500, ContentType: "application/pdf"}}, Deed, validKey, ErrProofPending, 1, 0},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service := NewProofService(testCase.store, testCase.objects)

			proof, err := service.Submit(context.Background(), hostID, listingID, testCase.documentTyp, testCase.key)

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
			if len(testCase.store.saved) != testCase.wantSaved || len(testCase.objects.removed) != testCase.wantRemoved {
				t.Fatalf("saved %d removed %d", len(testCase.store.saved), len(testCase.objects.removed))
			}
			if testCase.wantErr == nil && (proof.Status != Submitted || proof.SizeBytes != 500) {
				t.Fatalf("unexpected proof: %+v", proof)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	adminID, proofID := uuid.New(), uuid.New()
	cases := []struct {
		name       string
		admin      bool
		decision   Decision
		reason     string
		wantErr    error
		wantStatus Status
		wantReason string
	}{
		{"admin approves and any reason is dropped", true, Approve, "ignored", nil, Approved, ""},
		{"admin rejects with a reason", true, Reject, "  Name does not match  ", nil, Rejected, "Name does not match"},
		{"rejection without a reason", true, Reject, "   ", ErrReasonRequired, "", ""},
		{"rejection reason too long", true, Reject, strings.Repeat("a", 501), ErrReasonRequired, "", ""},
		{"unknown decision", true, Decision("maybe"), "", ErrInvalidDecision, "", ""},
		{"non admin is refused before anything else", false, Approve, "", ErrNotAdmin, "", ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &storeStub{admin: testCase.admin}
			service := NewProofService(store, &objectsStub{})

			err := service.Decide(context.Background(), adminID, proofID, testCase.decision, testCase.reason)

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
			if store.decided != testCase.wantStatus || store.reason != testCase.wantReason {
				t.Fatalf("stored %q %q", store.decided, store.reason)
			}
		})
	}
}

func TestListSubmittedRequiresAdminAndSignsShortLivedLinks(t *testing.T) {
	adminID := uuid.New()
	items := []ReviewItem{{Proof: Proof{ObjectKey: "property-proofs/a/b.pdf"}}}

	if _, err := NewProofService(&storeStub{items: items}, &objectsStub{}).ListSubmitted(context.Background(), adminID); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("expected ErrNotAdmin, got %v", err)
	}

	listed, err := NewProofService(&storeStub{admin: true, items: items}, &objectsStub{}).ListSubmitted(context.Background(), adminID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed[0].DocumentURL, "download/property-proofs/a/b.pdf") {
		t.Fatalf("download link was not signed: %q", listed[0].DocumentURL)
	}
}
