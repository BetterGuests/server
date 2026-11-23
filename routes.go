package main

import (
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/BetterGuests/BetterGuests-Server/internal/compliance"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/middleware"
	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/go-chi/cors"
)

type routeDeps struct {
	database     databasePinger
	origins      []string
	authenticate func(http.Handler) http.Handler
	listings     *listings.HTTPHandler
	reviews      *reviews.HTTPHandler
	bookings     *bookings.HTTPHandler
	cashback     *cashback.HTTPHandler
	verification *verification.HTTPHandler
	payments     *payments.HTTPHandler
	compliance   *compliance.HTTPHandler
}

func routes(deps routeDeps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /ready", ready(deps.database))
	deps.mountVerification(mux)
	deps.mountPayments(mux)
	deps.mountPublicListings(mux)
	deps.mountAuthenticatedAPI(mux)
	return withMiddleware(mux, deps.origins)
}

func (deps routeDeps) mountVerification(mux *http.ServeMux) {
	if deps.verification == nil || deps.authenticate == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/webhooks/persona", deps.verification.PersonaWebhook)
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

func (deps routeDeps) mountAuthenticatedAPI(mux *http.ServeMux) {
	if deps.authenticate == nil {
		return
	}
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/compliance/status", deps.compliance.GetStatus)
	api.HandleFunc("POST /api/v1/compliance/age", deps.compliance.ConfirmAge)
	api.HandleFunc("POST /api/v1/compliance/consents", deps.compliance.GrantConsent)
	api.Handle("POST /api/v1/listings", deps.compliance.RequireAdult(http.HandlerFunc(deps.listings.CreateListing)))
	api.HandleFunc("POST /api/v1/reviews", deps.reviews.CreateReview)
	api.Handle("POST /api/v1/bookings", deps.compliance.RequireAdult(http.HandlerFunc(deps.bookings.CreateBooking)))
	api.HandleFunc("GET /api/v1/bookings/{booking_id}/cashback", deps.cashback.GetTrip)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cashback/tasks/{task_key}/photo-upload", deps.cashback.RequestUpload)
	api.HandleFunc("POST /api/v1/bookings/{booking_id}/cashback/tasks/{task_key}/photo", deps.cashback.SubmitPhoto)
	api.HandleFunc("GET /api/v1/hosting/bookings/{booking_id}/cashback", deps.cashback.GetHostTrip)
	api.HandleFunc("GET /api/v1/hosting/cashback/pending", deps.cashback.ListPending)
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
