package notifications

import (
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type HTTPHandler struct {
	inbox        InboxReader
	unsubscriber Unsubscriber
}

func NewHTTPHandler(inbox InboxReader) *HTTPHandler { return &HTTPHandler{inbox: inbox} }

func (handler *HTTPHandler) WithUnsubscriber(unsubscriber Unsubscriber) *HTTPHandler {
	handler.unsubscriber = unsubscriber
	return handler
}

func (handler *HTTPHandler) UnsubscribePage(w http.ResponseWriter, r *http.Request) {
	token, category, ok := unsubscribeRequest(r)
	if !ok {
		writeUnsubscribePage(w, http.StatusNotFound, deadLinkPage)
		return
	}
	action := unsubscribePath + "?" + url.Values{"token": {token.String()}, "category": {category}}.Encode()
	writeUnsubscribePage(w, http.StatusOK,
		"<h1>Stop these emails?</h1><p>You will still get emails about your bookings.</p>"+
			`<form method="post" action="`+html.EscapeString(action)+`"><button>Unsubscribe</button></form>`)
}

func (handler *HTTPHandler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	token, category, ok := unsubscribeRequest(r)
	if !ok || handler.unsubscriber == nil {
		writeUnsubscribePage(w, http.StatusNotFound, deadLinkPage)
		return
	}
	found, err := handler.unsubscriber.Unsubscribe(r.Context(), token, category)
	if err != nil {
		slog.Error("Unsubscribe email", "error", err)
		writeUnsubscribePage(w, http.StatusInternalServerError, "<h1>That did not work.</h1><p>Please try again.</p>")
		return
	}
	if !found {
		writeUnsubscribePage(w, http.StatusNotFound, deadLinkPage)
		return
	}
	writeUnsubscribePage(w, http.StatusOK, "<h1>You are unsubscribed.</h1><p>You will no longer get these emails.</p>")
}

func unsubscribeRequest(r *http.Request) (uuid.UUID, string, bool) {
	token, err := uuid.Parse(r.URL.Query().Get("token"))
	category := r.URL.Query().Get("category")
	known := category == categoryReminders || category == categoryMessages
	return token, category, err == nil && token != uuid.Nil && known
}

func writeUnsubscribePage(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width, initial-scale=1"><title>Better Guests email</title><body>`+body)
}

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
