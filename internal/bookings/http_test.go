package bookings

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type creatorStub struct {
	booking Booking
	err     error
}

func (stub creatorStub) CreateBooking(context.Context, uuid.UUID, CreateBookingInput) (Booking, error) {
	return stub.booking, stub.err
}

func TestCreateBookingRejectsUnauthenticatedRequests(t *testing.T) {
	handler := NewHTTPHandler(creatorStub{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/bookings", bytes.NewReader([]byte(`{}`)))
	response := httptest.NewRecorder()

	handler.CreateBooking(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func authenticatedRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/bookings", bytes.NewReader([]byte(body)))
	return request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
}

func TestCreateBookingReturnsCreatedBooking(t *testing.T) {
	created := Booking{ID: uuid.New(), Status: StatusPending}
	handler := NewHTTPHandler(creatorStub{booking: created})

	request := authenticatedRequest(`{"listing_id":"` + uuid.New().String() + `","starts_on":"2026-06-10","ends_on":"2026-06-15"}`)
	response := httptest.NewRecorder()

	handler.CreateBooking(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
}

func TestCreateBookingRejectsMalformedDates(t *testing.T) {
	handler := NewHTTPHandler(creatorStub{})

	request := authenticatedRequest(`{"listing_id":"` + uuid.New().String() + `","starts_on":"not-a-date","ends_on":"2026-06-15"}`)
	response := httptest.NewRecorder()

	handler.CreateBooking(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestCreateBookingRejectsOversizedBody(t *testing.T) {
	handler := NewHTTPHandler(creatorStub{})

	oversized := `{"listing_id":"` + uuid.New().String() + `","starts_on":"` + strings.Repeat("2", 2<<20) + `","ends_on":"2026-06-15"}`
	request := authenticatedRequest(oversized)
	response := httptest.NewRecorder()

	handler.CreateBooking(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a body over the byte limit, got %d", response.Code)
	}
}

func TestCreateBookingRejectsUnknownFields(t *testing.T) {
	handler := NewHTTPHandler(creatorStub{})

	request := authenticatedRequest(`{"listing_id":"` + uuid.New().String() + `","starts_on":"2026-06-10","ends_on":"2026-06-15","unexpected":"field"}`)
	response := httptest.NewRecorder()

	handler.CreateBooking(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown field, got %d", response.Code)
	}
}

func TestCreateBookingMapsErrorsToStatusCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid", err: ErrInvalidBooking, want: http.StatusBadRequest},
		{name: "overlap", err: ErrBookingOverlaps, want: http.StatusConflict},
		{name: "verification required", err: ErrVerificationRequired, want: http.StatusForbidden},
		{name: "unexpected", err: context.DeadlineExceeded, want: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(creatorStub{err: test.err})
			request := authenticatedRequest(`{"listing_id":"` + uuid.New().String() + `","starts_on":"2026-06-10","ends_on":"2026-06-15"}`)
			response := httptest.NewRecorder()

			handler.CreateBooking(response, request)

			if response.Code != test.want {
				t.Fatalf("expected %d, got %d", test.want, response.Code)
			}
		})
	}
}

func TestCreateBookingVerificationRequiredResponseCarriesCode(t *testing.T) {
	handler := NewHTTPHandler(creatorStub{err: ErrVerificationRequired})
	request := authenticatedRequest(`{"listing_id":"` + uuid.New().String() + `","starts_on":"2026-06-10","ends_on":"2026-06-15"}`)
	response := httptest.NewRecorder()

	handler.CreateBooking(response, request)

	if !strings.Contains(response.Body.String(), `"code":"`+VerificationRequiredCode+`"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}
