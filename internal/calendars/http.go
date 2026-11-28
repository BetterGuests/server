package calendars

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

const (
	maxRequestBytes = 8 << 10
	calendarFile    = ".ics"
)

type HTTPHandler struct{ manager Manager }

func NewHTTPHandler(manager Manager) *HTTPHandler { return &HTTPHandler{manager: manager} }

func (handler *HTTPHandler) Export(w http.ResponseWriter, r *http.Request) {
	token, found := strings.CutSuffix(r.PathValue("file"), calendarFile)
	if !found || token == "" {
		writeError(w, ErrCalendarUnknown)
		return
	}
	calendar, err := handler.manager.Export(r.Context(), token)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="better-guests.ics"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(calendar)
}

func (handler *HTTPHandler) Overview(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	overview, err := handler.manager.Overview(r.Context(), hostID, listingID)
	respond(w, overview, err)
}

func (handler *HTTPHandler) AddFeed(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	var request struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		write(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return
	}
	overview, err := handler.manager.AddFeed(r.Context(), hostID, listingID, request.Name, request.URL)
	respond(w, overview, err)
}

func (handler *HTTPHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, feedID, ok := hostListingAndFeed(w, r)
	if !ok {
		return
	}
	overview, err := handler.manager.Refresh(r.Context(), hostID, listingID, feedID)
	respond(w, overview, err)
}

func (handler *HTTPHandler) RemoveFeed(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, feedID, ok := hostListingAndFeed(w, r)
	if !ok {
		return
	}
	overview, err := handler.manager.RemoveFeed(r.Context(), hostID, listingID, feedID)
	respond(w, overview, err)
}

func (handler *HTTPHandler) ResetLink(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	overview, err := handler.manager.ResetLink(r.Context(), hostID, listingID)
	respond(w, overview, err)
}

func hostAndListing(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		write(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return uuid.Nil, uuid.Nil, false
	}
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeError(w, ErrListingNotFound)
		return uuid.Nil, uuid.Nil, false
	}
	return hostID, listingID, true
}

func hostListingAndFeed(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, uuid.UUID, bool) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	feedID, err := uuid.Parse(r.PathValue("feed_id"))
	if err != nil {
		writeError(w, ErrFeedNotFound)
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	return hostID, listingID, feedID, true
}

func respond(w http.ResponseWriter, overview Overview, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	write(w, http.StatusOK, overview)
}

func writeError(w http.ResponseWriter, err error) {
	statuses := []struct {
		known  error
		status int
	}{
		{ErrListingNotFound, http.StatusNotFound},
		{ErrFeedNotFound, http.StatusNotFound},
		{ErrCalendarUnknown, http.StatusNotFound},
		{ErrInvalidName, http.StatusBadRequest},
		{ErrInvalidAddress, http.StatusBadRequest},
		{ErrOwnCalendar, http.StatusBadRequest},
		{ErrDuplicateFeed, http.StatusConflict},
		{ErrTooManyFeeds, http.StatusConflict},
		{ErrUnreachable, http.StatusUnprocessableEntity},
		{ErrNotACalendar, http.StatusUnprocessableEntity},
		{ErrTooLarge, http.StatusUnprocessableEntity},
	}
	for _, entry := range statuses {
		if errors.Is(err, entry.known) {
			write(w, entry.status, map[string]string{"error": entry.known.Error()})
			return
		}
	}
	slog.Error("calendar sync request", "error", err)
	write(w, http.StatusInternalServerError, map[string]string{"error": "could not load calendar sync"})
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
