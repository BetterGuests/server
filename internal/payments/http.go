package payments

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

const (
	maxWebhookBodyBytes = 1 << 20
	signatureHeaderName = "Stripe-Signature"
)

type HTTPHandler struct {
	starter   CheckoutStarter
	processor WebhookProcessor
}

func NewHTTPHandler(starter CheckoutStarter, processor WebhookProcessor) *HTTPHandler {
	return &HTTPHandler{starter: starter, processor: processor}
}

type CheckoutRequest struct {
	BookingID uuid.UUID `json:"booking_id"`
}

func (handler *HTTPHandler) StartCheckout(w http.ResponseWriter, r *http.Request) {
	guestID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var request CheckoutRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.BookingID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "booking_id is required")
		return
	}

	result, err := handler.starter.StartCheckout(r.Context(), guestID, request.BookingID)
	switch {
	case errors.Is(err, ErrBookingNotFound):
		writeError(w, http.StatusNotFound, "booking not found")
		return
	case errors.Is(err, ErrBookingNotPayable), errors.Is(err, ErrAlreadyPaid):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrListingUnpriced):
		writeError(w, http.StatusUnprocessableEntity, "listing cannot be booked yet")
		return
	case err != nil:
		slog.Error("Start checkout", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start checkout")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(result)
}

func (handler *HTTPHandler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err = handler.processor.HandleWebhook(r.Context(), r.Header.Get(signatureHeaderName), body)
	switch {
	case errors.Is(err, ErrInvalidSignature):
		slog.Warn("Stripe webhook rejected", "remote_addr", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	case errors.Is(err, ErrInvalidPayload):
		writeError(w, http.StatusBadRequest, "invalid webhook payload")
		return
	case err != nil:
		slog.Error("Stripe webhook", "error", err)
		writeError(w, http.StatusInternalServerError, "could not process webhook")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
