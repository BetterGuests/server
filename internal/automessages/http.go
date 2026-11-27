package automessages

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

const maxBodyBytes = 16 << 10

type HTTPHandler struct{ messages Manager }

func NewHTTPHandler(messages Manager) *HTTPHandler {
	return &HTTPHandler{messages: messages}
}

func (handler *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := identify(w, r)
	if !ok {
		return
	}
	overview, err := handler.messages.Overview(r.Context(), hostID, listingID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (handler *HTTPHandler) Create(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := identify(w, r)
	if !ok {
		return
	}
	input, ok := decode(w, r)
	if !ok {
		return
	}
	message, err := handler.messages.Create(r.Context(), hostID, listingID, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, message)
}

func (handler *HTTPHandler) Update(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, messageID, ok := identifyMessage(w, r)
	if !ok {
		return
	}
	input, ok := decode(w, r)
	if !ok {
		return
	}
	message, err := handler.messages.Update(r.Context(), hostID, listingID, messageID, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, message)
}

func (handler *HTTPHandler) Delete(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, messageID, ok := identifyMessage(w, r)
	if !ok {
		return
	}
	if err := handler.messages.Delete(r.Context(), hostID, listingID, messageID); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func identify(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, uuid.Nil, false
	}
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrListingNotFound.Error())
		return uuid.Nil, uuid.Nil, false
	}
	return hostID, listingID, true
}

func identifyMessage(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, uuid.UUID, bool) {
	hostID, listingID, ok := identify(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	messageID, err := uuid.Parse(r.PathValue("auto_message_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrMessageNotFound.Error())
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	return hostID, listingID, messageID, true
}

func decode(w http.ResponseWriter, r *http.Request) (Input, bool) {
	var input Input
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return Input{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return Input{}, false
	}
	return input, true
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrListingNotFound), errors.Is(err, ErrMessageNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidMessage):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrTooMany):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("Automatic message request", "error", err)
		writeError(w, http.StatusInternalServerError, "something went wrong")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
