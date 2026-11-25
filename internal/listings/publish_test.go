package listings

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type publishStoreStub struct {
	userID    string
	listingID uuid.UUID
	calls     int
	err       error
}

func (store *publishStoreStub) Publish(_ context.Context, userID string, listingID uuid.UUID) error {
	store.calls++
	store.userID = userID
	store.listingID = listingID
	return store.err
}

type hostVerifierStub struct {
	verified bool
	err      error
}

func (verifier hostVerifierStub) HasCompleted(context.Context, uuid.UUID) (bool, error) {
	return verifier.verified, verifier.err
}

func TestPublishListing(t *testing.T) {
	hostID := uuid.New()
	listingID := uuid.New()
	storeFailure := errors.New("store down")
	verifierFailure := errors.New("verifier down")

	cases := []struct {
		name        string
		verifier    HostVerifier
		storeErr    error
		wantErr     error
		wantPublish bool
	}{
		{name: "verified host publishes", verifier: hostVerifierStub{verified: true}, wantPublish: true},
		{name: "unverified host is refused before touching the store", verifier: hostVerifierStub{}, wantErr: ErrVerificationRequired},
		{name: "missing verifier fails closed", verifier: nil, wantErr: ErrVerificationRequired},
		{name: "verifier failure is returned", verifier: hostVerifierStub{err: verifierFailure}, wantErr: verifierFailure},
		{name: "duplicate address surfaces as conflict", verifier: hostVerifierStub{verified: true}, storeErr: ErrDuplicateListing, wantErr: ErrDuplicateListing, wantPublish: true},
		{name: "store failure is returned", verifier: hostVerifierStub{verified: true}, storeErr: storeFailure, wantErr: storeFailure, wantPublish: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &publishStoreStub{err: testCase.storeErr}
			service := NewListingService(&listingStoreStub{}).WithPublishing(store, testCase.verifier)

			err := service.PublishListing(context.Background(), hostID, listingID)

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
			if (store.calls == 1) != testCase.wantPublish {
				t.Fatalf("store calls = %d, want publish %v", store.calls, testCase.wantPublish)
			}
			if testCase.wantPublish && (store.userID != hostID.String() || store.listingID != listingID) {
				t.Fatal("publish did not run as the authenticated host")
			}
		})
	}
}

type listingPublisherStub struct{ err error }

func (stub listingPublisherStub) PublishListing(context.Context, uuid.UUID, uuid.UUID) error {
	return stub.err
}

func TestPublishListingHTTPStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"published", nil, http.StatusOK},
		{"missing listing", ErrListingNotFound, http.StatusNotFound},
		{"not the owner", ErrNotListingOwner, http.StatusForbidden},
		{"host not verified", ErrVerificationRequired, http.StatusForbidden},
		{"proof missing", ErrProofRequired, http.StatusForbidden},
		{"duplicate address", ErrDuplicateListing, http.StatusConflict},
		{"unexpected failure", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler := NewHTTPHandler(&listingCreatorStub{}).WithPublisher(listingPublisherStub{err: testCase.err})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/listings/x/publish", nil)
			request.SetPathValue("listing_id", uuid.NewString())
			request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
			response := httptest.NewRecorder()

			handler.PublishListing(response, request)

			if response.Code != testCase.want {
				t.Fatalf("expected %d, got %d: %s", testCase.want, response.Code, response.Body.String())
			}
		})
	}
}

func TestPublishListingHTTPRequiresAuthentication(t *testing.T) {
	handler := NewHTTPHandler(&listingCreatorStub{}).WithPublisher(listingPublisherStub{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/listings/x/publish", nil)
	request.SetPathValue("listing_id", uuid.NewString())
	response := httptest.NewRecorder()

	handler.PublishListing(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestPublishListingHTTPRejectsMalformedListingID(t *testing.T) {
	handler := NewHTTPHandler(&listingCreatorStub{}).WithPublisher(listingPublisherStub{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/listings/x/publish", nil)
	request.SetPathValue("listing_id", "not-a-uuid")
	request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
	response := httptest.NewRecorder()

	handler.PublishListing(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}
