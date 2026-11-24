package nearby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type searcherStub struct {
	places []Place
	err    error
	listID uuid.UUID
	catID  string
}

func (stub *searcherStub) Search(_ context.Context, listingID uuid.UUID, categoryID string) ([]Place, error) {
	stub.listID = listingID
	stub.catID = categoryID
	return stub.places, stub.err
}

func serve(stub *searcherStub, target string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/listings/{listing_id}/nearby", NewHTTPHandler(stub).Search)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

func TestSearchHandlerReturnsPlacesAsJSON(t *testing.T) {
	listing := uuid.New()
	stub := &searcherStub{places: []Place{{ID: "N1", Name: "Tiki Bar", Detail: "Bar", Latitude: 20.71, Longitude: -156.45, DistanceKm: 1.2}}}

	response := serve(stub, "/api/v1/listings/"+listing.String()+"/nearby?category=drinks")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if stub.listID != listing || stub.catID != "drinks" {
		t.Fatalf("searcher got %v %q", stub.listID, stub.catID)
	}
	var body struct {
		Category string `json:"category"`
		Places   []struct {
			ID         string  `json:"id"`
			Name       string  `json:"name"`
			Detail     string  `json:"detail"`
			Lat        float64 `json:"lat"`
			Lon        float64 `json:"lon"`
			DistanceKm float64 `json:"distance_km"`
		} `json:"places"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Category != "drinks" || len(body.Places) != 1 || body.Places[0].Name != "Tiki Bar" || body.Places[0].Lat != 20.71 || body.Places[0].DistanceKm != 1.2 {
		t.Fatalf("body = %+v", body)
	}
}

func TestSearchHandlerReturnsAnEmptyListNotNull(t *testing.T) {
	response := serve(&searcherStub{}, "/api/v1/listings/"+uuid.NewString()+"/nearby?category=drinks")

	var body map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if string(body["places"]) != "[]" {
		t.Fatalf("places = %s, want []", body["places"])
	}
}

func TestSearchHandlerMapsFailuresToStatusCodes(t *testing.T) {
	cases := []struct {
		name   string
		target string
		err    error
		status int
	}{
		{"bad listing id", "/api/v1/listings/not-a-uuid/nearby?category=drinks", nil, http.StatusBadRequest},
		{"unknown category", "/api/v1/listings/" + uuid.NewString() + "/nearby?category=zzz", ErrUnknownCategory, http.StatusBadRequest},
		{"missing category", "/api/v1/listings/" + uuid.NewString() + "/nearby", ErrUnknownCategory, http.StatusBadRequest},
		{"missing listing", "/api/v1/listings/" + uuid.NewString() + "/nearby?category=drinks", ErrListingNotFound, http.StatusNotFound},
		{"source down", "/api/v1/listings/" + uuid.NewString() + "/nearby?category=drinks", ErrSourceUnavailable, http.StatusBadGateway},
		{"unexpected", "/api/v1/listings/" + uuid.NewString() + "/nearby?category=drinks", context.DeadlineExceeded, http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := serve(&searcherStub{err: testCase.err}, testCase.target)
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d", response.Code, testCase.status)
			}
			var body map[string]string
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["error"] == "" {
				t.Fatalf("error body = %v (%v)", body, err)
			}
		})
	}
}
