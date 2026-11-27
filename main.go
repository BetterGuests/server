package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

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

	ctx, cancel := context.WithTimeout(context.Background(), databaseStartupWait)
	defer cancel()

	pool, err := db.OpenWithRetry(ctx, config)
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
	documents, err := buildDocumentRetention(pool)
	if err != nil {
		return err
	}
	pauses := buildVerificationPauses(pool)
	jobs, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()
	go superviseJob(jobs, "id redaction", redaction.Run)
	go superviseJob(jobs, "document retention", documents.Run)
	if scan := buildProofScan(pool); scan != nil {
		go superviseJob(jobs, "document scan", scan.Run)
	}
	go superviseJob(jobs, "verification pauses", pauses.Run)
	go superviseJob(jobs, "settlement", settlement.Run)
	go superviseJob(jobs, "trip updates", deps.tripUpdates.Run)

	server, err := createServer(routes(deps))
	if err != nil {
		return fmt.Errorf("server creation: %w", err)
	}

	return runServer(server)
}
