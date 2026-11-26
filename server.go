package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	databaseStartupWait = 60 * time.Second
	shutdownGracePeriod = 10 * time.Second
	jobRestartFirst     = time.Second
	jobRestartLimit     = time.Minute
)

type databasePinger interface {
	Ping(context.Context) error
}

func createServer(handler http.Handler) (*http.Server, error) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	return &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}, nil
}

func runServer(server *http.Server) error {
	slog.Info("Starting server", "address", server.Addr)

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	done := make(chan error, 1)
	go func() {
		done <- server.ListenAndServe()
	}()

	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		slog.Info("Shutting down...")
	}

	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}

	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	slog.Info("Server stopped gracefully")
	return nil
}

func superviseJob(ctx context.Context, name string, job func(context.Context)) {
	wait := jobRestartFirst
	for {
		if completed := runJobOnce(ctx, name, job); completed || ctx.Err() != nil {
			return
		}
		slog.Error("Background job crashed, restarting", "job", name, "restart_in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, jobRestartLimit)
	}
}

func runJobOnce(ctx context.Context, name string, job func(context.Context)) (completed bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("Background job panicked", "job", name, "panic", recovered, "stack", string(debug.Stack()))
			completed = false
		}
	}()
	job(ctx)
	return true
}

func logPoolStatsPeriodically(pool *pgxpool.Pool) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		db.LogPoolStats(pool)
	}
}

func health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("OK")); err != nil {
		slog.Error("Server did not respond", "error", err)
		return
	}
	slog.Debug("Server is healthy")
}

func ready(database databasePinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := database.Ping(ctx); err != nil {
			slog.Error("Database readiness check failed", "error", err)
			http.Error(w, "NOT READY", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("READY"))
	}
}
