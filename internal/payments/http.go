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
	cards     CardManager
}

func NewHTTPHandler(starter CheckoutStarter, processor WebhookProcessor) *HTTPHandler {
	return &HTTPHandler{starter: starter, processor: processor}
}

func (handler *HTTPHandler) WithCards(cards CardManager) *HTTPHandler {
	handler.cards = cards
	return handler
}

type SavedCardsResponse struct {
	Cards        []SavedCard `json:"cards"`
	ArtAvailable bool        `json:"art_available"`
}

func (handler *HTTPHandler) ListSavedCards(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if handler.cards == nil {
		writeError(w, http.StatusServiceUnavailable, "saved cards are unavailable")
		return
	}
	cards, err := handler.cards.SavedCards(r.Context(), accountID)
	if err != nil {
		slog.Error("List saved cards", "error", err)
		writeError(w, http.StatusInternalServerError, "could not load your cards")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(SavedCardsResponse{Cards: cards, ArtAvailable: handler.cards.CardArtEnabled()})
}

func (handler *HTTPHandler) RemoveSavedCard(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if handler.cards == nil {
		writeError(w, http.StatusServiceUnavailable, "saved cards are unavailable")
		return
	}
	err := handler.cards.RemoveCard(r.Context(), accountID, r.PathValue("card_id"))
	switch {
	case errors.Is(err, ErrCardNotFound):
		writeError(w, http.StatusNotFound, "card not found")
		return
	case err != nil:
		slog.Error("Remove saved card", "error", err)
		writeError(w, http.StatusInternalServerError, "could not remove your card")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) StyleSavedCard(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if handler.cards == nil {
		writeError(w, http.StatusServiceUnavailable, "saved cards are unavailable")
		return
	}
	var style CardStyle
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&style); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err := handler.cards.StyleCard(r.Context(), accountID, r.PathValue("card_id"), style)
	switch {
	case errors.Is(err, ErrCardNotFound):
		writeError(w, http.StatusNotFound, "card not found")
		return
	case errors.Is(err, ErrInvalidCardStyle):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		slog.Error("Style saved card", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the card style")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type LinkCardArtRequest struct {
	VaultCardID string `json:"vgs_card_id"`
}

func (handler *HTTPHandler) StartCardArt(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if handler.cards == nil {
		writeError(w, http.StatusServiceUnavailable, "card art is not set up")
		return
	}
	session, err := handler.cards.StartCardArt(r.Context(), accountID, r.PathValue("card_id"))
	if err != nil {
		writeCardArtError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(session)
}

func (handler *HTTPHandler) LinkCardArt(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if handler.cards == nil {
		writeError(w, http.StatusServiceUnavailable, "card art is not set up")
		return
	}
	var request LinkCardArtRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.VaultCardID == "" {
		writeError(w, http.StatusBadRequest, "vgs_card_id is required")
		return
	}
	err := handler.cards.LinkCardArt(r.Context(), accountID, r.PathValue("card_id"), request.VaultCardID)
	if err != nil {
		writeCardArtError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeCardArtError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCardNotFound):
		writeError(w, http.StatusNotFound, "card not found")
	case errors.Is(err, ErrArtCardMismatch):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, ErrCardArtUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		slog.Error("Card art", "error", err)
		writeError(w, http.StatusBadGateway, "could not add the card art")
	}
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
