package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/middleware"
	"github.com/google/uuid"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func requestAs(method string, accountID uuid.UUID, clientIP string) *http.Request {
	request := httptest.NewRequest(method, "/api/v1/listings", nil)
	request.Header.Set("X-Real-IP", clientIP)
	if accountID != uuid.Nil {
		request = request.WithContext(accounts.WithUserID(request.Context(), accountID))
	}
	return request
}

func send(handler http.Handler, request *http.Request) int {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code
}

func TestAccountLimitFollowsTheAccountAcrossIPs(t *testing.T) {
	handler := perAccount(middleware.RateLimiterConfig{RequestsPerWindow: 2, Window: day}, okHandler())
	bot := uuid.New()

	send(handler, requestAs(http.MethodPost, bot, "203.0.113.1"))
	send(handler, requestAs(http.MethodPost, bot, "203.0.113.2"))
	if code := send(handler, requestAs(http.MethodPost, bot, "203.0.113.3")); code != http.StatusTooManyRequests {
		t.Fatalf("switching IPs must not reset an account's budget, got %d", code)
	}
	if code := send(handler, requestAs(http.MethodPost, uuid.New(), "203.0.113.3")); code != http.StatusOK {
		t.Fatalf("another account on the same IP keeps its own budget, got %d", code)
	}
}

func TestSignedOutRequestsAreLimitedByIP(t *testing.T) {
	handler := perAccount(middleware.RateLimiterConfig{RequestsPerWindow: 1, Window: day}, okHandler())

	send(handler, requestAs(http.MethodPost, uuid.Nil, "203.0.113.9"))
	if code := send(handler, requestAs(http.MethodPost, uuid.Nil, "203.0.113.9")); code != http.StatusTooManyRequests {
		t.Fatalf("expected the second signed-out request from one IP to be limited, got %d", code)
	}
}

func TestAccountWriteLimitLeavesReadsAlone(t *testing.T) {
	handler := limitAccountWrites(okHandler())
	account := uuid.New()

	for range accountWritesBudget.RequestsPerWindow {
		send(handler, requestAs(http.MethodPost, account, "203.0.113.4"))
	}
	if code := send(handler, requestAs(http.MethodPost, account, "203.0.113.4")); code != http.StatusTooManyRequests {
		t.Fatalf("expected writes past the budget to be limited, got %d", code)
	}
	if code := send(handler, requestAs(http.MethodGet, account, "203.0.113.4")); code != http.StatusOK {
		t.Fatalf("reads must not be blocked by the write budget, got %d", code)
	}
}
