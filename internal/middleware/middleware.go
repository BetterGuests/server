// Package middleware holds HTTP middleware that is not specific to any one
// business domain: rate limiting, HTTPS enforcement, panic recovery, and
// baseline security headers.
package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimiterConfig controls RateLimit.
type RateLimiterConfig struct {
	// RequestsPerWindow is how many requests a single client IP may make
	// within Window before further requests are rejected with 429.
	RequestsPerWindow int
	Window            time.Duration
}

// DefaultRateLimiterConfig is a conservative default for a public API
// surface: 60 requests per minute per client IP.
func DefaultRateLimiterConfig() RateLimiterConfig {
	return RateLimiterConfig{RequestsPerWindow: 60, Window: time.Minute}
}

// rateLimiter tracks a fixed window request count per client IP. A fixed
// window (rather than a sliding one or a token bucket) keeps this
// dependency-free and easy to reason about; it is not exact at the window
// boundary, which is an acceptable trade for a template default. Swap in a
// shared store (e.g. Redis) behind the same RateLimit signature if the API
// runs as more than one replica and needs limits enforced across them.
type rateLimiter struct {
	config  RateLimiterConfig
	mu      sync.Mutex
	clients map[string]*clientWindow
	now     func() time.Time
}

type clientWindow struct {
	count      int
	windowEnds time.Time
}

// maxTrackedClients bounds memory use: once this many distinct client keys
// are being tracked, expired windows are swept before adding a new one.
const maxTrackedClients = 10_000

func newRateLimiter(config RateLimiterConfig, now func() time.Time) *rateLimiter {
	return &rateLimiter{config: config, clients: make(map[string]*clientWindow), now: now}
}

// RateLimit returns middleware that limits each client IP to
// config.RequestsPerWindow requests per config.Window, responding 429 Too
// Many Requests once exceeded. Limiting by IP rather than by account means it
// protects unauthenticated endpoints too, and runs before authentication.
func RateLimit(config RateLimiterConfig) func(http.Handler) http.Handler {
	limiter := newRateLimiter(config, time.Now)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.allow(clientIP(r)) {
				w.Header().Set("Retry-After", strconv.Itoa(int(config.Window.Seconds())))
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if len(l.clients) >= maxTrackedClients {
		l.sweepExpired(now)
	}

	window, exists := l.clients[key]
	if !exists || now.After(window.windowEnds) {
		l.clients[key] = &clientWindow{count: 1, windowEnds: now.Add(l.config.Window)}
		return true
	}
	if window.count >= l.config.RequestsPerWindow {
		return false
	}
	window.count++
	return true
}

func (l *rateLimiter) sweepExpired(now time.Time) {
	for key, window := range l.clients {
		if now.After(window.windowEnds) {
			delete(l.clients, key)
		}
	}
}

// clientIP returns the request's client IP, preferring the de-facto standard
// X-Forwarded-For header set by a reverse proxy (Railway and most PaaS run
// this way) over RemoteAddr, which would otherwise always be the proxy's own
// address.
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if trimmed := strings.TrimSpace(first); trimmed != "" {
			return trimmed
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RequireHTTPS returns middleware that rejects a request forwarded over
// plain HTTP with 400 Bad Request. It trusts the de-facto standard
// X-Forwarded-Proto header set by a TLS-terminating reverse proxy (Railway
// and most PaaS run this way); r.TLS is always nil at the application
// process itself, since TLS terminates at the proxy, not here. When the
// header is absent entirely (no proxy in front, e.g. local development) the
// request passes through unchanged, so this is safe to leave enabled
// everywhere rather than gating it behind an environment check.
func RequireHTTPS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" && proto != "https" {
			http.Error(w, "HTTPS required", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Recover returns middleware that catches a panic in next, logs it at Error
// level with a stack trace, and responds 500 instead of letting net/http's
// own recovery silently close the connection. Keeping this in the same
// structured log stream as every other failure is what makes it possible to
// alert on "level=error" without missing panics.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("Panic recovered",
					"error", recovered,
					"stack", string(debug.Stack()),
					"method", r.Method,
					"path", r.URL.Path,
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal server error"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders returns middleware that sets baseline response headers
// hardening the API against content-sniffing and framing attacks. A
// content-security-policy header is intentionally omitted: this is a JSON
// API with no HTML responses, so a CSP tuned for script/style sources would
// not change the response's actual trust model.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
