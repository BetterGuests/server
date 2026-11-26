package cancellations

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
	canceller Canceller
	desk      ReportDesk
	listings  ListingDesk
}

func NewHTTPHandler(canceller Canceller, desk ReportDesk) *HTTPHandler {
	return &HTTPHandler{canceller: canceller, desk: desk}
}

func (handler *HTTPHandler) WithListingDesk(listings ListingDesk) *HTTPHandler {
	handler.listings = listings
	return handler
}

func (handler *HTTPHandler) AdminListings(w http.ResponseWriter, r *http.Request) {
	adminID, ok := authenticated(w, r)
	if !ok {
		return
	}
	listings, err := handler.listings.AdminListings(r.Context(), adminID)
	respond(w, map[string][]AdminListing{"listings": listings}, err)
}

func (handler *HTTPHandler) PopularListings(w http.ResponseWriter, r *http.Request) {
	adminID, ok := authenticated(w, r)
	if !ok {
		return
	}
	listings, err := handler.listings.PopularListings(r.Context(), adminID, r.URL.Query()["city"])
	respond(w, map[string][]PopularListing{"listings": listings}, err)
}

func (handler *HTTPHandler) PauseListing(w http.ResponseWriter, r *http.Request) {
	adminID, listingID, ok := identifyListing(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	err := handler.listings.PauseListing(r.Context(), adminID, listingID, body.Reason)
	respond(w, ListingEvent{ListingID: listingID, Paused: true}, err)
}

func (handler *HTTPHandler) UnpauseListing(w http.ResponseWriter, r *http.Request) {
	adminID, listingID, ok := identifyListing(w, r)
	if !ok {
		return
	}
	err := handler.listings.UnpauseListing(r.Context(), adminID, listingID)
	respond(w, ListingEvent{ListingID: listingID, Paused: false}, err)
}

func identifyListing(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": ErrListingNotFound.Error()})
		return uuid.Nil, uuid.Nil, false
	}
	return actorID, listingID, true
}

func (handler *HTTPHandler) Options(w http.ResponseWriter, r *http.Request) {
	actorID, bookingID, ok := identify(w, r)
	if !ok {
		return
	}
	options, err := handler.canceller.Options(r.Context(), actorID, bookingID)
	respond(w, options, err)
}

func (handler *HTTPHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	actorID, bookingID, ok := identify(w, r)
	if !ok {
		return
	}
	var body struct {
		RefundCents int `json:"refund_cents"`
	}
	if !decode(w, r, &body) {
		return
	}
	options, err := handler.canceller.Cancel(r.Context(), actorID, bookingID, body.RefundCents)
	respond(w, options, err)
}

func (handler *HTTPHandler) Report(w http.ResponseWriter, r *http.Request) {
	guestID, bookingID, ok := identify(w, r)
	if !ok {
		return
	}
	var body struct {
		Details string `json:"details"`
	}
	if !decode(w, r, &body) {
		return
	}
	options, err := handler.canceller.Report(r.Context(), guestID, bookingID, body.Details)
	respond(w, options, err)
}

func (handler *HTTPHandler) OpenReports(w http.ResponseWriter, r *http.Request) {
	adminID, ok := authenticated(w, r)
	if !ok {
		return
	}
	reports, err := handler.desk.OpenReports(r.Context(), adminID)
	respond(w, map[string][]Report{"reports": reports}, err)
}

func (handler *HTTPHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	adminID, bookingID, ok := identify(w, r)
	if !ok {
		return
	}
	var decision Decision
	if !decode(w, r, &decision) {
		return
	}
	report, err := handler.desk.Resolve(r.Context(), adminID, bookingID, decision)
	respond(w, report, err)
}

func authenticated(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	actorID, ok := accounts.UserID(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	}
	return actorID, ok
}

func identify(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	actorID, ok := authenticated(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	bookingID, err := uuid.Parse(r.PathValue("booking_id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": ErrBookingNotFound.Error()})
		return uuid.Nil, uuid.Nil, false
	}
	return actorID, bookingID, true
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must contain one JSON object"})
		return false
	}
	return true
}

func respond(w http.ResponseWriter, body any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, body)
		return
	}
	status, message := http.StatusInternalServerError, "something went wrong"
	switch {
	case errors.Is(err, ErrBookingNotFound), errors.Is(err, ErrNotAdmin), errors.Is(err, ErrListingNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, ErrInvalidReport), errors.Is(err, ErrInvalidReason):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrNotCancellable), errors.Is(err, ErrRefundChanged), errors.Is(err, ErrReportClosed),
		errors.Is(err, ErrAlreadyReported), errors.Is(err, ErrAlreadyDecided),
		errors.Is(err, ErrAlreadyPaused), errors.Is(err, ErrNotPaused):
		status, message = http.StatusConflict, err.Error()
	default:
		slog.Error("Cancellation request", "error", err)
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
