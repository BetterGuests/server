package availability

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

type HTTPHandler struct{ reader Reader }

func NewHTTPHandler(reader Reader) *HTTPHandler { return &HTTPHandler{reader: reader} }

func (handler *HTTPHandler) ListBookedDates(w http.ResponseWriter, r *http.Request) {
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid listing id"})
		return
	}
	ranges, err := handler.reader.BookedRanges(r.Context(), listingID)
	switch {
	case errors.Is(err, ErrListingNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "listing not found"})
	case err != nil:
		slog.Error("List booked dates failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load booked dates"})
	default:
		writeJSON(w, http.StatusOK, map[string][]BookedRange{"booked": ranges})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
