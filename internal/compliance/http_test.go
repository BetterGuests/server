package compliance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

func authedRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	return request.WithContext(accounts.WithUserID(context.Background(), uuid.New()))
}

func newTestHandler(store *stubStore) *HTTPHandler {
	service := newTestService(store)
	return NewHTTPHandler(service, service, service)
}

func TestConfirmAgeStatusCodes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "adult", body: `{"date_of_birth":"1990-05-01"}`, want: http.StatusNoContent},
		{name: "minor", body: `{"date_of_birth":"2015-06-15"}`, want: http.StatusForbidden},
		{name: "invalid date", body: `{"date_of_birth":"nope"}`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"date_of_birth":"1990-05-01","role":"admin"}`, want: http.StatusBadRequest},
		{name: "two values", body: `{"date_of_birth":"1990-05-01"}{}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newTestHandler(&stubStore{}).ConfirmAge(recorder, authedRequest(http.MethodPost, "/", test.body))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

func TestMinorGetsUnderageCode(t *testing.T) {
	recorder := httptest.NewRecorder()
	newTestHandler(&stubStore{}).ConfirmAge(recorder, authedRequest(http.MethodPost, "/", `{"date_of_birth":"2015-06-15"}`))
	if !strings.Contains(recorder.Body.String(), CodeUnderage) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestRequireAdult(t *testing.T) {
	tests := []struct {
		name          string
		adult         bool
		authenticated bool
		want          int
	}{
		{name: "adult passes", adult: true, authenticated: true, want: http.StatusOK},
		{name: "unconfirmed blocked", adult: false, authenticated: true, want: http.StatusForbidden},
		{name: "anonymous blocked", authenticated: false, want: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
			handler := newTestHandler(&stubStore{hasAge: test.adult}).RequireAdult(next)
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			if test.authenticated {
				request = authedRequest(http.MethodPost, "/", "")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if test.want == http.StatusForbidden && !strings.Contains(recorder.Body.String(), CodeAgeConfirmationRequired) {
				t.Fatalf("body = %s", recorder.Body.String())
			}
		})
	}
}

func TestGrantConsentStatusCodes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "valid", body: `{"kind":"biometric","policy_version":"2026-11-22"}`, want: http.StatusNoContent},
		{name: "unknown kind", body: `{"kind":"marketing","policy_version":"1"}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newTestHandler(&stubStore{}).GrantConsent(recorder, authedRequest(http.MethodPost, "/", test.body))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}
