package waitlist

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

type HTTPHandler struct{ joiner Joiner }

func NewHTTPHandler(joiner Joiner) *HTTPHandler {
	return &HTTPHandler{joiner: joiner}
}

type JoinRequest struct {
	Email   string `json:"email"`
	Country string `json:"country"`
}

func (handler *HTTPHandler) Join(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	var request JoinRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	err := handler.joiner.Join(r.Context(), request.Email, request.Country)
	switch {
	case errors.Is(err, ErrInvalidEmail), errors.Is(err, ErrInvalidCountry):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.Error("Waitlist signup", "error", err)
		writeError(w, http.StatusInternalServerError, "could not complete request")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
