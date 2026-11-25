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
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/monitoring"
	"github.com/BetterGuests/BetterGuests-Server/internal/nearby"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/profiles"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/BetterGuests/BetterGuests-Server/internal/realtime"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/BetterGuests/BetterGuests-Server/internal/waitlist"
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
	accountStore := accounts.NewPostgresAccountStore(pool)
	accountService := accounts.NewAccountService(accountStore)
	nameFetcher, err := auth.NewClerkNameFetcher(clerkConfig)
	if err != nil {
		return routeDeps{}, fmt.Errorf("clerk configuration: %w", err)
	}
	authService := auth.NewAuthService(clerkVerifier, newNamingProvisioner(accountService, nameFetcher, accountStore))
	authenticator := auth.NewAuthenticator(authService)
	realtimeService := realtime.NewRealtimeService()

	complianceService := compliance.NewComplianceService(compliance.NewPostgresComplianceStore(pool))

	personaConfig, err := verification.ConfigFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("persona configuration: %w", err)
	}
	verificationStore := verification.NewPostgresVerificationStore(pool)
	personaClient := verification.NewPersonaClient(personaConfig, nil)
	verificationService := verification.NewVerificationService(
		verificationStore,
		personaClient,
		personaConfig,
	).WithConsents(complianceService)
	deletionService := verification.NewDeletionService(verificationStore, personaClient)

	currentVerifier := verification.NewCurrentVerifier(verificationStore)

	messageService := messages.NewMessageService(messages.NewPostgresMessageStore(pool)).WithPublisher(realtimeService)

	paymentConfig, err := payments.ConfigFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("stripe configuration: %w", err)
	}
	stripeGateway := payments.NewStripeGateway(paymentConfig)
	paymentService := payments.NewPaymentService(payments.NewPostgresPaymentStore(pool), stripeGateway, stripeGateway, paymentConfig).
		WithController(stripeGateway).
		WithPublisher(realtimeService)

	bookingStore := bookings.NewPostgresBookingStore(pool)
	bookingService := bookings.NewBookingService(bookingStore).
		WithVerification(currentVerifier).
		WithDetails(bookingStore).
		WithPublisher(realtimeService).
		WithPayments(paymentService)

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
	listingService := listings.NewListingService(listingStore).WithPublishing(listingStore, verificationStore)

	proofService := proofs.NewProofService(proofs.NewPostgresProofStore(pool), proofObjectsFrom(objectStore))

	profileService := profiles.NewProfileService(profiles.NewPostgresProfileStore(pool), bannerObjectsFrom(objectStore))

	return routeDeps{
		database:     pool,
		origins:      origins,
		authenticate: authenticator.Middleware,
		listings: listings.NewHTTPHandler(listingService).
			WithPublisher(listingService).
			WithPublicReader(listings.NewPublicListingService(listingStore, objectStore)),
		proofs:       proofs.NewHTTPHandler(proofService, proofService, proofService, proofService, proofService),
		nearby:       buildNearbyHandler(listingStore),
		reviews:      reviews.NewHTTPHandler(reviews.NewService(reviews.NewPostgresStore(pool))),
		bookings:     bookings.NewHTTPHandler(bookingService).WithReader(bookingService).WithResponder(bookingService),
		messages:     messages.NewHTTPHandler(messageService, messageService, messageService),
		realtime:     realtime.NewHTTPHandler(realtimeService, authService, origins),
		cashback:     cashback.NewHTTPHandler(cashbackService, cashbackService, cashbackService, cashbackService, cashbackService),
		verification: verification.NewHTTPHandler(verificationService, verificationService).WithStatusReader(verificationStore).WithCurrentChecker(currentVerifier).WithDeletion(deletionService),
		payments:     payments.NewHTTPHandler(paymentService, paymentService),
		compliance:   compliance.NewHTTPHandler(complianceService, complianceService, complianceService),
		profiles:     profiles.NewHTTPHandler(profileService, profileService, profileService, profileService),
		waitlist:     waitlist.NewHTTPHandler(waitlist.NewWaitlistService(waitlist.NewPostgresWaitlistStore(pool))),
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

type proofObjects struct{ store cashback.ObjectStore }

func proofObjectsFrom(store cashback.ObjectStore) proofs.ObjectStore {
	if store == nil {
		return nil
	}
	return proofObjects{store: store}
}

func (objects proofObjects) PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error) {
	return objects.store.PresignUpload(ctx, key, contentType, expires)
}

func (objects proofObjects) PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error) {
	return objects.store.PresignDownload(ctx, key, expires)
}

func (objects proofObjects) Stat(ctx context.Context, key string) (proofs.ObjectInfo, error) {
	info, err := objects.store.Stat(ctx, key)
	if err != nil {
		return proofs.ObjectInfo{}, err
	}
	return proofs.ObjectInfo{Size: info.Size, ContentType: info.ContentType}, nil
}

func (objects proofObjects) Remove(ctx context.Context, key string) error {
	remover, ok := objects.store.(objectRemover)
	if !ok {
		return nil
	}
	return remover.Remove(ctx, key)
}

func buildRedactionService(pool *pgxpool.Pool) (*verification.RedactionService, error) {
	personaConfig, err := verification.ConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("persona configuration: %w", err)
	}
	redactionConfig, err := verification.RedactionConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("ID retention configuration: %w", err)
	}
	return verification.NewRedactionService(
		verification.NewPostgresVerificationStore(pool),
		verification.NewPersonaClient(personaConfig, nil),
		redactionConfig,
	), nil
}
