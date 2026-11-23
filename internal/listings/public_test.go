package listings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type publishedReaderStub struct {
	listings []PublicListing
	err      error
}

func (stub *publishedReaderStub) ListPublished(context.Context) ([]PublicListing, error) {
	return stub.listings, stub.err
}

func (stub *publishedReaderStub) GetPublished(_ context.Context, id uuid.UUID) (PublicListing, error) {
	if stub.err != nil {
		return PublicListing{}, stub.err
	}
	for _, listing := range stub.listings {
		if listing.ID == id {
			return listing, nil
		}
	}
	return PublicListing{}, ErrListingNotFound
}

type photoSignerStub struct{ err error }

func (stub photoSignerStub) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	if stub.err != nil {
		return "", stub.err
	}
	return "https://storage.test/" + key + "?sig=1", nil
}

func sampleListing() PublicListing {
	return PublicListing{
		ID:    uuid.New(),
		Title: "Beach Breeze Inn",
		Location: PublicLocation{
			City: "Pompano Beach", Region: "Florida", CountryCode: "US",
			Latitude: 26.237891, Longitude: -80.124567,
		},
		Photos: []ListingPhoto{
			{ID: uuid.New(), ObjectKey: "listings/a/0.jpg", Position: 0},
			{ID: uuid.New(), ObjectKey: "listings/a/1.jpg", Position: 1},
		},
	}
}

func TestListPublishedSignsEveryPhotoURL(t *testing.T) {
	reader := &publishedReaderStub{listings: []PublicListing{sampleListing()}}
	service := NewPublicListingService(reader, photoSignerStub{})

	result, err := service.ListPublished(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for _, photo := range result[0].Photos {
		if !strings.HasPrefix(photo.URL, "https://storage.test/listings/a/") {
			t.Fatalf("photo %d was not signed: %q", photo.Position, photo.URL)
		}
	}
}

func TestPublicListingsRoundCoordinatesToHideExactAddress(t *testing.T) {
	reader := &publishedReaderStub{listings: []PublicListing{sampleListing()}}
	service := NewPublicListingService(reader, photoSignerStub{})

	result, err := service.ListPublished(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if result[0].Location.Latitude != 26.24 || result[0].Location.Longitude != -80.12 {
		t.Fatalf("coordinates not rounded: %v, %v", result[0].Location.Latitude, result[0].Location.Longitude)
	}
}

func TestGetPublishedReturnsNotFoundForUnknownListing(t *testing.T) {
	service := NewPublicListingService(&publishedReaderStub{}, photoSignerStub{})

	_, err := service.GetPublished(context.Background(), uuid.New())

	if !errors.Is(err, ErrListingNotFound) {
		t.Fatalf("expected ErrListingNotFound, got %v", err)
	}
}

func TestListPublishedFailsWhenPhotoSigningFails(t *testing.T) {
	reader := &publishedReaderStub{listings: []PublicListing{sampleListing()}}
	service := NewPublicListingService(reader, photoSignerStub{err: errors.New("no credentials")})

	if _, err := service.ListPublished(context.Background()); err == nil {
		t.Fatal("expected a signing failure to surface")
	}
}

func TestListPublishedWithoutSignerReturnsListingsWithoutPhotoURLs(t *testing.T) {
	reader := &publishedReaderStub{listings: []PublicListing{sampleListing()}}
	service := NewPublicListingService(reader, nil)

	result, err := service.ListPublished(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 1 || result[0].Photos[0].URL != "" {
		t.Fatalf("expected unsigned photos, got %+v", result)
	}
}

func TestPublicListHTTPNeedsNoAuthenticationAndHidesObjectKeys(t *testing.T) {
	reader := &publishedReaderStub{listings: []PublicListing{sampleListing()}}
	handler := publicHandler(NewPublicListingService(reader, photoSignerStub{}))
	response := httptest.NewRecorder()

	handler.ListListings(response, httptest.NewRequest(http.MethodGet, "/api/v1/listings", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "object_key") || strings.Contains(response.Body.String(), "listings/a/0.jpg?") == false {
		t.Fatalf("response leaked an object key or missed the signed url: %s", response.Body.String())
	}
	var body struct {
		Listings []json.RawMessage `json:"listings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Listings) != 1 {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
}

func TestPublicGetHTTPRejectsMalformedID(t *testing.T) {
	handler := publicHandler(NewPublicListingService(&publishedReaderStub{}, photoSignerStub{}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/listings/not-a-uuid", nil)
	request.SetPathValue("listing_id", "not-a-uuid")
	response := httptest.NewRecorder()

	handler.GetListing(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestPublicGetHTTPReturns404ForUnknownListing(t *testing.T) {
	handler := publicHandler(NewPublicListingService(&publishedReaderStub{}, photoSignerStub{}))
	id := uuid.New().String()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/listings/"+id, nil)
	request.SetPathValue("listing_id", id)
	response := httptest.NewRecorder()

	handler.GetListing(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

func TestPublicGetHTTPReturnsSignedListing(t *testing.T) {
	listing := sampleListing()
	handler := publicHandler(NewPublicListingService(&publishedReaderStub{listings: []PublicListing{listing}}, photoSignerStub{}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/listings/"+listing.ID.String(), nil)
	request.SetPathValue("listing_id", listing.ID.String())
	response := httptest.NewRecorder()

	handler.GetListing(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "sig=1") {
		t.Fatalf("expected 200 with signed photo, got %d: %s", response.Code, response.Body.String())
	}
}

func publicHandler(reader PublicListingReader) *HTTPHandler {
	return NewHTTPHandler(nil).WithPublicReader(reader)
}
