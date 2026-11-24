package nearby

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testUserAgent = "BetterGuests-Test/1.0"
	testAPIKey    = "secret-test-key"
)

func geoapifyAt(t *testing.T, handler http.HandlerFunc) *GeoapifySource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewGeoapifySource(GeoapifyConfig{BaseURL: server.URL, APIKey: testAPIKey, UserAgent: testUserAgent}, server.Client())
}

func TestGeoapifyRequestAsksForNamedPlacesInACircleNearestFirst(t *testing.T) {
	var request *http.Request
	source := geoapifyAt(t, func(w http.ResponseWriter, r *http.Request) {
		request = r.Clone(context.Background())
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
	})

	if _, err := source.Find(context.Background(), categories["drinks"], home); err != nil {
		t.Fatal(err)
	}

	if request.Method != http.MethodGet || request.URL.Path != "/v2/places" {
		t.Fatalf("request = %s %s", request.Method, request.URL.Path)
	}
	if got := request.Header.Get("User-Agent"); got != testUserAgent {
		t.Fatalf("User-Agent = %q", got)
	}
	query := request.URL.Query()
	want := map[string]string{
		"categories": "catering.bar,catering.pub,catering.biergarten,catering.taproom,adult.nightclub,production.brewery",
		"filter":     "circle:-156.45,20.7,8000",
		"bias":       "proximity:-156.45,20.7",
		"conditions": "named",
		"limit":      "20",
		"lang":       "en",
		"apiKey":     testAPIKey,
	}
	for name, value := range want {
		if got := query.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
}

func TestGeoapifyMapsFeaturesToPlaces(t *testing.T) {
	source := geoapifyAt(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[
			{"type":"Feature","properties":{"place_id":"abc","name":"Tiki Bar","lat":20.71,"lon":-156.45,"street":"Kihei Road","city":"Kihei","categories":["catering","catering.bar"]},"geometry":{"type":"Point","coordinates":[-156.45,20.71]}},
			{"type":"Feature","properties":{"place_id":"def","name":"Fast Bite","categories":["catering","catering.fast_food"]},"geometry":{"type":"Point","coordinates":[-156.46,20.72]}},
			{"type":"Feature","properties":{"place_id":"ghi","name":"Mystery"},"geometry":{"type":"Point","coordinates":[-156.47,20.73]}}
		]}`))
	})

	places, err := source.Find(context.Background(), categories["drinks"], home)
	if err != nil {
		t.Fatal(err)
	}

	want := []Place{
		{ID: "abc", Name: "Tiki Bar", Detail: "Bar · Kihei Road · Kihei", Latitude: 20.71, Longitude: -156.45},
		{ID: "def", Name: "Fast Bite", Detail: "Fast food", Latitude: 20.72, Longitude: -156.46},
		{ID: "ghi", Name: "Mystery", Detail: "", Latitude: 20.73, Longitude: -156.47},
	}
	if len(places) != len(want) {
		t.Fatalf("got %d places (%+v), want %d", len(places), places, len(want))
	}
	for index := range want {
		if places[index] != want[index] {
			t.Errorf("place %d = %+v, want %+v", index, places[index], want[index])
		}
	}
}

func TestGeoapifySkipsFeaturesWithoutANameAnIDOrAPosition(t *testing.T) {
	source := geoapifyAt(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"features":[
			{"properties":{"place_id":"a","lat":1,"lon":1}},
			{"properties":{"name":"No id","lat":1,"lon":1}},
			{"properties":{"place_id":"c","name":"Nowhere"}},
			{"properties":{"place_id":"d","name":"Fine","lat":1,"lon":2}}
		]}`))
	})

	places, err := source.Find(context.Background(), categories["drinks"], home)
	if err != nil {
		t.Fatal(err)
	}

	if len(places) != 1 || places[0].ID != "d" {
		t.Fatalf("places = %+v, want only d", places)
	}
}

func TestGeoapifyFailuresAreReportedAsUnavailable(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"bad key":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"over quota": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) },
		"server":     func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"garbled":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := geoapifyAt(t, handler).Find(context.Background(), categories["drinks"], home)
			if !errors.Is(err, ErrSourceUnavailable) {
				t.Fatalf("err = %v, want ErrSourceUnavailable", err)
			}
			if strings.Contains(err.Error(), testAPIKey) {
				t.Fatalf("error leaks the API key: %v", err)
			}
		})
	}
}

func TestGeoapifyUnreachableHostErrorDoesNotLeakTheAPIKey(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	source := NewGeoapifySource(GeoapifyConfig{BaseURL: url, APIKey: testAPIKey, UserAgent: testUserAgent}, nil)

	_, err := source.Find(context.Background(), categories["drinks"], home)

	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("err = %v, want ErrSourceUnavailable", err)
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

func TestGeoapifyGivesUpOnASlowAnswer(t *testing.T) {
	source := geoapifyAt(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	source.timeout = 100 * time.Millisecond

	started := time.Now()
	_, err := source.Find(context.Background(), categories["drinks"], home)

	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("err = %v, want ErrSourceUnavailable", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("took %s; the slow answer was not cut off", elapsed)
	}
}

func TestGeoapifyWithoutAKeyNeverCallsOut(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(server.Close)
	source := NewGeoapifySource(GeoapifyConfig{BaseURL: server.URL, UserAgent: testUserAgent}, server.Client())

	if _, err := source.Find(context.Background(), categories["drinks"], home); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("err = %v, want ErrSourceUnavailable", err)
	}
	if called {
		t.Fatal("a request was sent without an API key")
	}
}

func TestGeoapifyAcceptsNumericNamesAndOtherTextFields(t *testing.T) {
	source := geoapifyAt(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"features":[
			{"properties":{"place_id":"n","name":76,"street":12,"city":null,"lat":1,"lon":2,"categories":["service","service.vehicle.fuel"]}},
			{"properties":{"place_id":"o","name":{"en":"odd"},"lat":1,"lon":2}}
		]}`))
	})

	places, err := source.Find(context.Background(), categories["gas"], home)
	if err != nil {
		t.Fatal(err)
	}

	if len(places) != 1 || places[0].Name != "76" || places[0].Detail != "Fuel · 12" {
		t.Fatalf("places = %+v, want one place named 76 with detail %q", places, "Fuel · 12")
	}
}
