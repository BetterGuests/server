package nearby

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

type HTTPHandler struct {
	searcher PlaceSearcher
}

func NewHTTPHandler(searcher PlaceSearcher) *HTTPHandler {
	return &HTTPHandler{searcher: searcher}
}

type searchResponse struct {
	Category string  `json:"category"`
	Places   []Place `json:"places"`
}

func (handler *HTTPHandler) Search(w http.ResponseWriter, r *http.Request) {
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid listing id")
		return
	}
	categoryID := r.URL.Query().Get("category")

	places, err := handler.searcher.Search(r.Context(), listingID, categoryID)
	switch {
	case errors.Is(err, ErrUnknownCategory):
		writeError(w, http.StatusBadRequest, "unknown category")
		return
	case errors.Is(err, ErrListingNotFound):
		writeError(w, http.StatusNotFound, "listing not found")
		return
	case errors.Is(err, ErrSourceUnavailable):
		slog.Warn("nearby places source unavailable", "error", err)
		writeError(w, http.StatusBadGateway, "nearby places are unavailable right now")
		return
	case err != nil:
		slog.Error("nearby search failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not search nearby places")
		return
	}

	if places == nil {
		places = []Place{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(searchResponse{Category: categoryID, Places: places})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
