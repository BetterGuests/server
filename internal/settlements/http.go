package settlements

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
)

type HTTPHandler struct {
	reader  PayoutReader
	starter PayoutStarter
}

func NewHTTPHandler(reader PayoutReader, starter PayoutStarter) *HTTPHandler {
	return &HTTPHandler{reader: reader, starter: starter}
}

func (handler *HTTPHandler) GetPayouts(w http.ResponseWriter, r *http.Request) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	status, err := handler.reader.Status(r.Context(), hostID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (handler *HTTPHandler) StartOnboarding(w http.ResponseWriter, r *http.Request) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	link, err := handler.starter.StartOnboarding(r.Context(), hostID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"url": link})
}

func writeServiceError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrOnboardingDisabled) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	slog.Error("Payout request", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not complete request"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
