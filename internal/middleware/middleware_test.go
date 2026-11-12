package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterAllowsUnderLimit(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(RateLimiterConfig{RequestsPerWindow: 3, Window: time.Minute}, func() time.Time { return now })

	for i := 0; i < 3; i++ {
		if !limiter.allow("1.1.1.1") {
			t.Fatalf("request %d was unexpectedly rejected", i)
		}
	}
}

func TestRateLimiterRejectsOverLimit(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(RateLimiterConfig{RequestsPerWindow: 2, Window: time.Minute}, func() time.Time { return now })

	limiter.allow("1.1.1.1")
	limiter.allow("1.1.1.1")
	if limiter.allow("1.1.1.1") {
		t.Fatal("expected the third request in the window to be rejected")
	}
}

func TestRateLimiterTracksClientsIndependently(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(RateLimiterConfig{RequestsPerWindow: 1, Window: time.Minute}, func() time.Time { return now })

	if !limiter.allow("1.1.1.1") {
		t.Fatal("first client's first request should be allowed")
	}
	if !limiter.allow("2.2.2.2") {
		t.Fatal("second client should not be limited by the first client's usage")
	}
	if limiter.allow("1.1.1.1") {
		t.Fatal("first client's second request should be rejected")
	}
}

func TestRateLimiterResetsAfterWindow(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(RateLimiterConfig{RequestsPerWindow: 1, Window: time.Minute}, func() time.Time { return now })

	limiter.allow("1.1.1.1")
	if limiter.allow("1.1.1.1") {
		t.Fatal("second request within the window should be rejected")
	}

	now = now.Add(time.Minute + time.Second)
	if !limiter.allow("1.1.1.1") {
		t.Fatal("request after the window elapsed should be allowed")
	}
}

func TestRateLimitMiddlewareRejectsWithRetryAfter(t *testing.T) {
	handler := RateLimit(RateLimiterConfig{RequestsPerWindow: 1, Window: time.Minute})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = "9.9.9.9:1234"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, first)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", recorder.Code)
	}

	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.RemoteAddr = "9.9.9.9:1234"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, second)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header on a rate-limited response")
	}
}

func TestClientIPPrefersForwardedFor(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.1:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")

	if got := clientIP(request); got != "203.0.113.5" {
		t.Fatalf("expected the first forwarded address, got %q", got)
	}
}

func TestClientIPFallsBackToRemoteAddr(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.1:1234"

	if got := clientIP(request); got != "10.0.0.1" {
		t.Fatalf("expected the remote address host, got %q", got)
	}
}

func TestRequireHTTPSRejectsPlainHTTP(t *testing.T) {
	handler := RequireHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run for a rejected request")
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Forwarded-Proto", "http")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

func TestRequireHTTPSAllowsHTTPS(t *testing.T) {
	called := false
	handler := RequireHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if !called || recorder.Code != http.StatusOK {
		t.Fatalf("expected the request to pass through, got called=%v code=%d", called, recorder.Code)
	}
}

func TestRequireHTTPSAllowsRequestsWithoutTheHeader(t *testing.T) {
	called := false
	handler := RequireHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if !called || recorder.Code != http.StatusOK {
		t.Fatal("a request with no proxy header (e.g. local development) must pass through")
	}
}

func TestRecoverCatchesPanicAndReturns500(t *testing.T) {
	handler := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatal("expected a JSON error response")
	}
}

func TestRecoverPassesThroughWithoutPanic(t *testing.T) {
	handler := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTeapot {
		t.Fatalf("expected the handler's own status to pass through, got %d", recorder.Code)
	}
}

func TestSecurityHeadersSetsBaselineHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Fatalf("header %s: got %q, want %q", header, got, want)
		}
	}
}
