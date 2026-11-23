package monitoring

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

type recordingTransport struct {
	mutex  sync.Mutex
	events []*sentry.Event
}

func (transport *recordingTransport) Configure(sentry.ClientOptions) {}
func (transport *recordingTransport) SendEvent(event *sentry.Event) {
	transport.mutex.Lock()
	defer transport.mutex.Unlock()
	transport.events = append(transport.events, event)
}
func (transport *recordingTransport) Flush(time.Duration) bool              { return true }
func (transport *recordingTransport) FlushWithContext(context.Context) bool { return true }
func (transport *recordingTransport) Close()                                {}

func testLogger(t *testing.T) (*slog.Logger, *recordingTransport) {
	t.Helper()
	transport := &recordingTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:        "https://public@example.ingest.sentry.io/1",
		Transport:  transport,
		BeforeSend: scrub,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := sentry.NewHub(client, sentry.NewScope())
	return slog.New(newErrorHandler(hub)), transport
}

func TestErrorLogsBecomeSentryEventsWithTheErrorAttached(t *testing.T) {
	logger, transport := testLogger(t)
	logger.Error("Stripe webhook", "error", errors.New("signature mismatch"), "path", "/api/v1/webhooks/stripe")
	if len(transport.events) != 1 {
		t.Fatalf("events = %d", len(transport.events))
	}
	event := transport.events[0]
	if len(event.Exception) == 0 || event.Exception[len(event.Exception)-1].Value != "Stripe webhook: signature mismatch" {
		t.Fatalf("exception = %+v", event.Exception)
	}
	if event.Tags["path"] != "/api/v1/webhooks/stripe" || event.Level != sentry.LevelError {
		t.Fatalf("tags = %v, level = %s", event.Tags, event.Level)
	}
}

func TestPanicValuesThatAreNotErrorsStillReachSentry(t *testing.T) {
	logger, transport := testLogger(t)
	logger.Error("Panic recovered", "error", "index out of range", "stack", "goroutine 1")
	if len(transport.events) != 1 {
		t.Fatalf("events = %d", len(transport.events))
	}
	chain := transport.events[0].Exception
	if chain[len(chain)-1].Value != "Panic recovered: index out of range" {
		t.Fatalf("exception = %+v", chain)
	}
}

func TestLowerLevelsNeverReachSentry(t *testing.T) {
	logger, transport := testLogger(t)
	logger.Info("Starting server")
	logger.Warn("Persona webhook rejected", "remote_addr", "203.0.113.9:4000")
	logger.Debug("noise")
	if len(transport.events) != 0 {
		t.Fatalf("events = %d", len(transport.events))
	}
}

func TestUntaggedAttributesAreNeverSent(t *testing.T) {
	logger, transport := testLogger(t)
	logger.Error("Start verification", "error", errors.New("boom"), "remote_addr", "203.0.113.9:4000", "account_id", "11111111-1111-1111-1111-111111111111", "stack", "trace")
	event := transport.events[0]
	for key := range event.Tags {
		if key == "remote_addr" || key == "account_id" || key == "stack" {
			t.Fatalf("tag %q leaked", key)
		}
	}
}

func TestScrubRemovesRequestUserAndServerDetails(t *testing.T) {
	event := &sentry.Event{
		Request:    &sentry.Request{URL: "https://api.betterguests.com/x?token=secret", Headers: map[string]string{"Authorization": "Bearer abc"}, Data: "body"},
		User:       sentry.User{Email: "guest@example.com", IPAddress: "203.0.113.9"},
		ServerName: "railway-host",
		Contexts:   map[string]sentry.Context{"device": {"name": "host"}},
	}
	scrubbed := scrub(event, nil)
	if scrubbed.Request != nil || scrubbed.User.Email != "" || scrubbed.User.IPAddress != "" || scrubbed.ServerName != "" || scrubbed.Contexts != nil {
		t.Fatalf("scrubbed = %+v", scrubbed)
	}
}

func TestWithAttrsKeepsLoggerAttributesOnLaterEvents(t *testing.T) {
	logger, transport := testLogger(t)
	logger.With("method", "POST").Error("Webhook", "error", errors.New("x"))
	if transport.events[0].Tags["method"] != "POST" {
		t.Fatalf("tags = %v", transport.events[0].Tags)
	}
}

func TestWrapWithoutDSNReturnsTheBaseHandlerUnchanged(t *testing.T) {
	base := slog.NewTextHandler(nopWriter{}, nil)
	handler, flush, err := Wrap(base, Config{})
	if err != nil || handler != slog.Handler(base) {
		t.Fatalf("handler changed or error: %v", err)
	}
	flush()
}

func TestWrapRejectsAnInvalidDSNWithoutCrashing(t *testing.T) {
	base := slog.NewTextHandler(nopWriter{}, nil)
	handler, flush, err := Wrap(base, Config{DSN: "not a dsn"})
	if err == nil || handler != slog.Handler(base) {
		t.Fatalf("expected an error and the base handler, got %v", err)
	}
	flush()
}

func TestConfigFromLookupFallsBackToRailwayValues(t *testing.T) {
	values := map[string]string{"SENTRY_DSN": " https://k@o.ingest.sentry.io/1 ", "RAILWAY_ENVIRONMENT_NAME": "production", "RAILWAY_GIT_COMMIT_SHA": "abc123"}
	config := configFromLookup(func(key string) string { return values[key] })
	if config.DSN != "https://k@o.ingest.sentry.io/1" || config.Environment != "production" || config.Release != "abc123" {
		t.Fatalf("config = %+v", config)
	}
	values["SENTRY_ENVIRONMENT"] = "staging"
	values["SENTRY_RELEASE"] = "v1"
	config = configFromLookup(func(key string) string { return values[key] })
	if config.Environment != "staging" || config.Release != "v1" {
		t.Fatalf("explicit config = %+v", config)
	}
	if empty := configFromLookup(func(string) string { return "" }); empty.DSN != "" || empty.Environment != "development" {
		t.Fatalf("empty config = %+v", empty)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
