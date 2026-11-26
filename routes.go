package main

import (
	"context"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/availability"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cancellations"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/BetterGuests/BetterGuests-Server/internal/compliance"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/middleware"
	"github.com/BetterGuests/BetterGuests-Server/internal/nearby"
	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/profiles"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/BetterGuests/BetterGuests-Server/internal/realtime"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/settlements"
	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/BetterGuests/BetterGuests-Server/internal/teamevents"
	"github.com/BetterGuests/BetterGuests-Server/internal/usermap"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/BetterGuests/BetterGuests-Server/internal/waitlist"
	"github.com/go-chi/cors"
)

type routeDeps struct {
	database      databasePinger
	origins       []string
	authenticate  func(http.Handler) http.Handler
	listings      *listings.HTTPHandler
	proofs        *proofs.HTTPHandler
	nearby        *nearby.HTTPHandler
	reviews       *reviews.HTTPHandler
	bookings      *bookings.HTTPHandler
	cashback      *cashback.HTTPHandler
	verification  *verification.HTTPHandler
	payments      *payments.HTTPHandler
	payouts       *settlements.HTTPHandler
	compliance    *compliance.HTTPHandler
	profiles      *profiles.HTTPHandler
	messages      *messages.HTTPHandler
	realtime      *realtime.HTTPHandler
	waitlist      *waitlist.HTTPHandler
	availability  *availability.HTTPHandler
	stays         *stays.HTTPHandler
	inbox         *notifications.HTTPHandler
	tripUpdates   backgroundJob
	cancellations *cancellations.HTTPHandler
	userMap       *usermap.HTTPHandler
	teamEvents    *teamevents.HTTPHandler
}

type backgroundJob interface {
	Run(ctx context.Context)
}

func routes(deps routeDeps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /ready", ready(deps.database))
	deps.mountVerification(mux)
	deps.mountPayments(mux)
	deps.mountPublicListings(mux)
	deps.mountNearby(mux)
	deps.mountAvailability(mux)
	deps.mountRealtime(mux)
	deps.mountWaitlist(mux)
	deps.mountListingReviews(mux)
	deps.mountAuthenticatedAPI(mux)
	return withMiddleware(mux, deps.origins)
}

func (deps routeDeps) mountVerification(mux *http.ServeMux) {
	if deps.verification == nil || deps.authenticate == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/webhooks/persona", deps.verification.PersonaWebhook)
	mux.Handle("DELETE /api/v1/verification/data", deps.authenticate(http.HandlerFunc(deps.verification.DeleteIDData)))
	mux.Handle("POST /api/v1/verification/inquiries", deps.authenticate(deps.compliance.RequireAdult(http.HandlerFunc(deps.verification.StartVerification))))
}

func (deps routeDeps) mountPayments(mux *http.ServeMux) {
	if deps.payments == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/webhooks/stripe", deps.payments.StripeWebhook)
	if deps.authenticate != nil {
		mux.Handle("POST /api/v1/payments/checkout", deps.authenticate(deps.compliance.RequireAdult(http.HandlerFunc(deps.payments.StartCheckout))))
		mux.Handle("GET /api/v1/payments/cards", deps.authenticate(http.HandlerFunc(deps.payments.ListSavedCards)))
		mux.Handle("DELETE /api/v1/payments/cards/{card_id}", deps.authenticate(http.HandlerFunc(deps.payments.RemoveSavedCard)))
		mux.Handle("PUT /api/v1/payments/cards/{card_id}/style", deps.authenticate(http.HandlerFunc(deps.payments.StyleSavedCard)))
		mux.Handle("POST /api/v1/payments/cards/{card_id}/art/session", deps.authenticate(deps.compliance.RequireAdult(http.HandlerFunc(deps.payments.StartCardArt))))
		mux.Handle("PUT /api/v1/payments/cards/{card_id}/art", deps.authenticate(deps.compliance.RequireAdult(http.HandlerFunc(deps.payments.LinkCardArt))))
	}
}

func (deps routeDeps) mountPublicListings(mux *http.ServeMux) {
	if deps.listings == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/listings", deps.listings.ListListings)
	mux.HandleFunc("GET /api/v1/listings/{listing_id}", deps.listings.GetListing)
}

func (deps routeDeps) mountNearby(mux *http.ServeMux) {
	if deps.nearby == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/listings/{listing_id}/nearby", deps.nearby.Search)
}

func (deps routeDeps) mountAvailability(mux *http.ServeMux) {
	if deps.availability == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/listings/{listing_id}/booked-dates", deps.availability.ListBookedDates)
}

func (deps routeDeps) mountListingReviews(mux *http.ServeMux) {
	if deps.reviews == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/listings/{listing_id}/reviews", deps.reviews.ListingReviews)
	mux.HandleFunc("GET /api/v1/listings/{listing_id}/reviews/summary", deps.reviews.ListingReviewSummary)
}

func (deps routeDeps) mountRealtime(mux *http.ServeMux) {
	if deps.realtime == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/realtime", deps.realtime.Connect)
}

func (deps routeDeps) mountWaitlist(mux *http.ServeMux) {
	if deps.waitlist == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/waitlist", deps.waitlist.Join)
}

func (deps routeDeps) mountAuthenticatedAPI(mux *http.ServeMux) {
	if deps.authenticate == nil {
		return
	}
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/compliance/status", deps.compliance.GetStatus)
	api.HandleFunc("POST /api/v1/compliance/age", deps.compliance.ConfirmAge)
	api.HandleFunc("POST /api/v1/compliance/consents", deps.compliance.GrantConsent)
	api.Handle("POST /api/v1/listings", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.CreateListing)))
	api.Handle("POST /api/v1/listings/{listing_id}/publish", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.PublishListing)))
	api.Handle("POST /api/v1/listings/{listing_id}/proof/uploads", deps.compliance.RequireAdult(http.HandlerFunc(deps.proofs.RequestUpload)))
	api.Handle("POST /api/v1/listings/{listing_id}/proof", deps.compliance.RequireAdult(http.HandlerFunc(deps.proofs.Submit)))
	api.HandleFunc("GET /api/v1/listings/{listing_id}/proof", deps.proofs.Status)
	api.HandleFunc("GET /api/v1/listings/{listing_id}/setup", deps.listings.GetSetup)
	api.Handle("POST /api/v1/listings/{listing_id}/photos/uploads", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.RequestPhotoUpload)))
	api.Handle("POST /api/v1/listings/{listing_id}/photos", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.AddPhoto)))
	api.Handle("DELETE /api/v1/listings/{listing_id}/photos/{photo_id}", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.RemovePhoto)))
	api.Handle("PUT /api/v1/listings/{listing_id}/photos/order", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.ReorderPhotos)))
	api.Handle("PUT /api/v1/listings/{listing_id}/price", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.SetPrice)))
	api.Handle("DELETE /api/v1/listings/{listing_id}", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.DeleteListing)))
	api.HandleFunc("GET /api/v1/admin/me", deps.proofs.Me)
	api.HandleFunc("GET /api/v1/admin/user-map", deps.userMap.Summary)
	api.HandleFunc("GET /api/v1/admin/events", deps.teamEvents.Feed)
	api.HandleFunc("GET /api/v1/admin/property-proofs", deps.proofs.ListForReview)
	api.HandleFunc("POST /api/v1/admin/property-proofs/{proof_id}/revoke", deps.proofs.Revoke)
	api.HandleFunc("GET /api/v1/admin/identity-reviews", deps.verification.ListReviews)
	api.HandleFunc("POST /api/v1/admin/identity-reviews/{verification_id}/decision", deps.verification.DecideReview)
	api.HandleFunc("POST /api/v1/admin/property-proofs/{proof_id}/decision", deps.proofs.Decide)
	api.HandleFunc("POST /api/v1/reviews", deps.reviews.CreateReview)
	api.HandleFunc("GET /api/v1/reviews/pending", deps.reviews.PendingReviews)
	api.HandleFunc("GET /api/v1/reviews/about-me", deps.reviews.ReviewsAboutMe)
	api.HandleFunc("GET /api/v1/bookings/{booking_id}/reviews", deps.reviews.BookingReviews)
	api.HandleFunc("GET /api/v1/hosting/bookings/{booking_id}/guest-reviews", deps.reviews.BookingGuestReviews)
	api.HandleFunc("GET /api/v1/hosting/bookings/{booking_id}/guest-reviews/summary", deps.reviews.BookingGuestReviewSummary)
	api.HandleFunc("GET /api/v1/bookings/{booking_id}/stay", deps.stays.GetStay)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/check-in", deps.stays.CheckIn)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/check-out", deps.stays.CheckOut)
	api.HandleFunc("GET /api/v1/listings/{listing_id}/arrival-guide", deps.stays.GetGuide)
	api.Handle("PUT /api/v1/listings/{listing_id}/arrival-guide", deps.compliance.RequireAdult(http.HandlerFunc(deps.stays.SaveGuide)))
	api.HandleFunc("GET /api/v1/bookings/{booking_id}/cancellation", deps.cancellations.Options)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cancellation", deps.cancellations.Cancel)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/report", deps.cancellations.Report)
	api.HandleFunc("GET /api/v1/admin/stay-reports", deps.cancellations.OpenReports)
	api.HandleFunc("GET /api/v1/admin/listings", deps.cancellations.AdminListings)
	api.HandleFunc("GET /api/v1/admin/listings/popular", deps.cancellations.PopularListings)
	api.HandleFunc("POST /api/v1/admin/listings/{listing_id}/pause", deps.cancellations.PauseListing)
	api.HandleFunc("DELETE /api/v1/admin/listings/{listing_id}/pause", deps.cancellations.UnpauseListing)
	api.HandleFunc("POST /api/v1/admin/stay-reports/{booking_id}/decision", deps.cancellations.Resolve)
	api.HandleFunc("GET /api/v1/notifications", deps.inbox.List)
	api.HandleFunc("POST /api/v1/notifications/read", deps.inbox.MarkAllRead)
	api.HandleFunc("POST /api/v1/notifications/{notification_id}/read", deps.inbox.MarkRead)
	api.Handle("POST /api/v1/bookings", deps.compliance.RequireAdult(http.HandlerFunc(deps.bookings.CreateBooking)))
	api.HandleFunc("GET /api/v1/bookings", deps.bookings.ListMyBookings)
	api.HandleFunc("GET /api/v1/bookings/{booking_id}", deps.bookings.GetBooking)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cancel", deps.bookings.CancelBooking)
	api.HandleFunc("GET /api/v1/hosting/booking-requests", deps.bookings.ListHostRequests)
	api.HandleFunc("GET /api/v1/hosting/bookings", deps.bookings.ListHostBookings)
	api.HandleFunc("GET /api/v1/hosting/listings", deps.listings.ListHostListings)
	api.HandleFunc("POST /api/v1/hosting/bookings/{booking_id}/approve", deps.bookings.ApproveBooking)
	api.HandleFunc("POST /api/v1/hosting/bookings/{booking_id}/decline", deps.bookings.DeclineBooking)
	api.HandleFunc("GET /api/v1/conversations", deps.messages.ListConversations)
	api.Handle("POST /api/v1/conversations", deps.compliance.RequireAdult(http.HandlerFunc(deps.messages.OpenConversation)))
	api.HandleFunc("GET /api/v1/conversations/{conversation_id}", deps.messages.GetConversation)
	api.HandleFunc("GET /api/v1/conversations/{conversation_id}/messages", deps.messages.ListMessages)
	api.HandleFunc("POST /api/v1/conversations/{conversation_id}/messages", deps.messages.SendMessage)
	api.HandleFunc("POST /api/v1/conversations/{conversation_id}/read", deps.messages.MarkRead)
	api.HandleFunc("POST /api/v1/conversations/{conversation_id}/typing", deps.messages.Typing)
	api.HandleFunc("DELETE /api/v1/conversations/{conversation_id}/messages/{message_id}", deps.messages.DeleteMessage)
	api.HandleFunc("POST /api/v1/conversations/{conversation_id}/messages/{message_id}/reactions", deps.messages.ReactToMessage)
	api.HandleFunc("GET /api/v1/bookings/{booking_id}/cashback", deps.cashback.GetTrip)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cashback/tasks/{task_key}/photo-upload", deps.cashback.RequestUpload)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cashback/tasks/{task_key}/photo", deps.cashback.SubmitPhoto)
	api.HandleFunc("GET /api/v1/hosting/bookings/{booking_id}/cashback", deps.cashback.GetHostTrip)
	api.HandleFunc("GET /api/v1/hosting/cashback/pending", deps.cashback.ListPending)
	api.HandleFunc("GET /api/v1/hosting/payouts", deps.payouts.GetPayouts)
	api.Handle("POST /api/v1/hosting/payouts/onboarding", deps.compliance.RequireAdult(http.HandlerFunc(deps.payouts.StartOnboarding)))
	api.HandleFunc("GET /api/v1/profile/banner", deps.profiles.GetBanner)
	api.HandleFunc("POST /api/v1/profile/banner/upload", deps.profiles.RequestUpload)
	api.HandleFunc("PUT /api/v1/profile/banner", deps.profiles.SaveBanner)
	api.HandleFunc("DELETE /api/v1/profile/banner", deps.profiles.RemoveBanner)
	api.HandleFunc("GET /api/v1/verification/status", deps.verification.GetStatus)
	api.HandleFunc("POST /api/v1/cashback/submissions/{submission_id}/review", deps.cashback.ReviewSubmission)
	mux.Handle("/api/v1/", deps.authenticate(api))
}

const preflightCacheSeconds = 600

func withMiddleware(next http.Handler, origins []string) http.Handler {
	limited := middleware.RateLimit(middleware.DefaultRateLimiterConfig())(next)
	corsHandler := cors.Handler(cors.Options{
		AllowedOrigins: origins,
		AllowedMethods: []string{
			"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS",
		},
		AllowedHeaders: []string{
			"Authorization", "Content-Type",
		},
		AllowCredentials: false,
		MaxAge:           preflightCacheSeconds,
	})(limited)

	return middleware.Recover(
		middleware.SecurityHeaders(
			middleware.RequireHTTPS(corsHandler),
		),
	)
}
