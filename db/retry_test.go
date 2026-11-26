package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var quickBackoff = backoff{first: time.Millisecond, limit: 2 * time.Millisecond}

func connectFailure() error {
	_, err := pgconn.Connect(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	return err
}

func TestBackoffDoublesUpToLimit(t *testing.T) {
	schedule := backoff{first: 100 * time.Millisecond, limit: 500 * time.Millisecond}
	want := []time.Duration{100, 200, 400, 500, 500}
	for attempt, expected := range want {
		if got := schedule.delay(attempt); got != expected*time.Millisecond {
			t.Fatalf("attempt %d: got %s, want %s", attempt, got, expected*time.Millisecond)
		}
	}
}

func TestOpenWithRetryKeepsTryingUntilDatabaseAnswers(t *testing.T) {
	attempts := 0
	pool := &pgxpool.Pool{}
	got, err := openWithRetry(context.Background(), quickBackoff, func(context.Context) (*pgxpool.Pool, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("connection refused")
		}
		return pool, nil
	})
	if err != nil || got != pool {
		t.Fatalf("expected the pool after retries, got %v, %v", got, err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestOpenWithRetryStopsOnWrongRole(t *testing.T) {
	attempts := 0
	_, err := openWithRetry(context.Background(), quickBackoff, func(context.Context) (*pgxpool.Pool, error) {
		attempts++
		return nil, fmt.Errorf("%w: got %q", ErrUnexpectedRole, "postgres")
	})
	if !errors.Is(err, ErrUnexpectedRole) || attempts != 1 {
		t.Fatalf("expected one attempt and ErrUnexpectedRole, got %d attempts, %v", attempts, err)
	}
}

func TestOpenWithRetryStopsOnInvalidURL(t *testing.T) {
	attempts := 0
	_, err := openWithRetry(context.Background(), quickBackoff, func(context.Context) (*pgxpool.Pool, error) {
		attempts++
		return nil, errInvalidDatabase
	})
	if !errors.Is(err, errInvalidDatabase) || attempts != 1 {
		t.Fatalf("expected one attempt, got %d, %v", attempts, err)
	}
}

func TestOpenWithRetryGivesUpWhenStartupWindowEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	cause := errors.New("connection refused")
	_, err := openWithRetry(ctx, quickBackoff, func(context.Context) (*pgxpool.Pool, error) {
		return nil, cause
	})
	if !errors.Is(err, cause) {
		t.Fatalf("expected the last connection error, got %v", err)
	}
}

type fakeTx struct{ pgx.Tx }

type flakyBeginner struct {
	failures []error
	calls    int
}

func (b *flakyBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	b.calls++
	if b.calls <= len(b.failures) {
		return nil, b.failures[b.calls-1]
	}
	return &fakeTx{}, nil
}

func TestBeginRetriesWhenConnectionCannotBeOpened(t *testing.T) {
	beginner := &flakyBeginner{failures: []error{connectFailure()}}
	tx, err := beginWithRetry(context.Background(), beginner, pgx.TxOptions{})
	if err != nil || tx == nil {
		t.Fatalf("expected a transaction after one retry, got %v", err)
	}
	if beginner.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", beginner.calls)
	}
}

func TestBeginGivesUpAfterThreeConnectionFailures(t *testing.T) {
	failure := connectFailure()
	beginner := &flakyBeginner{failures: []error{failure, failure, failure, failure}}
	_, err := beginWithRetry(context.Background(), beginner, pgx.TxOptions{})
	if err == nil || beginner.calls != beginAttempts {
		t.Fatalf("expected %d calls and an error, got %d, %v", beginAttempts, beginner.calls, err)
	}
}

func TestBeginDoesNotRetryServerErrors(t *testing.T) {
	beginner := &flakyBeginner{failures: []error{&pgconn.PgError{Code: "53300"}}}
	_, err := beginWithRetry(context.Background(), beginner, pgx.TxOptions{})
	if err == nil || beginner.calls != 1 {
		t.Fatalf("expected one call and an error, got %d, %v", beginner.calls, err)
	}
}

func TestBeginDoesNotRetryCanceledRequests(t *testing.T) {
	beginner := &flakyBeginner{failures: []error{context.Canceled}}
	_, err := beginWithRetry(context.Background(), beginner, pgx.TxOptions{})
	if !errors.Is(err, context.Canceled) || beginner.calls != 1 {
		t.Fatalf("expected one call and context.Canceled, got %d, %v", beginner.calls, err)
	}
}
