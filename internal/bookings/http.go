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

const VerificationRequiredCode = "verification_required"

const TripOverlapCode = "trip_overlap"

const dateLayout = "2006-01-02"

type BookingCreator interface {
	CreateBooking(ctx context.Context, guestID uuid.UUID, input CreateBookingInput) (Booking, error)
}

type BookingReader interface {
	Get(ctx context.Context, actorID, bookingID uuid.UUID) (Detail, error)
	ListForGuest(ctx context.Context, guestID uuid.UUID) ([]Detail, error)
	ListForHost(ctx context.Context, hostID uuid.UUID) ([]Detail, error)
	ListRequestsForHost(ctx context.Context, hostID uuid.UUID) ([]Detail, error)
}

type BookingResponder interface {
	Approve(ctx context.Context, hostID, bookingID uuid.UUID) (Detail, error)
	Decline(ctx context.Context, hostID, bookingID uuid.UUID) (Detail, error)
	Cancel(ctx context.Context, guestID, bookingID uuid.UUID) (Detail, error)
}

type HTTPHandler struct {
	creator   BookingCreator
	reader    BookingReader
	responder BookingResponder
}

func NewHTTPHandler(creator BookingCreator) *HTTPHandler {
	return &HTTPHandler{creator: creator}
}

func (handler *HTTPHandler) WithReader(reader BookingReader) *HTTPHandler {
	handler.reader = reader
	return handler
}

func (handler *HTTPHandler) WithResponder(responder BookingResponder) *HTTPHandler {
	handler.responder = responder
	return handler
}

type CreateBookingRequest struct {
	ListingID uuid.UUID `json:"listing_id"`
	StartsOn  string    `json:"starts_on"`
	EndsOn    string    `json:"ends_on"`
}

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
	case errors.Is(err, ErrVerificationRequired):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "code": VerificationRequiredCode})
		return
	case errors.Is(err, ErrTripOverlaps):
		writeBookingJSON(w, http.StatusConflict, map[string]string{"error": ErrTripOverlaps.Error(), "code": TripOverlapCode})
		return
	case errors.Is(err, ErrReleaseFailed):
		writeBookingError(w, http.StatusBadGateway, ErrReleaseFailed.Error())
		return
	case errors.Is(err, ErrBookingOverlaps), errors.Is(err, ErrListingPaused):
		writeBookingError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		slog.Error("Create booking", "error", err)
		writeBookingError(w, http.StatusInternalServerError, "could not create booking")
		return
	}

	writeBookingJSON(w, http.StatusCreated, booking)
}

func (handler *HTTPHandler) GetBooking(w http.ResponseWriter, r *http.Request) {
	actorID, bookingID, ok := actorAndBookingID(w, r)
	if !ok {
		return
	}
	detail, err := handler.reader.Get(r.Context(), actorID, bookingID)
	if err != nil {
		writeServiceError(w, "Get booking", err)
		return
	}
	writeBookingJSON(w, http.StatusOK, detail)
}

func (handler *HTTPHandler) ListMyBookings(w http.ResponseWriter, r *http.Request) {
	guestID, ok := accounts.UserID(r.Context())
	if !ok {
		writeBookingError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	details, err := handler.reader.ListForGuest(r.Context(), guestID)
	if err != nil {
		writeServiceError(w, "List bookings", err)
		return
	}
	writeBookingJSON(w, http.StatusOK, map[string][]Detail{"bookings": nonNil(details)})
}

func (handler *HTTPHandler) ListHostBookings(w http.ResponseWriter, r *http.Request) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeBookingError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	details, err := handler.reader.ListForHost(r.Context(), hostID)
	if err != nil {
		writeServiceError(w, "List host bookings", err)
		return
	}
	writeBookingJSON(w, http.StatusOK, map[string][]Detail{"bookings": nonNil(details)})
}

func (handler *HTTPHandler) ListHostRequests(w http.ResponseWriter, r *http.Request) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeBookingError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	details, err := handler.reader.ListRequestsForHost(r.Context(), hostID)
	if err != nil {
		writeServiceError(w, "List booking requests", err)
		return
	}
	writeBookingJSON(w, http.StatusOK, map[string][]Detail{"bookings": nonNil(details)})
}

func (handler *HTTPHandler) ApproveBooking(w http.ResponseWriter, r *http.Request) {
	handler.respond(w, r, handler.responder.Approve)
}

func (handler *HTTPHandler) DeclineBooking(w http.ResponseWriter, r *http.Request) {
	handler.respond(w, r, handler.responder.Decline)
}

func (handler *HTTPHandler) CancelBooking(w http.ResponseWriter, r *http.Request) {
	handler.respond(w, r, handler.responder.Cancel)
}

func (handler *HTTPHandler) respond(
	w http.ResponseWriter,
	r *http.Request,
	action func(ctx context.Context, actorID, bookingID uuid.UUID) (Detail, error),
) {
	actorID, bookingID, ok := actorAndBookingID(w, r)
	if !ok {
		return
	}
	detail, err := action(r.Context(), actorID, bookingID)
	if err != nil {
		writeServiceError(w, "Respond to booking", err)
		return
	}
	writeBookingJSON(w, http.StatusOK, detail)
}

func actorAndBookingID(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	actorID, ok := accounts.UserID(r.Context())
	if !ok {
		writeBookingError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, uuid.Nil, false
	}
	bookingID, err := uuid.Parse(r.PathValue("booking_id"))
	if err != nil {
		writeBookingError(w, http.StatusBadRequest, "booking_id must be a UUID")
		return uuid.Nil, uuid.Nil, false
	}
	return actorID, bookingID, true
}

func writeServiceError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, ErrBookingNotFound):
		writeBookingError(w, http.StatusNotFound, "booking not found")
	case errors.Is(err, ErrHostIDRequired):
		writeBookingJSON(w, http.StatusForbidden, map[string]string{"error": err.Error(), "code": VerificationRequiredCode})
	case errors.Is(err, ErrNotAwaitingApproval), errors.Is(err, ErrNotCancellable), errors.Is(err, ErrPaymentNotAuthorized), errors.Is(err, ErrStayAlreadyStarted), errors.Is(err, ErrGuestBanned):
		writeBookingError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrCaptureFailed), errors.Is(err, ErrReleaseFailed):
		writeBookingError(w, http.StatusBadGateway, err.Error())
	default:
		slog.Error(operation, "error", err)
		writeBookingError(w, http.StatusInternalServerError, "could not complete the request")
	}
}

func nonNil(details []Detail) []Detail {
	if details == nil {
		return []Detail{}
	}
	return details
}

func writeBookingJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeBookingError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
