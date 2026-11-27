package settlements

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type HTTPHandler struct {
	reader  PayoutReader
	starter PayoutStarter
	blocked BlockedReviewer
}

func NewHTTPHandler(reader PayoutReader, starter PayoutStarter) *HTTPHandler {
	return &HTTPHandler{reader: reader, starter: starter}
}

func (handler *HTTPHandler) WithBlockedReviewer(blocked BlockedReviewer) *HTTPHandler {
	handler.blocked = blocked
	return handler
}

func (handler *HTTPHandler) ListBlocked(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	blocked, err := handler.blocked.Blocked(r.Context(), adminID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]BlockedSettlement{"settlements": blocked})
}

func (handler *HTTPHandler) RetryBlocked(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	bookingID, err := uuid.Parse(r.PathValue("booking_id"))
	if err != nil {
		writeServiceError(w, ErrNotBlocked)
		return
	}
	if err := handler.blocked.Retry(r.Context(), adminID, bookingID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(StatusProcessing)})
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
	switch {
	case errors.Is(err, ErrNotAdmin):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, ErrNotBlocked), errors.Is(err, ErrOwnBooking):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
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
