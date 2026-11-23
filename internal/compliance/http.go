package compliance

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
)

const maxBodyBytes = 1 << 10

type HTTPHandler struct {
	ages     AgeConfirmer
	consents ConsentRecorder
	status   StatusReader
}

func NewHTTPHandler(ages AgeConfirmer, consents ConsentRecorder, status StatusReader) *HTTPHandler {
	return &HTTPHandler{ages: ages, consents: consents, status: status}
}

type ageRequest struct {
	DateOfBirth string `json:"date_of_birth"`
}

type consentRequest struct {
	Kind          string `json:"kind"`
	PolicyVersion string `json:"policy_version"`
}

func (handler *HTTPHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}
	status, err := handler.status.Status(r.Context(), accountID)
	if err != nil {
		slog.Error("Compliance status", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read status", "")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (handler *HTTPHandler) ConfirmAge(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}
	var request ageRequest
	if !decodeBody(w, r, &request) {
		return
	}
	err := handler.ages.ConfirmAge(r.Context(), accountID, request.DateOfBirth)
	switch {
	case errors.Is(err, ErrInvalidDateOfBirth):
		writeError(w, http.StatusBadRequest, "enter a real date of birth", "")
		return
	case errors.Is(err, ErrUnderage):
		writeError(w, http.StatusForbidden, "you must be 18 or older", CodeUnderage)
		return
	case err != nil:
		slog.Error("Confirm age", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save age confirmation", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) GrantConsent(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required", "")
		return
	}
	var request consentRequest
	if !decodeBody(w, r, &request) {
		return
	}
	err := handler.consents.GrantConsent(r.Context(), accountID, request.Kind, request.PolicyVersion)
	switch {
	case errors.Is(err, ErrInvalidConsent):
		writeError(w, http.StatusBadRequest, "unknown consent", "")
		return
	case err != nil:
		slog.Error("Grant consent", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save consent", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) RequireAdult(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := accounts.UserID(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required", "")
			return
		}
		adult, err := handler.ages.IsAdult(r.Context(), accountID)
		if err != nil {
			slog.Error("Require adult", "error", err)
			writeError(w, http.StatusInternalServerError, "could not check age confirmation", "")
			return
		}
		if !adult {
			writeError(w, http.StatusForbidden, "confirm your age to continue", CodeAgeConfirmationRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "")
		return false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body", "")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message, code string) {
	body := map[string]string{"error": message}
	if code != "" {
		body["code"] = code
	}
	writeJSON(w, status, body)
}
