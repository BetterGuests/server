package main

import (
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/BetterGuests/BetterGuests-Server/internal/compliance"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/middleware"
	"github.com/BetterGuests/BetterGuests-Server/internal/nearby"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/profiles"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/BetterGuests/BetterGuests-Server/internal/realtime"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/go-chi/cors"
)

type routeDeps struct {
	database     databasePinger
	origins      []string
	authenticate func(http.Handler) http.Handler
	listings     *listings.HTTPHandler
	proofs       *proofs.HTTPHandler
	nearby       *nearby.HTTPHandler
	reviews      *reviews.HTTPHandler
	bookings     *bookings.HTTPHandler
	cashback     *cashback.HTTPHandler
	verification *verification.HTTPHandler
	payments     *payments.HTTPHandler
	compliance   *compliance.HTTPHandler
	profiles     *profiles.HTTPHandler
	messages     *messages.HTTPHandler
	realtime     *realtime.HTTPHandler
}

func routes(deps routeDeps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /ready", ready(deps.database))
	deps.mountVerification(mux)
	deps.mountPayments(mux)
	deps.mountPublicListings(mux)
	deps.mountNearby(mux)
	deps.mountRealtime(mux)
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

func (deps routeDeps) mountRealtime(mux *http.ServeMux) {
	if deps.realtime == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/realtime", deps.realtime.Connect)
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
	api.HandleFunc("GET /api/v1/admin/me", deps.proofs.Me)
	api.HandleFunc("GET /api/v1/admin/property-proofs", deps.proofs.ListSubmitted)
	api.HandleFunc("POST /api/v1/admin/property-proofs/{proof_id}/decision", deps.proofs.Decide)
	api.HandleFunc("POST /api/v1/reviews", deps.reviews.CreateReview)
	api.Handle("POST /api/v1/bookings", deps.compliance.RequireAdult(http.HandlerFunc(deps.bookings.CreateBooking)))
	api.HandleFunc("GET /api/v1/bookings", deps.bookings.ListMyBookings)
	api.HandleFunc("GET /api/v1/bookings/{booking_id}", deps.bookings.GetBooking)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cancel", deps.bookings.CancelBooking)
	api.HandleFunc("GET /api/v1/hosting/booking-requests", deps.bookings.ListHostRequests)
	api.HandleFunc("POST /api/v1/hosting/bookings/{booking_id}/approve", deps.bookings.ApproveBooking)
	api.HandleFunc("POST /api/v1/hosting/bookings/{booking_id}/decline", deps.bookings.DeclineBooking)
	api.HandleFunc("GET /api/v1/conversations", deps.messages.ListConversations)
	api.Handle("POST /api/v1/conversations", deps.compliance.RequireAdult(http.HandlerFunc(deps.messages.OpenConversation)))
	api.HandleFunc("GET /api/v1/conversations/{conversation_id}", deps.messages.GetConversation)
	api.HandleFunc("GET /api/v1/conversations/{conversation_id}/messages", deps.messages.ListMessages)
	api.HandleFunc("POST /api/v1/conversations/{conversation_id}/messages", deps.messages.SendMessage)
	api.HandleFunc("POST /api/v1/conversations/{conversation_id}/read", deps.messages.MarkRead)
	api.HandleFunc("GET /api/v1/bookings/{booking_id}/cashback", deps.cashback.GetTrip)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cashback/tasks/{task_key}/photo-upload", deps.cashback.RequestUpload)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cashback/tasks/{task_key}/photo", deps.cashback.SubmitPhoto)
	api.HandleFunc("GET /api/v1/hosting/bookings/{booking_id}/cashback", deps.cashback.GetHostTrip)
	api.HandleFunc("GET /api/v1/hosting/cashback/pending", deps.cashback.ListPending)
	api.HandleFunc("GET /api/v1/profile/banner", deps.profiles.GetBanner)
	api.HandleFunc("POST /api/v1/profile/banner/upload", deps.profiles.RequestUpload)
	api.HandleFunc("PUT /api/v1/profile/banner", deps.profiles.SaveBanner)
	api.HandleFunc("DELETE /api/v1/profile/banner", deps.profiles.RemoveBanner)
	api.HandleFunc("GET /api/v1/verification/status", deps.verification.GetStatus)
	api.HandleFunc("POST /api/v1/cashback/submissions/{submission_id}/review", deps.cashback.ReviewSubmission)
	mux.Handle("/api/v1/", deps.authenticate(api))
}

func withMiddleware(next http.Handler, origins []string) http.Handler {
	corsHandler := cors.Handler(cors.Options{
		AllowedOrigins: origins,
		AllowedMethods: []string{
			"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS",
		},
		AllowedHeaders: []string{
			"Authorization", "Content-Type",
		},
		AllowCredentials: false,
	})(next)

	return middleware.Recover(
		middleware.SecurityHeaders(
			middleware.RequireHTTPS(
				middleware.RateLimit(middleware.DefaultRateLimiterConfig())(corsHandler),
			),
		),
	)
}
