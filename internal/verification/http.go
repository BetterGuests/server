package verification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
)

const (
	maxWebhookBodyBytes = 1 << 20
	lookupTimeout       = 5 * time.Second
	lookupWindow        = 48 * time.Hour
	signatureHeaderName = "Persona-Signature"
	replaceQueryParam   = "replace"
	inquiryQueryParam   = "inquiry_id"
	handoffQueryParam   = "handoff"
)

type HTTPHandler struct {
	starter   Starter
	processor WebhookProcessor
	status    StatusReader
	deletion  IDDeleter
	reviews   IdentityReviewer
	lookups   Reconciler
}

type Reconciler interface {
	Reconcile(ctx context.Context, accountID uuid.UUID, inquiryID string) error
}

func (handler *HTTPHandler) WithReconciler(reconciler Reconciler) *HTTPHandler {
	handler.lookups = reconciler
	return handler
}

type IDDeleter interface {
	DeleteIDData(ctx context.Context, accountID uuid.UUID) (DeletionResult, error)
}

func (handler *HTTPHandler) WithDeletion(deleter IDDeleter) *HTTPHandler {
	handler.deletion = deleter
	return handler
}

func (handler *HTTPHandler) DeleteIDData(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if handler.deletion == nil {
		writeError(w, http.StatusNotFound, "ID data deletion is not available")
		return
	}
	result, err := handler.deletion.DeleteIDData(r.Context(), accountID)
	if err != nil {
		slog.Error("Delete ID data", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete ID data")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
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
	attempts, err := handler.status.Attempts(r.Context(), accountID)
	if err != nil {
		slog.Error("Verification status", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read verification status")
		return
	}
	inquiryID := r.URL.Query().Get(inquiryQueryParam)
	if handler.reconcile(r.Context(), accountID, attempts, inquiryID) {
		if refreshed, err := handler.status.Attempts(r.Context(), accountID); err == nil {
			attempts = refreshed
		} else {
			slog.Warn("Reread verification status", "error", err)
		}
	}
	response, found := summarize(attempts, inquiryID, time.Now())
	if !found {
		writeError(w, http.StatusNotFound, "verification inquiry not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (handler *HTTPHandler) reconcile(ctx context.Context, accountID uuid.UUID, attempts []Attempt, inquiryID string) bool {
	if handler.lookups == nil || len(attempts) == 0 {
		return false
	}
	target := attempts[0]
	if inquiryID != "" {
		index := slices.IndexFunc(attempts, func(attempt Attempt) bool { return attempt.InquiryID == inquiryID })
		if index < 0 {
			return false
		}
		target = attempts[index]
	}
	if target.Status != StatusCreated || time.Since(target.CompletedAt) > lookupWindow {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	if err := handler.lookups.Reconcile(lookupCtx, accountID, target.InquiryID); err != nil {
		slog.Warn("Look up ID check with Persona", "inquiry_id", target.InquiryID, "error", err)
		return false
	}
	return true
}

func (handler *HTTPHandler) StartVerification(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	replace := r.URL.Query().Get(replaceQueryParam) == "true"
	var result StartResult
	var err error
	if r.URL.Query().Get(handoffQueryParam) == "true" {
		result, err = handler.startHandoff(r, accountID, replace)
	} else {
		result, err = handler.starter.StartVerification(r.Context(), accountID, replace)
	}
	switch {
	case errors.Is(err, ErrHandoffUnavailable):
		writeError(w, http.StatusNotFound, "handoff is not available")
		return
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

func (handler *HTTPHandler) startHandoff(r *http.Request, accountID uuid.UUID, replace bool) (StartResult, error) {
	starter, ok := handler.starter.(HandoffStarter)
	if !ok {
		return StartResult{}, ErrHandoffUnavailable
	}
	return starter.StartHandoff(r.Context(), accountID, replace)
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
