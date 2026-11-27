package pms

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

const maxRequestBytes = 32 << 10

type HTTPHandler struct {
	importer Importer
}

func NewHTTPHandler(importer Importer) *HTTPHandler {
	return &HTTPHandler{importer: importer}
}

type connectionRequest struct {
	Provider ProviderName `json:"provider"`
	Account  string       `json:"account"`
	Secret   string       `json:"secret"`
}

type importRequest struct {
	connectionRequest
	PropertyIDs []string          `json:"property_ids"`
	Answers     map[string]Answer `json:"answers"`
}

func (handler *HTTPHandler) Preview(w http.ResponseWriter, r *http.Request) {
	var request connectionRequest
	hostID, ok := accept(w, r, &request)
	if !ok {
		return
	}
	candidates, err := handler.importer.Preview(r.Context(), hostID, request.Provider, Credentials{Account: request.Account, Secret: request.Secret})
	respond(w, map[string]any{"properties": candidates}, err)
}

func (handler *HTTPHandler) Import(w http.ResponseWriter, r *http.Request) {
	var request importRequest
	hostID, ok := accept(w, r, &request)
	if !ok {
		return
	}
	outcomes, err := handler.importer.Import(r.Context(), hostID, request.Provider, Credentials{Account: request.Account, Secret: request.Secret}, Selection{PropertyIDs: request.PropertyIDs, Answers: request.Answers})
	respond(w, map[string]any{"results": outcomes}, err)
}

func accept(w http.ResponseWriter, r *http.Request, into any) (uuid.UUID, bool) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		write(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return uuid.Nil, false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		write(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return uuid.Nil, false
	}
	return hostID, true
}

func respond(w http.ResponseWriter, body any, err error) {
	if err == nil {
		write(w, http.StatusOK, body)
		return
	}
	status, known := statusFor(err)
	if status >= http.StatusInternalServerError {
		slog.Warn("property system import", "error", err)
	}
	message := "could not import your places"
	if known != nil {
		message = known.Error()
	}
	write(w, status, map[string]string{"error": message})
}

func statusFor(err error) (int, error) {
	statuses := []struct {
		known  error
		status int
	}{
		{ErrUnknownProvider, http.StatusBadRequest},
		{ErrCredentialsRequired, http.StatusBadRequest},
		{ErrNothingSelected, http.StatusBadRequest},
		{ErrTooManySelected, http.StatusBadRequest},
		{ErrCredentialsRefused, http.StatusUnprocessableEntity},
		{ErrProviderBusy, http.StatusServiceUnavailable},
		{ErrProviderFailed, http.StatusBadGateway},
	}
	for _, entry := range statuses {
		if errors.Is(err, entry.known) {
			return entry.status, entry.known
		}
	}
	return http.StatusInternalServerError, nil
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
