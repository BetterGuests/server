package bookings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

// dateLayout is the wire format for starts_on/ends_on: a bare calendar date
// with no time-of-day or timezone component, matching app.bookings' "date"
// columns.
const dateLayout = "2006-01-02"

// BookingCreator is the application capability the create-booking HTTP
// route needs.
type BookingCreator interface {
	CreateBooking(ctx context.Context, guestID uuid.UUID, input CreateBookingInput) (Booking, error)
}

// HTTPHandler owns the HTTP-facing booking routes.
type HTTPHandler struct{ creator BookingCreator }

// NewHTTPHandler returns a handler backed by creator.
func NewHTTPHandler(creator BookingCreator) *HTTPHandler {
	return &HTTPHandler{creator: creator}
}

// CreateBookingRequest is the JSON body accepted by POST /api/v1/bookings.
// Guest identity is deliberately absent because it comes from the validated
// bearer token.
type CreateBookingRequest struct {
	ListingID uuid.UUID `json:"listing_id"`
	StartsOn  string    `json:"starts_on"`
	EndsOn    string    `json:"ends_on"`
}

// CreateBooking handles POST /api/v1/bookings.
func (handler *HTTPHandler) CreateBooking(w http.ResponseWriter, r *http.Request) {
	guestID, ok := accounts.UserID(r.Context())
	if !ok {
		writeBookingError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var request CreateBookingRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeBookingError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeBookingError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}

	startsOn, err := time.Parse(dateLayout, request.StartsOn)
	if err != nil {
		writeBookingError(w, http.StatusBadRequest, "starts_on must be a YYYY-MM-DD date")
		return
	}
	endsOn, err := time.Parse(dateLayout, request.EndsOn)
	if err != nil {
		writeBookingError(w, http.StatusBadRequest, "ends_on must be a YYYY-MM-DD date")
		return
	}

	booking, err := handler.creator.CreateBooking(r.Context(), guestID, CreateBookingInput{
		ListingID: request.ListingID,
		StartsOn:  startsOn,
		EndsOn:    endsOn,
	})
	switch {
	case errors.Is(err, ErrInvalidBooking):
		writeBookingError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, ErrBookingOverlaps):
		writeBookingError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		slog.Error("Create booking", "error", err)
		writeBookingError(w, http.StatusInternalServerError, "could not create booking")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(booking)
}

func writeBookingError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
