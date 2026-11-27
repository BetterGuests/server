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
	"github.com/BetterGuests/BetterGuests-Server/internal/automessages"
	"github.com/BetterGuests/BetterGuests-Server/internal/availability"
	"github.com/BetterGuests/BetterGuests-Server/internal/bans"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cancellations"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/BetterGuests/BetterGuests-Server/internal/compliance"
	"github.com/BetterGuests/BetterGuests-Server/internal/email"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/monitoring"
	"github.com/BetterGuests/BetterGuests-Server/internal/nearby"
	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/profiles"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofscan"
	"github.com/BetterGuests/BetterGuests-Server/internal/realtime"
	"github.com/BetterGuests/BetterGuests-Server/internal/reports"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviewdigest"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/screening"
	"github.com/BetterGuests/BetterGuests-Server/internal/settlements"
	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/BetterGuests/BetterGuests-Server/internal/teamevents"
	"github.com/BetterGuests/BetterGuests-Server/internal/usermap"
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
	banStore := bans.NewPostgresBanStore(pool)
	authService := auth.NewAuthService(clerkVerifier, newNamingProvisioner(accountService, nameFetcher, accountStore)).WithBans(banStore)
	authenticator := auth.NewAuthenticator(authService)
	realtimeService := realtime.NewRealtimeService()

	notificationStore := notifications.NewPostgresNotificationStore(pool)
	journeyService := notifications.NewJourneyService(notificationStore, realtimeService)
	tripPublisher := nudgingPublisher{publisher: realtimeService, nudger: journeyService}
	stayStore := stays.NewPostgresStayStore(pool)
	stayService := stays.NewStayService(stayStore, stayStore).WithNudger(journeyService)
	reviewStore := reviews.NewPostgresReviewStore(pool)
	reviewService := reviews.NewReviewService(reviewStore).WithNudger(journeyService)
	if config, enabled := reviewdigest.ConfigFromLookup(os.Getenv); enabled {
		reviewService.WithSummaries(reviewStore, reviewdigest.NewReviewDigestService(config))
	} else {
		slog.Warn("review summaries are off: ANTHROPIC_API_KEY is not set")
	}
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
	if personaConfig.FingerprintKey == "" {
		slog.Warn("one person on several accounts is not detected: ID_FINGERPRINT_KEY is not set")
	}

	autoMessageStore := automessages.NewPostgresAutoMessageStore(pool)
	autoMessageService := automessages.NewAutoMessageService(autoMessageStore, autoMessageStore, autoMessageStore).
		WithPublisher(realtimeService).
		WithNudger(journeyService)
	journeyService.WithAutomaticMessages(autoMessageService)

	messageService := messages.NewMessageService(messages.NewPostgresMessageStore(pool)).
		WithPublisher(realtimeService).
		WithPrivateBodies(autoMessageService).
		WithFocus(realtimeService)
	realtimeService.WithFocusListener(messageService)

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
	vgsConfig, cardArtEnabled, err := payments.VGSConfigFromLookup(os.Getenv)
	if err != nil {
		return routeDeps{}, fmt.Errorf("card art configuration: %w", err)
	}
	if cardArtEnabled {
		paymentService.WithCardArt(payments.NewVGSClient(vgsConfig))
	}

	cancellationService := cancellations.NewCancellationService(
		cancellations.NewPostgresCancellationStore(pool),
		cancellations.NewStripeGateway(paymentConfig.SecretKey),
		paymentService,
		tripPublisher,
	)
	journeyService.WithUpkeep(cancellationService).WithTeamAlerts(notificationStore)
	mailConfig, mailEnabled, err := email.ConfigFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("email configuration: %w", err)
	}
	if mailEnabled {
		journeyService.WithMail(notifications.NewMailService(
			notificationStore,
			resendSender{client: email.NewResendClient(mailConfig, nil)},
			accountEmails{accounts: accountStore, fetcher: nameFetcher},
			mailConfig.WebURL,
		))
	} else {
		slog.Warn("email is off: RESEND_API_KEY is not set")
	}

	bookingStore := bookings.NewPostgresBookingStore(pool)
	bookingService := bookings.NewBookingService(bookingStore).
		WithVerification(currentVerifier).
		WithDetails(bookingStore).
		WithPublisher(tripPublisher).
		WithPayments(paymentService).
		WithBans(banStore)

	objectStore, err := cashback.NewObjectStoreFromEnv()
	if err != nil {
		return routeDeps{}, fmt.Errorf("photo storage configuration: %w", err)
	}
	listingPhotos := listingObjectsFrom(objectStore)
	if listingPhotos != nil {
		cancellationService.WithListingPhotos(listingPhotos)
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
		WithPublishing(listingStore, currentVerifier).
		WithMedia(listingStore, listingPhotos)
	if config, enabled := screening.ConfigFromLookup(os.Getenv); enabled {
		listingService.WithScreening(screening.NewScreeningService(config))
	} else {
		slog.Warn("listing photos are not screened: ANTHROPIC_API_KEY is not set")
	}

	proofStore := proofs.NewPostgresProofStore(pool)
	proofService := proofs.NewProofService(proofStore, proofObjectsFrom(objectStore))
	userDirectory, err := usermap.NewClerkDirectory(clerkConfig.SecretKey)
	if err != nil {
		return routeDeps{}, fmt.Errorf("clerk configuration: %w", err)
	}
	proofHandler := proofs.NewHTTPHandler(proofService, proofService, proofService, proofService, proofService)

	reportService := reports.NewReportService(reports.NewPostgresReportStore(pool), proofService)

	profileService := profiles.NewProfileService(profiles.NewPostgresProfileStore(pool), bannerObjectsFrom(objectStore))

	return routeDeps{
		database:     pool,
		origins:      origins,
		authenticate: authenticator.Middleware,
		listings: listings.NewHTTPHandler(listingService).
			WithPublisher(listingService).
			WithMedia(listingService).
			WithHostReader(listingStore).
			WithPublicReader(listings.NewPublicListingService(listingStore, listingPhotos)),
		proofs:   proofHandler,
		nearby:   buildNearbyHandler(listingStore),
		reviews:  reviews.NewHTTPHandler(reviewService, reviewService).WithSummaries(reviewService),
		bookings: bookings.NewHTTPHandler(bookingService).WithReader(bookingService).WithResponder(bookingService),
		messages: messages.NewHTTPHandler(messageService, messageService, messageService),
		realtime: realtime.NewHTTPHandler(realtimeService, authService, origins).
			WithReceiver(messages.TypingEventKind, messages.NewTypingReceiver(messageService)),
		cashback: cashback.NewHTTPHandler(cashbackService, cashbackService, cashbackService, cashbackService, cashbackService),
		verification: verification.NewHTTPHandler(verificationService, verificationService).WithStatusReader(verificationStore).WithReconciler(verificationService).WithDeletion(deletionService).
			WithReviews(verification.NewReviewService(verificationStore).WithInquiries(verificationStore, personaClient)),
		payments:     payments.NewHTTPHandler(paymentService, paymentService).WithCards(paymentService),
		payouts:      settlements.NewHTTPHandler(payoutService, payoutService),
		compliance:   compliance.NewHTTPHandler(complianceService, complianceService, complianceService),
		profiles:     profiles.NewHTTPHandler(profileService, profileService, profileService, profileService),
		waitlist:     waitlist.NewHTTPHandler(waitlist.NewWaitlistService(waitlist.NewPostgresWaitlistStore(pool))),
		availability: availability.NewHTTPHandler(availability.NewAvailabilityService(availability.NewPostgresAvailabilityStore(pool))),
		stays:        stays.NewHTTPHandler(stayService, stayService),
		autoMessages: automessages.NewHTTPHandler(autoMessageService),
		inbox:        notifications.NewHTTPHandler(notifications.NewNotificationService(notificationStore)),
		tripUpdates:  journeyService,
		cancellations: cancellations.NewHTTPHandler(cancellationService, cancellationService).
			WithListingDesk(cancellationService).
			WithTeamCanceller(cancellationService),
		userMap:    usermap.NewHTTPHandler(usermap.NewUserMapService(userDirectory, proofService)),
		teamEvents: teamevents.NewHTTPHandler(teamevents.NewTeamEventsService(teamevents.NewPostgresTeamEventsStore(pool), proofService)),
		reports:    reports.NewHTTPHandler(reportService, reportService),
		bans:       bans.NewHTTPHandler(bans.NewBanService(banStore, proofService, realtimeService)),
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

type resendSender struct{ client *email.ResendClient }

func (sender resendSender) Send(ctx context.Context, message notifications.Email) error {
	return sender.client.Send(ctx, email.Message{
		To:             message.To,
		Subject:        message.Subject,
		Text:           message.Text,
		IdempotencyKey: message.IdempotencyKey,
	})
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

type cacheableDownloader interface {
	PresignCacheableDownload(ctx context.Context, key string, expires time.Duration) (string, error)
}

type cacheableDownloads struct{ store cashback.ObjectStore }

func (downloads cacheableDownloads) PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error) {
	if cacheable, ok := downloads.store.(cacheableDownloader); ok {
		return cacheable.PresignCacheableDownload(ctx, key, expires)
	}
	return downloads.store.PresignDownload(ctx, key, expires)
}

type listingObjects struct {
	store     cashback.ObjectStore
	downloads *listings.ReusedPhotoSigner
}

func listingObjectsFrom(store cashback.ObjectStore) listings.PhotoObjects {
	if store == nil {
		return nil
	}
	return listingObjects{store: store, downloads: listings.NewReusedPhotoSigner(cacheableDownloads{store: store})}
}

func (objects listingObjects) PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error) {
	return objects.store.PresignUpload(ctx, key, contentType, expires)
}

func (objects listingObjects) PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error) {
	return objects.downloads.PresignDownload(ctx, key, expires)
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

func buildDocumentRetention(pool *pgxpool.Pool) (*proofs.FileRetentionService, error) {
	objectStore, err := cashback.NewObjectStoreFromEnv()
	if err != nil {
		return nil, fmt.Errorf("document storage configuration: %w", err)
	}
	var files proofs.FileRemover
	if remover, ok := objectStore.(objectRemover); ok {
		files = remover
	}
	return proofs.NewFileRetentionService(proofs.NewPostgresProofStore(pool), files), nil
}

type documentReader interface {
	Read(ctx context.Context, key string, maxBytes int64) ([]byte, error)
}

func buildProofScan(pool *pgxpool.Pool) *proofscan.Service {
	if !proofscan.Enabled(os.Getenv) {
		return nil
	}
	if missing := proofscan.MissingTools(); len(missing) > 0 {
		slog.Warn("ownership documents are not scanned: OCR tools are missing", "missing", missing)
		return nil
	}
	objectStore, err := cashback.NewObjectStoreFromEnv()
	if err != nil {
		slog.Warn("ownership documents are not scanned: document storage is not configured", "error", err)
		return nil
	}
	reader, ok := objectStore.(documentReader)
	if !ok {
		slog.Warn("ownership documents are not scanned: document storage cannot be read")
		return nil
	}
	return proofscan.NewService(proofscan.NewPostgresStore(pool, proofscan.AutoApproveEnabled(os.Getenv)), reader, proofscan.NewCommandExtractor())
}

func buildVerificationPauses(pool *pgxpool.Pool) *verification.VerificationPauseService {
	return verification.NewVerificationPauseService(verification.NewPostgresVerificationStore(pool))
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
