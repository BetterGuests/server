package teamevents

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
)

type HTTPHandler struct {
	reader Reader
}

func NewHTTPHandler(reader Reader) *HTTPHandler {
	return &HTTPHandler{reader: reader}
}

func (handler *HTTPHandler) Feed(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC 3339 time")
			return
		}
		since = parsed
	}
	var kinds []Kind
	if raw := r.URL.Query().Get("kind"); raw != "" {
		for _, kind := range strings.Split(raw, ",") {
			kinds = append(kinds, Kind(strings.TrimSpace(kind)))
		}
	}
	events, err := handler.reader.Feed(r.Context(), adminID, since, kinds)
	switch {
	case errors.Is(err, ErrNotAdmin):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrInvalidKind):
		writeError(w, http.StatusBadRequest, "unknown event kind")
	case err != nil:
		slog.Error("read team events", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read events right now")
	default:
		writeJSON(w, http.StatusOK, map[string][]Event{"events": events})
	}
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
