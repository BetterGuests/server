package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/auth"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/logging"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logging.Configure()

	if err := run(); err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

func run() error {

	// D A T A B A S E

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

	// SECURITY.md: "Monitor slow queries and connection usage". Slow
	// individual queries are logged by db.SlowQueryTracer (wired in
	// db.Open); this covers the complementary concern of the pool as a
	// whole slowly running out of connections.
	go logPoolStatsPeriodically(pool)

	// S E R V E R

	origins, err := allowedOrigins()
	if err != nil {
		return fmt.Errorf("CORS configuration: %w", err)
	}
	clerkConfig, err := auth.ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("clerk configuration: %w", err)
	}
	clerkVerifier, err := auth.NewClerkTokenVerifier(clerkConfig)
	if err != nil {
		return fmt.Errorf("clerk configuration: %w", err)
	}
	accountService := accounts.NewAccountService(accounts.NewPostgresAccountStore(pool))
	authService := auth.NewAuthService(clerkVerifier, accountService)
	authenticator := auth.NewAuthenticator(authService)
	listingHandler := listings.NewHTTPHandler(listings.NewListingService(listings.NewPostgresListingStore(pool)))
	reviewHandler := reviews.NewHTTPHandler(reviews.NewService(reviews.NewPostgresStore(pool)))
	bookingHandler := bookings.NewHTTPHandler(bookings.NewBookingService(bookings.NewPostgresBookingStore(pool)))
	api := http.NewServeMux()
	api.HandleFunc("POST /api/v1/listings", listingHandler.CreateListing)
	api.HandleFunc("POST /api/v1/reviews", reviewHandler.CreateReview)
	api.HandleFunc("POST /api/v1/bookings", bookingHandler.CreateBooking)
	server, err := createServer(routes(pool, origins, authenticator.Middleware(api)))
	if err != nil {
		return fmt.Errorf("server creation: %w", err)
	}

	return runServer(server)
}

// logPoolStatsPeriodically logs the database connection pool's stats on a
// fixed interval for the life of the process. It never stops itself; the
// process exiting is what stops it, which is fine for a log line with no
// resource to release.
func logPoolStatsPeriodically(pool *pgxpool.Pool) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		db.LogPoolStats(pool)
	}
}

func allowedOrigins() ([]string, error) {
	var origins []string
	if err := json.Unmarshal([]byte(os.Getenv("ALLOWED_ORIGINS")), &origins); err != nil {
		return nil, err
	}
	return origins, nil
}
