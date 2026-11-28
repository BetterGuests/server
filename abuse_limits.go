package main

import (
	"net/http"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/middleware"
)

const day = 24 * time.Hour

var (
	accountWritesBudget  = middleware.RateLimiterConfig{RequestsPerWindow: 120, Window: time.Minute}
	listingCreateBudget  = middleware.RateLimiterConfig{RequestsPerWindow: 10, Window: day}
	calendarSyncBudget   = middleware.RateLimiterConfig{RequestsPerWindow: 30, Window: time.Hour}
	pmsSignInBudget      = middleware.RateLimiterConfig{RequestsPerWindow: 20, Window: time.Hour}
	pmsPreviewBudget     = middleware.RateLimiterConfig{RequestsPerWindow: 20, Window: time.Hour}
	pmsImportBudget      = middleware.RateLimiterConfig{RequestsPerWindow: 10, Window: time.Hour}
	listingPublishBudget = middleware.RateLimiterConfig{RequestsPerWindow: 20, Window: day}
	photoUploadBudget    = middleware.RateLimiterConfig{RequestsPerWindow: 200, Window: time.Hour}
	proofUploadBudget    = middleware.RateLimiterConfig{RequestsPerWindow: 10, Window: time.Hour}
	bookingRequestBudget = middleware.RateLimiterConfig{RequestsPerWindow: 10, Window: time.Hour}
	conversationBudget   = middleware.RateLimiterConfig{RequestsPerWindow: 20, Window: time.Hour}
	messageBudget        = middleware.RateLimiterConfig{RequestsPerWindow: 30, Window: time.Minute}
	reviewBudget         = middleware.RateLimiterConfig{RequestsPerWindow: 20, Window: time.Hour}
	stayReportBudget     = middleware.RateLimiterConfig{RequestsPerWindow: 10, Window: time.Hour}
	userReportBudget     = middleware.RateLimiterConfig{RequestsPerWindow: 10, Window: time.Hour}
	idCheckBudget        = middleware.RateLimiterConfig{RequestsPerWindow: 5, Window: day}
	checkoutBudget       = middleware.RateLimiterConfig{RequestsPerWindow: 20, Window: time.Hour}
	cardArtBudget        = middleware.RateLimiterConfig{RequestsPerWindow: 10, Window: time.Hour}
	waitlistJoinBudget   = middleware.RateLimiterConfig{RequestsPerWindow: 5, Window: time.Hour}
)

func accountOrIP(r *http.Request) string {
	if accountID, ok := accounts.UserID(r.Context()); ok {
		return "account:" + accountID.String()
	}
	return "ip:" + middleware.ClientIP(r)
}

func perAccount(budget middleware.RateLimiterConfig, next http.Handler) http.Handler {
	return middleware.RateLimitBy(budget, accountOrIP)(next)
}

func perAccountFunc(budget middleware.RateLimiterConfig, next http.HandlerFunc) http.Handler {
	return perAccount(budget, next)
}

func limitAccountWrites(next http.Handler) http.Handler {
	limited := perAccount(accountWritesBudget, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isReadOnlyMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		limited.ServeHTTP(w, r)
	})
}

func isReadOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
