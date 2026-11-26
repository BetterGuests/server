package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUnexpectedRole  = errors.New("connected as an unexpected database role")
	errInvalidDatabase = errors.New("invalid database URL")
)

type backoff struct {
	first time.Duration
	limit time.Duration
}

var (
	startupBackoff = backoff{first: 500 * time.Millisecond, limit: 8 * time.Second}
	beginBackoff   = backoff{first: 100 * time.Millisecond, limit: 400 * time.Millisecond}
)

const beginAttempts = 3

func (b backoff) delay(attempt int) time.Duration {
	wait := b.first
	for range attempt {
		wait *= 2
		if wait >= b.limit {
			return b.limit
		}
	}
	return wait
}

func OpenWithRetry(ctx context.Context, config Config) (*pgxpool.Pool, error) {
	return openWithRetry(ctx, startupBackoff, func(attemptCtx context.Context) (*pgxpool.Pool, error) {
		return Open(attemptCtx, config)
	})
}

func openWithRetry(
	ctx context.Context,
	schedule backoff,
	open func(context.Context) (*pgxpool.Pool, error),
) (*pgxpool.Pool, error) {
	for attempt := 0; ; attempt++ {
		pool, err := open(ctx)
		if err == nil {
			if attempt > 0 {
				slog.Info("Database connected", "attempts", attempt+1)
			}
			return pool, nil
		}
		if errors.Is(err, ErrUnexpectedRole) || errors.Is(err, errInvalidDatabase) {
			return nil, err
		}

		wait := schedule.delay(attempt)
		slog.Warn("Database not reachable yet, retrying", "attempt", attempt+1, "retry_in", wait, "error", err)
		if sleepErr := sleep(ctx, wait); sleepErr != nil {
			return nil, fmt.Errorf("database still unreachable after %d attempts: %w", attempt+1, err)
		}
	}
}

func beginWithRetry(ctx context.Context, db transactionBeginner, options pgx.TxOptions) (pgx.Tx, error) {
	var lastErr error
	for attempt := range beginAttempts {
		tx, err := db.BeginTx(ctx, options)
		if err == nil {
			return tx, nil
		}
		lastErr = err
		if !isConnectionFailure(err) || attempt == beginAttempts-1 {
			break
		}
		if err := sleep(ctx, beginBackoff.delay(attempt)); err != nil {
			break
		}
	}
	return nil, lastErr
}

func isConnectionFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var connectErr *pgconn.ConnectError
	return errors.As(err, &connectErr) || pgconn.SafeToRetry(err)
}

func sleep(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
