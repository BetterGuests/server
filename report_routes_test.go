package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/reports"
	"github.com/google/uuid"
)

type reportDeskStub struct{ calls []string }

func (stub *reportDeskStub) File(context.Context, uuid.UUID, reports.Submission) (reports.Receipt, error) {
	stub.calls = append(stub.calls, "file")
	return reports.Receipt{ID: uuid.New()}, nil
}

func (stub *reportDeskStub) List(context.Context, uuid.UUID, bool) ([]reports.Report, error) {
	stub.calls = append(stub.calls, "list")
	return []reports.Report{}, nil
}

func (stub *reportDeskStub) Resolve(context.Context, uuid.UUID, uuid.UUID, reports.Decision) error {
	stub.calls = append(stub.calls, "resolve")
	return nil
}

func TestReportRoutesNeedASignInAndLimitHowOftenOneAccountFiles(t *testing.T) {
	stub := &reportDeskStub{}
	account := uuid.New()
	handler := routes(routeDeps{
		reports: reports.NewHTTPHandler(stub, stub),
		authenticate: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r.WithContext(accounts.WithUserID(r.Context(), account)))
			})
		},
	})
	send := func(method, path, body string, signedIn bool) int {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("X-Forwarded-Proto", "https")
		if signedIn {
			request.Header.Set("Authorization", "Bearer test")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}

	if got := send(http.MethodPost, "/api/v1/reports", `{}`, false); got != http.StatusUnauthorized {
		t.Fatalf("signed out = %d", got)
	}
	if got := send(http.MethodGet, "/api/v1/admin/user-reports", "", true); got != http.StatusOK {
		t.Fatalf("list = %d", got)
	}
	decision := "/api/v1/admin/user-reports/" + uuid.NewString() + "/decision"
	if got := send(http.MethodPost, decision, `{"outcome":"dismissed"}`, true); got != http.StatusNoContent {
		t.Fatalf("decision = %d", got)
	}
	for attempt := 1; attempt <= userReportBudget.RequestsPerWindow; attempt++ {
		if got := send(http.MethodPost, "/api/v1/reports", `{}`, true); got != http.StatusCreated {
			t.Fatalf("report %d = %d", attempt, got)
		}
	}
	if got := send(http.MethodPost, "/api/v1/reports", `{}`, true); got != http.StatusTooManyRequests {
		t.Fatalf("over the limit = %d", got)
	}
}
