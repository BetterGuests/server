package bans

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type HTTPHandler struct {
	desk Desk
}

func NewHTTPHandler(desk Desk) *HTTPHandler {
	return &HTTPHandler{desk: desk}
}

func (handler *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	adminID, ok := authenticated(w, r)
	if !ok {
		return
	}
	found, err := handler.desk.List(r.Context(), adminID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]Ban{"bans": found})
}

func (handler *HTTPHandler) Ban(w http.ResponseWriter, r *http.Request) {
	adminID, ok := authenticated(w, r)
	if !ok {
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	banID, err := handler.desk.Ban(r.Context(), adminID, request)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]uuid.UUID{"id": banID})
}

func (handler *HTTPHandler) Lift(w http.ResponseWriter, r *http.Request) {
	adminID, ok := authenticated(w, r)
	if !ok {
		return
	}
	banID, err := uuid.Parse(r.PathValue("ban_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrBanNotFound.Error())
		return
	}
	if err := handler.desk.Lift(r.Context(), adminID, banID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func authenticated(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
	return accountID, ok
}

func fail(w http.ResponseWriter, err error) {
	for _, known := range []struct {
		err    error
		status int
	}{
		{ErrInvalidBan, http.StatusBadRequest},
		{ErrCannotBanAdmin, http.StatusForbidden},
		{ErrAccountNotFound, http.StatusNotFound},
		{ErrBanNotFound, http.StatusNotFound},
		{ErrNotAdmin, http.StatusNotFound},
		{ErrAlreadyBanned, http.StatusConflict},
	} {
		if errors.Is(err, known.err) {
			writeError(w, known.status, known.err.Error())
			return
		}
	}
	slog.Error("Ban request", "error", err)
	writeError(w, http.StatusInternalServerError, "something went wrong")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
