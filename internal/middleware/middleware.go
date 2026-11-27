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

type RateLimiterConfig struct {
	RequestsPerWindow int
	Window            time.Duration
}

const (
	requestsPerClientPerMinute = 240
	maxTrackedClients          = 10_000
	railwayClientIPHeader      = "X-Real-IP"
)

func DefaultRateLimiterConfig() RateLimiterConfig {
	return RateLimiterConfig{RequestsPerWindow: requestsPerClientPerMinute, Window: time.Minute}
}

type KeyFunc func(r *http.Request) string

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

func newRateLimiter(config RateLimiterConfig, now func() time.Time) *rateLimiter {
	return &rateLimiter{config: config, clients: make(map[string]*clientWindow), now: now}
}

func RateLimit(config RateLimiterConfig) func(http.Handler) http.Handler {
	return RateLimitBy(config, ClientIP)
}

func RateLimitBy(config RateLimiterConfig, key KeyFunc) func(http.Handler) http.Handler {
	limiter := newRateLimiter(config, time.Now)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.allow(key(r)) {
				rejectTooMany(w, r, config)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func rejectTooMany(w http.ResponseWriter, r *http.Request, config RateLimiterConfig) {
	slog.Warn("Rate limit hit", "method", r.Method, "path", r.URL.Path, "limit", config.RequestsPerWindow, "window", config.Window.String())
	w.Header().Set("Retry-After", strconv.Itoa(int(config.Window.Seconds())))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"too many requests, try again later"}`))
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

func ClientIP(r *http.Request) string {
	if proxied := strings.TrimSpace(r.Header.Get(railwayClientIPHeader)); proxied != "" {
		return proxied
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func RequireHTTPS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" && proto != "https" {
			http.Error(w, "HTTPS required", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

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

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
