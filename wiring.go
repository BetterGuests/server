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
	"github.com/BetterGuests/BetterGuests-Server/internal/availability"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cancellations"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/BetterGuests/BetterGuests-Server/internal/compliance"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/monitoring"
	"github.com/BetterGuests/BetterGuests-Server/internal/nearby"
	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/profiles"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/BetterGuests/BetterGuests-Server/internal/realtime"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/screening"
	"github.com/BetterGuests/BetterGuests-Server/internal/settlements"
	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
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

	notificationStore := notifications.NewPostgresNotificationStore(pool)
	journeyService := notifications.NewJourneyService(notificationStore, realtimeService)
	tripPublisher := nudgingPublisher{publisher: realtimeService, nudger: journeyService}
	stayStore := stays.NewPostgresStayStore(pool)
	stayService := stays.NewStayService(stayStore, stayStore).WithNudger(journeyService)
	reviewService := reviews.NewReviewService(reviews.NewPostgresReviewStore(pool)).WithNudger(journeyService)
	journeyService.WithLifecycle(stayService, reviewService)

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
	paymentStore := payments.NewPostgresPaymentStore(pool)
	paymentService := payments.NewPaymentService(paymentStore, stripeGateway, stripeGateway, paymentConfig).
		WithSavedCards(stripeGateway, paymentStore).
		WithController(stripeGateway).
		WithPublisher(tripPublisher)

	cancellationService := cancellations.NewCancellationService(
		cancellations.NewPostgresCancellationStore(pool),
		cancellations.NewStripeGateway(paymentConfig.SecretKey),
		paymentService,
		tripPublisher,
	)
	journeyService.WithUpkeep(cancellationService)

	bookingStore := bookings.NewPostgresBookingStore(pool)
	bookingService := bookings.NewBookingService(bookingStore).
		WithVerification(currentVerifier).
		WithDetails(bookingStore).
		WithPublisher(tripPublisher).
		WithPayments(paymentService)

	objectStore, err := cashback.NewObjectStoreFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("photo storage configuration: %w", err)
	}
	cashbackService := cashback.NewCashbackService(
		cashback.NewPostgresCashbackStore(pool),
		verificationStore,
		objectStore,
	)

	settlementConfig, err := settlements.ConfigFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("settlement configuration: %w", err)
	}
	payoutService := settlements.NewPayoutService(
		settlements.NewPostgresSettlementStore(pool),
		settlements.NewStripeGateway(paymentConfig.SecretKey),
		accountEmails{accounts: accountStore, fetcher: nameFetcher},
		settlementConfig,
	)

	listingStore := listings.NewPostgresListingStore(pool)
	listingService := listings.NewListingService(listingStore).
		WithPublishing(listingStore, verificationStore).
		WithMedia(listingStore, listingObjectsFrom(objectStore))
	if config, enabled := screening.ConfigFromLookup(os.Getenv); enabled {
		listingService.WithScreening(screening.NewScreeningService(config))
	} else {
		slog.Warn("listing photos are not screened: ANTHROPIC_API_KEY is not set")
	}

	proofStore := proofs.NewPostgresProofStore(pool)
	proofService := proofs.NewProofService(proofStore, proofObjectsFrom(objectStore))
	proofHandler := proofs.NewHTTPHandler(proofService, proofService, proofService, proofService, proofService)
	checksEnabled := personaConfig.PropertyTemplateID != ""
	var checkInquiries proofs.InquiryStarter
	if checksEnabled {
		checkInquiries = propertyInquiries{client: personaClient, templateID: personaConfig.PropertyTemplateID}
	}
	checkService := proofs.NewCheckService(proofStore, checkInquiries, currentIDs{checker: currentVerifier})
	if checksEnabled {
		proofHandler.WithChecks(checkService)
	} else {
		slog.Warn("listing documents are reviewed by hand: PERSONA_PROPERTY_TEMPLATE_ID is not set")
	}
	verificationService.WithOtherInquiries(propertyEvents{checks: checkService})

	profileService := profiles.NewProfileService(profiles.NewPostgresProfileStore(pool), bannerObjectsFrom(objectStore))

	return routeDeps{
		database:     pool,
		origins:      origins,
		authenticate: authenticator.Middleware,
		listings: listings.NewHTTPHandler(listingService).
			WithPublisher(listingService).
			WithMedia(listingService).
			WithHostReader(listingStore).
			WithPublicReader(listings.NewPublicListingService(listingStore, objectStore)),
		proofs:        proofHandler,
		nearby:        buildNearbyHandler(listingStore),
		reviews:       reviews.NewHTTPHandler(reviewService, reviewService),
		bookings:      bookings.NewHTTPHandler(bookingService).WithReader(bookingService).WithResponder(bookingService),
		messages:      messages.NewHTTPHandler(messageService, messageService, messageService),
		realtime:      realtime.NewHTTPHandler(realtimeService, authService, origins),
		cashback:      cashback.NewHTTPHandler(cashbackService, cashbackService, cashbackService, cashbackService, cashbackService),
		verification:  verification.NewHTTPHandler(verificationService, verificationService).WithStatusReader(verificationStore).WithCurrentChecker(currentVerifier).WithDeletion(deletionService),
		payments:      payments.NewHTTPHandler(paymentService, paymentService),
		payouts:       settlements.NewHTTPHandler(payoutService, payoutService),
		compliance:    compliance.NewHTTPHandler(complianceService, complianceService, complianceService),
		profiles:      profiles.NewHTTPHandler(profileService, profileService, profileService, profileService),
		waitlist:      waitlist.NewHTTPHandler(waitlist.NewWaitlistService(waitlist.NewPostgresWaitlistStore(pool))),
		availability:  availability.NewHTTPHandler(availability.NewAvailabilityService(availability.NewPostgresAvailabilityStore(pool))),
		stays:         stays.NewHTTPHandler(stayService, stayService),
		inbox:         notifications.NewHTTPHandler(notifications.NewNotificationService(notificationStore)),
		tripUpdates:   journeyService,
		cancellations: cancellations.NewHTTPHandler(cancellationService, cancellationService),
	}, nil
}

type realtimePublisher interface {
	Publish(userID uuid.UUID, kind string, payload any)
}

type nudger interface {
	Nudge()
}

type nudgingPublisher struct {
	publisher realtimePublisher
	nudger    nudger
}

func (trip nudgingPublisher) Publish(userID uuid.UUID, kind string, payload any) {
	trip.publisher.Publish(userID, kind, payload)
	trip.nudger.Nudge()
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

type objectContent interface {
	Read(ctx context.Context, key string, maxBytes int64) ([]byte, error)
	Write(ctx context.Context, key, contentType string, data []byte) error
}

type listingObjects struct{ store cashback.ObjectStore }

func listingObjectsFrom(store cashback.ObjectStore) listings.PhotoObjects {
	if store == nil {
		return nil
	}
	return listingObjects{store: store}
}

func (objects listingObjects) PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error) {
	return objects.store.PresignUpload(ctx, key, contentType, expires)
}

func (objects listingObjects) PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error) {
	return objects.store.PresignDownload(ctx, key, expires)
}

func (objects listingObjects) Stat(ctx context.Context, key string) (listings.ObjectInfo, error) {
	info, err := objects.store.Stat(ctx, key)
	if err != nil {
		return listings.ObjectInfo{}, err
	}
	return listings.ObjectInfo{Size: info.Size, ContentType: info.ContentType}, nil
}

func (objects listingObjects) Read(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	content, ok := objects.store.(objectContent)
	if !ok {
		return nil, errors.New("storage cannot read objects")
	}
	return content.Read(ctx, key, maxBytes)
}

func (objects listingObjects) Write(ctx context.Context, key, contentType string, data []byte) error {
	content, ok := objects.store.(objectContent)
	if !ok {
		return errors.New("storage cannot write objects")
	}
	return content.Write(ctx, key, contentType, data)
}

func (objects listingObjects) Remove(ctx context.Context, key string) error {
	remover, ok := objects.store.(objectRemover)
	if !ok {
		return nil
	}
	return remover.Remove(ctx, key)
}

type propertyInquiries struct {
	client     *verification.PersonaClient
	templateID string
}

func (inquiries propertyInquiries) StartInquiry(ctx context.Context, referenceID string, fields map[string]string) (proofs.CheckTicket, error) {
	inquiry, err := inquiries.client.CreatePrefilledInquiry(ctx, inquiries.templateID, referenceID, fields)
	if err != nil {
		return proofs.CheckTicket{}, err
	}
	return proofs.CheckTicket{InquiryID: inquiry.ID, SessionToken: inquiry.SessionToken}, nil
}

type currentIDs struct{ checker verification.CurrentChecker }

func (ids currentIDs) HasCurrentID(ctx context.Context, accountID uuid.UUID) (bool, error) {
	_, current, err := ids.checker.CurrentVerificationID(ctx, accountID)
	return current, err
}

type propertyEvents struct{ checks *proofs.CheckService }

func (events propertyEvents) Claim(ctx context.Context, inquiry verification.OtherInquiry) (bool, error) {
	return events.checks.Process(ctx, proofs.CheckEvent{
		AccountID: inquiry.AccountID, InquiryID: inquiry.InquiryID, Name: inquiry.EventName, Body: inquiry.Body,
	})
}

func buildSettlementService(pool *pgxpool.Pool) (*settlements.SettlementService, error) {
	paymentConfig, err := payments.ConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("stripe configuration: %w", err)
	}
	settlementConfig, err := settlements.ConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("settlement configuration: %w", err)
	}
	return settlements.NewSettlementService(
		settlements.NewPostgresSettlementStore(pool),
		settlements.NewStripeGateway(paymentConfig.SecretKey),
		settlementConfig,
	), nil
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
