package verification

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
)

const (
	maxWebhookBodyBytes = 1 << 20
	signatureHeaderName = "Persona-Signature"
	replaceQueryParam   = "replace"
)

type HTTPHandler struct {
	starter   Starter
	processor WebhookProcessor
	status    StatusReader
}

func NewHTTPHandler(starter Starter, processor WebhookProcessor) *HTTPHandler {
	return &HTTPHandler{starter: starter, processor: processor}
}

func (handler *HTTPHandler) WithStatusReader(reader StatusReader) *HTTPHandler {
	handler.status = reader
	return handler
}

func (handler *HTTPHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if handler.status == nil {
		writeError(w, http.StatusNotFound, "status is not available")
		return
	}
	status, found, err := handler.status.LatestStatus(r.Context(), accountID)
	if err != nil {
		slog.Error("Verification status", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read verification status")
		return
	}
	response := StatusResponse{Status: "none"}
	if found {
		response = StatusResponse{Status: string(status), Verified: status == StatusCompleted}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (handler *HTTPHandler) StartVerification(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	result, err := handler.starter.StartVerification(r.Context(), accountID, r.URL.Query().Get(replaceQueryParam) == "true")
	switch {
	case errors.Is(err, ErrAlreadyVerified):
		writeError(w, http.StatusConflict, "account is already verified")
		return
	case errors.Is(err, ErrConsentRequired):
		writeError(w, http.StatusForbidden, "consent is required before verification")
		return
	case err != nil:
		slog.Error("Start verification", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start verification")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(result)
}

func (handler *HTTPHandler) PersonaWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err = handler.processor.HandleWebhook(r.Context(), r.Header.Get(signatureHeaderName), body)
	switch {
	case errors.Is(err, ErrInvalidSignature):
		slog.Warn("Persona webhook rejected", "remote_addr", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	case errors.Is(err, ErrInvalidPayload):
		writeError(w, http.StatusBadRequest, "invalid webhook payload")
		return
	case errors.Is(err, ErrInquiryNotFound):
		writeError(w, http.StatusNotFound, "inquiry not found")
		return
	case err != nil:
		slog.Error("Persona webhook", "error", err)
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
