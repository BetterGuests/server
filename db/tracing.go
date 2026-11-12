package db

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// slowQueryThreshold is how long a query may run before SlowQueryTracer logs
// it. See SECURITY.md: "Monitor slow queries and connection usage".
const slowQueryThreshold = 200 * time.Millisecond

type queryTraceKey struct{}

type queryTrace struct {
	sql       string
	startedAt time.Time
}

// SlowQueryTracer implements pgx.QueryTracer, logging any query slower than
// slowQueryThreshold at Warn level. It logs the SQL text (always a
// parameterized literal under this codebase's rules — see AGENTS.md
// "Database") but never query arguments, since those can carry user data.
type SlowQueryTracer struct{}

// TraceQueryStart records when a query began, for TraceQueryEnd to measure
// against.
func (SlowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryTraceKey{}, queryTrace{sql: data.SQL, startedAt: time.Now()})
}

// TraceQueryEnd logs the query if it ran longer than slowQueryThreshold.
func (SlowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(queryTraceKey{}).(queryTrace)
	if !ok {
		return
	}
	duration := time.Since(trace.startedAt)
	if duration < slowQueryThreshold {
		return
	}
	slog.Warn("Slow query", "duration", duration.String(), "sql", trace.sql, "error", data.Err)
}

// LogPoolStats logs the connection pool's current acquired/idle/total
// connection counts at Info level. Call this periodically (e.g. alongside a
// health check) to make connection exhaustion visible in logs before it
// becomes an outage.
func LogPoolStats(pool *pgxpool.Pool) {
	stats := pool.Stat()
	slog.Info("Database pool stats",
		"acquired_conns", stats.AcquiredConns(),
		"idle_conns", stats.IdleConns(),
		"total_conns", stats.TotalConns(),
		"max_conns", stats.MaxConns(),
	)
}
