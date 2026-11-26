package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/nearby"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/google/uuid"
)

type stubPinger struct {
	err error
}

func (p stubPinger) Ping(context.Context) error {
	return p.err
}

func TestHealthIsIndependentOfDatabase(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()

	handler := routes(routeDeps{
		database: stubPinger{err: errors.New("database unavailable")},
		origins:  []string{"http://localhost:3000"},
	})

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
}

func TestServerHasDefensiveTimeouts(t *testing.T) {
	server, err := createServer(http.NewServeMux())
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("unexpected read-header timeout: %s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 15*time.Second {
		t.Fatalf("unexpected read timeout: %s", server.ReadTimeout)
	}
	if server.WriteTimeout != 30*time.Second {
		t.Fatalf("unexpected write timeout: %s", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("unexpected idle timeout: %s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("unexpected maximum header size: %d", server.MaxHeaderBytes)
	}
}

func TestReadinessChecksDatabase(t *testing.T) {
	tests := []struct {
		name       string
		pingError  error
		wantStatus int
	}{
		{name: "ready", wantStatus: http.StatusOK},
		{
			name:       "database unavailable",
			pingError:  errors.New("unavailable"),
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/ready", nil)
			response := httptest.NewRecorder()

			handler := routes(routeDeps{
				database: stubPinger{err: test.pingError},
				origins:  []string{"http://localhost:3000"},
			})

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf(
					"expected status %d, got %d",
					test.wantStatus,
					response.Code,
				)
			}
		})
	}
}

type stubVerification struct {
	started   int
	processed int
}

func (stub *stubVerification) StartVerification(context.Context, uuid.UUID, bool) (verification.StartResult, error) {
	stub.started++
	return verification.StartResult{InquiryID: "inq_1", SessionToken: "tok"}, nil
}

func (stub *stubVerification) HandleWebhook(context.Context, string, []byte) error {
	stub.processed++
	return nil
}

func rejectAll(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
}

func TestPersonaWebhookRouteIsPublic(t *testing.T) {
	stub := &stubVerification{}
	handler := routes(routeDeps{
		database:     stubPinger{},
		origins:      []string{"http://localhost:3000"},
		verification: verification.NewHTTPHandler(stub, stub),
		authenticate: rejectAll,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/persona", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || stub.processed != 1 {
		t.Fatalf("status = %d, processed = %d", response.Code, stub.processed)
	}
}

func TestStartVerificationRouteRequiresAuthentication(t *testing.T) {
	stub := &stubVerification{}
	handler := routes(routeDeps{
		database:     stubPinger{},
		origins:      []string{"http://localhost:3000"},
		verification: verification.NewHTTPHandler(stub, stub),
		authenticate: rejectAll,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/verification/inquiries", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || stub.started != 0 {
		t.Fatalf("status = %d, started = %d", response.Code, stub.started)
	}
}

func TestVerificationRoutesAreAbsentWithoutHandler(t *testing.T) {
	handler := routes(routeDeps{database: stubPinger{}, origins: []string{"http://localhost:3000"}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/persona", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

type nearbySearcherStub struct{}

func (nearbySearcherStub) Search(context.Context, uuid.UUID, string) ([]nearby.Place, error) {
	return []nearby.Place{{ID: "N1", Name: "Tiki Bar"}}, nil
}

type listingReaderStub struct {
	listing listings.PublicListing
	err     error
}

func (stub listingReaderStub) GetPublished(context.Context, uuid.UUID) (listings.PublicListing, error) {
	return stub.listing, stub.err
}

func TestNearbyRouteIsPublic(t *testing.T) {
	handler := routes(routeDeps{
		database: stubPinger{},
		origins:  []string{"http://localhost:3000"},
		nearby:   nearby.NewHTTPHandler(nearbySearcherStub{}),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/listings/"+uuid.NewString()+"/nearby?category=drinks", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200 without credentials, got %d", response.Code)
	}
}

func TestNearbyRouteIsAbsentWithoutHandler(t *testing.T) {
	handler := routes(routeDeps{database: stubPinger{}, origins: []string{"http://localhost:3000"}})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/listings/"+uuid.NewString()+"/nearby?category=drinks", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", response.Code)
	}
}

func TestListingLocationsMapNotFoundAndKeepPublicCoordinates(t *testing.T) {
	missing := listingLocations{reader: listingReaderStub{err: listings.ErrListingNotFound}}
	if _, err := missing.Location(context.Background(), uuid.New()); !errors.Is(err, nearby.ErrListingNotFound) {
		t.Fatalf("expected nearby.ErrListingNotFound, got %v", err)
	}

	present := listingLocations{reader: listingReaderStub{listing: listings.PublicListing{
		Location: listings.PublicLocation{Latitude: 20.71, Longitude: -156.45},
	}}}
	point, err := present.Location(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if point.Latitude != 20.71 || point.Longitude != -156.45 {
		t.Fatalf("unexpected point %+v", point)
	}
}

type stubIDDeleter struct{ calls int }

func (stub *stubIDDeleter) DeleteIDData(context.Context, uuid.UUID) (verification.DeletionResult, error) {
	stub.calls++
	return verification.DeletionResult{}, nil
}

func TestDeleteIDDataRouteRequiresAuthentication(t *testing.T) {
	stub := &stubVerification{}
	deleter := &stubIDDeleter{}
	handler := routes(routeDeps{
		database:     stubPinger{},
		origins:      []string{"http://localhost:3000"},
		verification: verification.NewHTTPHandler(stub, stub).WithDeletion(deleter),
		authenticate: rejectAll,
	})
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/verification/data", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || deleter.calls != 0 {
		t.Fatalf("status = %d, calls = %d", response.Code, deleter.calls)
	}
}

func TestDeleteIDDataRouteIsReachedOnlyByDelete(t *testing.T) {
	stub := &stubVerification{}
	deleter := &stubIDDeleter{}
	handler := routes(routeDeps{
		database:     stubPinger{},
		origins:      []string{"http://localhost:3000"},
		verification: verification.NewHTTPHandler(stub, stub).WithDeletion(deleter),
		authenticate: func(next http.Handler) http.Handler { return next },
	})
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch} {
		request := httptest.NewRequest(method, "/api/v1/verification/data", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusOK || deleter.calls != 0 {
			t.Fatalf("%s status = %d calls = %d", method, response.Code, deleter.calls)
		}
	}
}

func TestRateLimitedResponsesStillCarryCORSHeaders(t *testing.T) {
	const origin = "https://app.example.test"
	handler := withMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), []string{origin})

	var last *httptest.ResponseRecorder
	for range 200 {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/listings", nil)
		r.Header.Set("Origin", origin)
		r.RemoteAddr = "203.0.113.9:1234"
		last = httptest.NewRecorder()
		handler.ServeHTTP(last, r)
		if last.Code == http.StatusTooManyRequests {
			break
		}
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("expected the limit to be reached, last status %d", last.Code)
	}
	if last.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Fatal("a refused request must still carry CORS headers so the browser can read the 429")
	}
}

func TestPreflightsAreCachedAndDoNotSpendTheRateLimit(t *testing.T) {
	const origin = "https://app.example.test"
	handler := withMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), []string{origin})

	for range 500 {
		r := httptest.NewRequest(http.MethodOptions, "/api/v1/conversations", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", http.MethodGet)
		r.Header.Set("Access-Control-Request-Headers", "authorization")
		r.RemoteAddr = "203.0.113.10:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code == http.StatusTooManyRequests {
			t.Fatal("preflight checks must not count against the rate limit")
		}
		if response.Header().Get("Access-Control-Max-Age") != strconv.Itoa(preflightCacheSeconds) {
			t.Fatalf("preflight must be cacheable, max-age = %q", response.Header().Get("Access-Control-Max-Age"))
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil)
	r.Header.Set("Origin", origin)
	r.RemoteAddr = "203.0.113.10:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	if response.Code != http.StatusOK {
		t.Fatalf("a real request after many preflights must still go through, got %d", response.Code)
	}
}
