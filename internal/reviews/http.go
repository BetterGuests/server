package reviews

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
	creator   Creator
	reader    Reader
	summaries SummaryReader
}

func NewHTTPHandler(creator Creator, reader Reader) *HTTPHandler {
	return &HTTPHandler{creator: creator, reader: reader}
}

func (handler *HTTPHandler) WithSummaries(summaries SummaryReader) *HTTPHandler {
	handler.summaries = summaries
	return handler
}

func (handler *HTTPHandler) CreateReview(w http.ResponseWriter, r *http.Request) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return
	}
	var input CreateReviewInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCreateBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	review, err := handler.creator.CreateReview(r.Context(), actorID, input)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, review)
}

func (handler *HTTPHandler) ListingReviews(w http.ResponseWriter, r *http.Request) {
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "listing not found")
		return
	}
	result, err := handler.reader.ForListing(r.Context(), listingID)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (handler *HTTPHandler) ListingReviewSummary(w http.ResponseWriter, r *http.Request) {
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "listing not found")
		return
	}
	if handler.summaries == nil {
		writeJSON(w, http.StatusOK, ReviewSummary{})
		return
	}
	result, err := handler.summaries.ListingSummary(r.Context(), listingID)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (handler *HTTPHandler) BookingGuestReviewSummary(w http.ResponseWriter, r *http.Request) {
	hostID, bookingID, ok := authenticatedBooking(w, r)
	if !ok {
		return
	}
	if handler.summaries == nil {
		writeJSON(w, http.StatusOK, ReviewSummary{})
		return
	}
	result, err := handler.summaries.BookingGuestSummary(r.Context(), hostID, bookingID)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (handler *HTTPHandler) BookingReviews(w http.ResponseWriter, r *http.Request) {
	actorID, bookingID, ok := authenticatedBooking(w, r)
	if !ok {
		return
	}
	result, err := handler.reader.ForBooking(r.Context(), actorID, bookingID)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (handler *HTTPHandler) BookingGuestReviews(w http.ResponseWriter, r *http.Request) {
	hostID, bookingID, ok := authenticatedBooking(w, r)
	if !ok {
		return
	}
	result, err := handler.reader.AboutBookingGuest(r.Context(), hostID, bookingID)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (handler *HTTPHandler) ReviewsAboutMe(w http.ResponseWriter, r *http.Request) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return
	}
	result, err := handler.reader.AboutMe(r.Context(), actorID)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (handler *HTTPHandler) PendingReviews(w http.ResponseWriter, r *http.Request) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return
	}
	pending, err := handler.reader.Pending(r.Context(), actorID)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]PendingReview{"pending": pending})
}

func authenticated(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	actorID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
	return actorID, ok
}

func authenticatedBooking(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	bookingID, err := uuid.Parse(r.PathValue("booking_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrBookingNotFound.Error())
		return uuid.Nil, uuid.Nil, false
	}
	return actorID, bookingID, true
}

func writeReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidReview):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrBookingNotReviewable):
		writeError(w, http.StatusForbidden, ErrBookingNotReviewable.Error())
	case errors.Is(err, ErrBookingNotFound):
		writeError(w, http.StatusNotFound, ErrBookingNotFound.Error())
	case errors.Is(err, ErrReviewAlreadyExists), errors.Is(err, ErrReviewWindowClosed):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("Review request", "error", err)
		writeError(w, http.StatusInternalServerError, "something went wrong with reviews")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
