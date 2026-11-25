package listings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type hostReaderStub struct{ askedFor uuid.UUID }

func (stub *hostReaderStub) ListForHost(_ context.Context, hostID uuid.UUID) ([]HostListing, error) {
	stub.askedFor = hostID
	return []HostListing{}, nil
}

func TestListHostListingsAsksOnlyForTheSignedInHost(t *testing.T) {
	reader := &hostReaderStub{}
	hostID := uuid.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/hosting/listings", nil)
	request = request.WithContext(accounts.WithUserID(request.Context(), hostID))
	response := httptest.NewRecorder()

	NewHTTPHandler(nil).WithHostReader(reader).ListHostListings(response, request)

	if response.Code != http.StatusOK || reader.askedFor != hostID {
		t.Fatalf("status = %d, asked for %s, want 200 for %s", response.Code, reader.askedFor, hostID)
	}
	if body := response.Body.String(); body != "{\"listings\":[]}\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestListHostListingsRequiresASignedInUser(t *testing.T) {
	response := httptest.NewRecorder()

	NewHTTPHandler(nil).WithHostReader(&hostReaderStub{}).ListHostListings(response, httptest.NewRequest(http.MethodGet, "/api/v1/hosting/listings", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
