package notifications

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type HTTPHandler struct{ inbox InboxReader }

func NewHTTPHandler(inbox InboxReader) *HTTPHandler { return &HTTPHandler{inbox: inbox} }

func (handler *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return
	}
	inbox, err := handler.inbox.List(r.Context(), actorID)
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(inbox)
}

func (handler *HTTPHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return
	}
	notificationID, err := uuid.Parse(r.PathValue("notification_id"))
	if err != nil {
		writeNotificationError(w, ErrNotificationNotFound)
		return
	}
	if err := handler.inbox.MarkRead(r.Context(), actorID, notificationID); err != nil {
		writeNotificationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (handler *HTTPHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return
	}
	if err := handler.inbox.MarkAllRead(r.Context(), actorID); err != nil {
		writeNotificationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func authenticated(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	actorID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
	return actorID, ok
}

func writeNotificationError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotificationNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	slog.Error("Notification request", "error", err)
	writeError(w, http.StatusInternalServerError, "something went wrong with notifications")
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
