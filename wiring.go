package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/auth"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/BetterGuests/BetterGuests-Server/internal/compliance"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/monitoring"
	"github.com/BetterGuests/BetterGuests-Server/internal/nearby"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/profiles"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/google/uuid"
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

	currentVerifier := verification.NewCurrentVerifier(verificationStore)

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

	profileService := profiles.NewProfileService(profiles.NewPostgresProfileStore(pool), bannerObjectsFrom(objectStore))

	return routeDeps{
		database:     pool,
		origins:      origins,
		authenticate: authenticator.Middleware,
		listings: listings.NewHTTPHandler(listings.NewListingService(listingStore)).
			WithPublicReader(listings.NewPublicListingService(listingStore, objectStore)),
		nearby:       buildNearbyHandler(listingStore),
		reviews:      reviews.NewHTTPHandler(reviews.NewService(reviews.NewPostgresStore(pool))),
		bookings:     bookings.NewHTTPHandler(bookings.NewBookingService(bookings.NewPostgresBookingStore(pool)).WithVerification(currentVerifier)),
		cashback:     cashback.NewHTTPHandler(cashbackService, cashbackService, cashbackService, cashbackService, cashbackService),
		verification: verification.NewHTTPHandler(verificationService, verificationService).WithStatusReader(verificationStore).WithCurrentChecker(currentVerifier),
		payments:     payments.NewHTTPHandler(paymentService, paymentService),
		compliance:   compliance.NewHTTPHandler(complianceService, complianceService, complianceService),
		profiles:     profiles.NewHTTPHandler(profileService, profileService, profileService, profileService),
	}, nil
}

func buildNearbyHandler(listingStore *listings.PostgresListingStore) *nearby.HTTPHandler {
	config := nearby.ConfigFromEnv()
	if !config.Configured() {
		slog.Warn("Nearby places are disabled: GEOAPIFY_API_KEY is not set")
		return nil
	}
	service := nearby.NewNearbyService(
		listingLocations{reader: listings.NewPublicListingService(listingStore, nil)},
		nearby.NewGeoapifySource(config, nil),
	)
	return nearby.NewHTTPHandler(service)
}

type publishedListingReader interface {
	GetPublished(ctx context.Context, id uuid.UUID) (listings.PublicListing, error)
}

type listingLocations struct{ reader publishedListingReader }

func (locations listingLocations) Location(ctx context.Context, listingID uuid.UUID) (nearby.Point, error) {
	listing, err := locations.reader.GetPublished(ctx, listingID)
	if errors.Is(err, listings.ErrListingNotFound) {
		return nearby.Point{}, nearby.ErrListingNotFound
	}
	if err != nil {
		return nearby.Point{}, err
	}
	return nearby.Point{Latitude: listing.Location.Latitude, Longitude: listing.Location.Longitude}, nil
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

type objectRemover interface {
	Remove(ctx context.Context, key string) error
}

type bannerObjects struct{ store cashback.ObjectStore }

func bannerObjectsFrom(store cashback.ObjectStore) profiles.ObjectStore {
	if store == nil {
		return nil
	}
	return bannerObjects{store: store}
}

func (objects bannerObjects) PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error) {
	return objects.store.PresignUpload(ctx, key, contentType, expires)
}

func (objects bannerObjects) PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error) {
	return objects.store.PresignDownload(ctx, key, expires)
}

func (objects bannerObjects) Stat(ctx context.Context, key string) (profiles.ObjectInfo, error) {
	info, err := objects.store.Stat(ctx, key)
	if err != nil {
		return profiles.ObjectInfo{}, err
	}
	return profiles.ObjectInfo{Size: info.Size, ContentType: info.ContentType}, nil
}

func (objects bannerObjects) Remove(ctx context.Context, key string) error {
	remover, ok := objects.store.(objectRemover)
	if !ok {
		return nil
	}
	return remover.Remove(ctx, key)
}
