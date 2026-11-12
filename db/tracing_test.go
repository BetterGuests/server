package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSlowQueryTraceRoundTrip(t *testing.T) {
	tracer := SlowQueryTracer{}

	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})

	trace, ok := ctx.Value(queryTraceKey{}).(queryTrace)
	if !ok {
		t.Fatal("expected TraceQueryStart to attach a queryTrace to the context")
	}
	if trace.sql != "SELECT 1" {
		t.Fatalf("unexpected sql: %q", trace.sql)
	}
	if trace.startedAt.IsZero() {
		t.Fatal("expected a non-zero start time")
	}

	// TraceQueryEnd reading from a context with no trace (e.g. a connection
	// not opened through this tracer) must not panic.
	SlowQueryTracer{}.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})
}

func TestSlowQueryTraceEndDoesNotPanicOnError(t *testing.T) {
	tracer := SlowQueryTracer{}
	ctx := context.WithValue(context.Background(), queryTraceKey{}, queryTrace{sql: "SELECT 1", startedAt: time.Now().Add(-time.Second)})

	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: errors.New("boom")})
}
