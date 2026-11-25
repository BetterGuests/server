package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/logging"
)

func main() {
	logging.Configure()
	flushMonitoring := installMonitoring()

	if err := run(); err != nil {
		slog.Error("Server failed", "error", err)
		flushMonitoring()
		os.Exit(1)
	}
	flushMonitoring()
}

func run() error {
	config, err := db.ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, config)
	if err != nil {
		return fmt.Errorf("database connection: %w", err)
	}
	defer pool.Close()

	go logPoolStatsPeriodically(pool)

	deps, err := buildRouteDeps(pool)
	if err != nil {
		return err
	}

	redaction, err := buildRedactionService(pool)
	if err != nil {
		return err
	}
	settlement, err := buildSettlementService(pool)
	if err != nil {
		return err
	}
	jobs, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()
	go redaction.Run(jobs)
	go settlement.Run(jobs)

	server, err := createServer(routes(deps))
	if err != nil {
		return fmt.Errorf("server creation: %w", err)
	}

	return runServer(server)
}
