package monitoring

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/getsentry/sentry-go"
)

func ConfigFromEnv() Config {
	return configFromLookup(os.Getenv)
}

func configFromLookup(lookup func(string) string) Config {
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value := strings.TrimSpace(lookup(key)); value != "" {
				return value
			}
		}
		return ""
	}
	environment := pick("SENTRY_ENVIRONMENT", "RAILWAY_ENVIRONMENT_NAME")
	if environment == "" {
		environment = "development"
	}
	return Config{
		DSN:         pick("SENTRY_DSN"),
		Environment: environment,
		Release:     pick("SENTRY_RELEASE", "RAILWAY_GIT_COMMIT_SHA"),
	}
}

func Install(config Config) (func(), error) {
	handler, flush, err := Wrap(slog.Default().Handler(), config)
	if err != nil {
		return func() {}, err
	}
	slog.SetDefault(slog.New(handler))
	return flush, nil
}

func Wrap(base slog.Handler, config Config) (slog.Handler, func(), error) {
	if config.DSN == "" {
		return base, func() {}, nil
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              config.DSN,
		Environment:      config.Environment,
		Release:          config.Release,
		SendDefaultPII:   false,
		AttachStacktrace: true,
		TracesSampleRate: 0,
		BeforeSend:       scrub,
	})
	if err != nil {
		return base, func() {}, fmt.Errorf("initialise sentry: %w", err)
	}
	hub := sentry.CurrentHub()
	flush := func() { hub.Flush(flushTimeout) }
	return slog.NewMultiHandler(base, newErrorHandler(hub)), flush, nil
}

func scrub(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	event.Request = nil
	event.User = sentry.User{}
	event.ServerName = ""
	event.Contexts = nil
	return event
}

type errorHandler struct {
	hub   *sentry.Hub
	attrs []slog.Attr
}

func newErrorHandler(hub *sentry.Hub) *errorHandler {
	return &errorHandler{hub: hub}
}

func (handler *errorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelError
}

func (handler *errorHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := append([]slog.Attr(nil), handler.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})

	var captured error
	handler.hub.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelError)
		for _, attr := range attrs {
			switch {
			case attr.Key == "error":
				if err, ok := attr.Value.Any().(error); ok {
					captured = err
				} else {
					captured = fmt.Errorf("%v", attr.Value.Any())
				}
			case taggedAttributes[attr.Key]:
				scope.SetTag(attr.Key, attr.Value.String())
			}
		}
		scope.SetTag("log_message", record.Message)
		if captured == nil {
			handler.hub.CaptureMessage(record.Message)
			return
		}
		handler.hub.CaptureException(fmt.Errorf("%s: %w", record.Message, captured))
	})
	return nil
}

func (handler *errorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &errorHandler{hub: handler.hub, attrs: append(append([]slog.Attr(nil), handler.attrs...), attrs...)}
}

func (handler *errorHandler) WithGroup(string) slog.Handler {
	return handler
}
