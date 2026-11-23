package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/auth"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/BetterGuests/BetterGuests-Server/internal/compliance"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/monitoring"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/jackc/pgx/v5/pgxpool"
)

func buildRouteDeps(pool *pgxpool.Pool) (routeDeps, error) {
	origins, err := allowedOrigins()
	if err != nil {
		return routeDeps{}, fmt.Errorf("CORS configuration: %w", err)
	}

	clerkConfig, err := auth.ConfigFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("clerk configuration: %w", err)
	}
	clerkVerifier, err := auth.NewClerkTokenVerifier(clerkConfig)
	if err != nil {
		return routeDeps{}, fmt.Errorf("clerk configuration: %w", err)
	}
	accountService := accounts.NewAccountService(accounts.NewPostgresAccountStore(pool))
	authenticator := auth.NewAuthenticator(auth.NewAuthService(clerkVerifier, accountService))

	complianceService := compliance.NewComplianceService(compliance.NewPostgresComplianceStore(pool))

	personaConfig, err := verification.ConfigFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("persona configuration: %w", err)
	}
	verificationStore := verification.NewPostgresVerificationStore(pool)
	verificationService := verification.NewVerificationService(
		verificationStore,
		verification.NewPersonaClient(personaConfig, nil),
		personaConfig,
	).WithConsents(complianceService)

	paymentConfig, err := payments.ConfigFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("stripe configuration: %w", err)
	}
	stripeGateway := payments.NewStripeGateway(paymentConfig)
	paymentService := payments.NewPaymentService(payments.NewPostgresPaymentStore(pool), stripeGateway, stripeGateway, paymentConfig)

	objectStore, err := cashback.NewObjectStoreFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("photo storage configuration: %w", err)
	}
	cashbackService := cashback.NewCashbackService(
		cashback.NewPostgresCashbackStore(pool),
		verificationStore,
		cashback.NewStripeRefunder(paymentConfig.SecretKey),
		objectStore,
	)

	listingStore := listings.NewPostgresListingStore(pool)

	return routeDeps{
		database:     pool,
		origins:      origins,
		authenticate: authenticator.Middleware,
		listings: listings.NewHTTPHandler(listings.NewListingService(listingStore)).
			WithPublicReader(listings.NewPublicListingService(listingStore, objectStore)),
		reviews:      reviews.NewHTTPHandler(reviews.NewService(reviews.NewPostgresStore(pool))),
		bookings:     bookings.NewHTTPHandler(bookings.NewBookingService(bookings.NewPostgresBookingStore(pool))),
		cashback:     cashback.NewHTTPHandler(cashbackService, cashbackService, cashbackService, cashbackService, cashbackService),
		verification: verification.NewHTTPHandler(verificationService, verificationService).WithStatusReader(verificationStore),
		payments:     payments.NewHTTPHandler(paymentService, paymentService),
		compliance:   compliance.NewHTTPHandler(complianceService, complianceService, complianceService),
	}, nil
}

func allowedOrigins() ([]string, error) {
	var origins []string
	if err := json.Unmarshal([]byte(os.Getenv("ALLOWED_ORIGINS")), &origins); err != nil {
		return nil, err
	}
	return origins, nil
}

func installMonitoring() func() {
	flush, err := monitoring.Install(monitoring.ConfigFromEnv())
	if err != nil {
		slog.Warn("Error monitoring is disabled", "error", err)
	}
	return flush
}
