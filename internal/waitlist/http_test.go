package waitlist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubStore struct {
	added []Signup
	err   error
}

func (store *stubStore) Add(_ context.Context, signup Signup) error {
	store.added = append(store.added, signup)
	return store.err
}

func TestJoin(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		storeErr    error
		status      int
		wantEmail   string
		wantCountry string
	}{
		{name: "normalizes email and country", body: `{"email":"  Ana@Example.COM ","country":"fr"}`, status: http.StatusNoContent, wantEmail: "ana@example.com", wantCountry: "FR"},
		{name: "country is optional", body: `{"email":"ana@example.com"}`, status: http.StatusNoContent, wantEmail: "ana@example.com"},
		{name: "accepts the tor country code", body: `{"email":"ana@example.com","country":"T1"}`, status: http.StatusNoContent, wantEmail: "ana@example.com", wantCountry: "T1"},
		{name: "rejects empty email", body: `{"email":""}`, status: http.StatusBadRequest},
		{name: "rejects display name form", body: `{"email":"Ana <ana@example.com>"}`, status: http.StatusBadRequest},
		{name: "rejects domain without a dot", body: `{"email":"ana@localhost"}`, status: http.StatusBadRequest},
		{name: "rejects oversized email", body: `{"email":"` + strings.Repeat("a", 250) + `@example.com"}`, status: http.StatusBadRequest},
		{name: "rejects long country", body: `{"email":"ana@example.com","country":"FRA"}`, status: http.StatusBadRequest},
		{name: "rejects unknown fields", body: `{"email":"ana@example.com","admin":true}`, status: http.StatusBadRequest},
		{name: "rejects trailing data", body: `{"email":"ana@example.com"}{}`, status: http.StatusBadRequest},
		{name: "hides store failures", body: `{"email":"ana@example.com"}`, storeErr: errors.New("secret detail"), status: http.StatusInternalServerError, wantEmail: "ana@example.com"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := &stubStore{err: test.storeErr}
			handler := NewHTTPHandler(NewWaitlistService(store))
			response := httptest.NewRecorder()
			handler.Join(response, httptest.NewRequest(http.MethodPost, "/api/v1/waitlist", strings.NewReader(test.body)))

			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body)
			}
			if strings.Contains(response.Body.String(), "secret detail") {
				t.Fatalf("internal error leaked: %s", response.Body)
			}
			if test.wantEmail == "" {
				if len(store.added) != 0 {
					t.Fatalf("stored a rejected signup: %+v", store.added)
				}
				return
			}
			if len(store.added) != 1 || store.added[0].Email != test.wantEmail || store.added[0].Country != test.wantCountry {
				t.Fatalf("stored %+v", store.added)
			}
		})
	}
}
