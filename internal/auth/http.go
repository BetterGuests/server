package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
)

const bannedCode = "account_banned"

type Authenticator struct {
	service *AuthService
}

func NewAuthenticator(service *AuthService) *Authenticator {
	return &Authenticator{service: service}
}

func (auth *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID, err := auth.service.Authenticate(r.Context(), r.Header.Get("Authorization"))
		switch {
		case errors.Is(err, ErrUnauthenticated):
			slog.Warn("Authentication rejected",
				"error", err,
				"method", r.Method,
				"path", r.URL.Path,
				"remote_addr", r.RemoteAddr,
			)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		case errors.Is(err, ErrBanned):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": ErrBanned.Error(), "code": bannedCode})
			return
		case err != nil:
			slog.Error("Authenticate request", "error", err)
			writeError(w, http.StatusInternalServerError, "could not authenticate request")
			return
		}

		next.ServeHTTP(w, r.WithContext(accounts.WithUserID(r.Context(), accountID)))
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
