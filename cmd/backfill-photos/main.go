package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

func main() {
	apply := flag.Bool("apply", false, "write card photos to storage and update photo records")
	flag.Parse()
	if err := run(*apply); err != nil {
		fmt.Fprintln(os.Stderr, "backfill failed:", err)
		os.Exit(1)
	}
}

func run(apply bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	config, err := db.ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("database config: %w", err)
	}
	pool, err := db.Open(ctx, config)
	if err != nil {
		return fmt.Errorf("database connection: %w", err)
	}
	defer pool.Close()

	objects, err := cashback.NewObjectStoreFromEnv()
	if err != nil {
		return fmt.Errorf("photo storage config: %w", err)
	}
	files, ok := objects.(listings.CardBackfillFiles)
	if !ok {
		return errors.New("photo storage is not configured")
	}

	service := listings.NewCardBackfillService(listings.NewPostgresListingStore(pool), files)
	backfill, mode := service.Preview, "PREVIEW, nothing was written"
	if apply {
		backfill, mode = service.Apply, "APPLIED"
	}
	report, err := backfill(ctx)
	printReport(mode, report)
	return err
}

func printReport(mode string, report listings.BackfillReport) {
	fmt.Println(mode)
	fmt.Println("published photos:", report.Photos)
	fmt.Println("already have a card:", report.AlreadySized)
	fmt.Println("keep the original:", report.KeptOriginal)
	fmt.Println("can be resized:", report.Resizable)
	fmt.Println("resized:", report.Resized)
	fmt.Println("failed:", report.Failed)
	fmt.Printf("full photos: %d KB, card versions: %d KB\n", report.OriginalBytes/1024, report.CardBytes/1024)
	for _, change := range report.Changes {
		fmt.Printf("changed %s from %s to %s\n", change.PhotoID, change.From, change.To)
	}
}
