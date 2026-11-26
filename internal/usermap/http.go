package usermap

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
)

type HTTPHandler struct {
	reader Reader
}

func NewHTTPHandler(reader Reader) *HTTPHandler {
	return &HTTPHandler{reader: reader}
}

func (handler *HTTPHandler) Summary(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	summary, err := handler.reader.Summary(r.Context(), adminID)
	if errors.Is(err, ErrNotAdmin) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		slog.Error("read user map", "error", err)
		writeError(w, http.StatusBadGateway, "could not read members right now")
		return
	}
	writeJSON(w, http.StatusOK, summary)
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
