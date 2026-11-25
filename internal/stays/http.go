package stays

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

const maxGuideBodyBytes = 32 << 10

type HTTPHandler struct {
	stays  StayManager
	guides GuideManager
}

func NewHTTPHandler(stays StayManager, guides GuideManager) *HTTPHandler {
	return &HTTPHandler{stays: stays, guides: guides}
}

func (handler *HTTPHandler) GetStay(w http.ResponseWriter, r *http.Request) {
	handler.stayAction(w, r, handler.stays.Stay)
}

func (handler *HTTPHandler) CheckIn(w http.ResponseWriter, r *http.Request) {
	handler.stayAction(w, r, handler.stays.CheckIn)
}

func (handler *HTTPHandler) CheckOut(w http.ResponseWriter, r *http.Request) {
	handler.stayAction(w, r, handler.stays.CheckOut)
}

func (handler *HTTPHandler) stayAction(
	w http.ResponseWriter,
	r *http.Request,
	action func(context.Context, uuid.UUID, uuid.UUID) (View, error),
) {
	actorID, bookingID, ok := identify(w, r, "booking_id", "stay not found")
	if !ok {
		return
	}
	view, err := action(r.Context(), actorID, bookingID)
	if err != nil {
		writeStayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (handler *HTTPHandler) GetGuide(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := identify(w, r, "listing_id", "listing not found")
	if !ok {
		return
	}
	guide, err := handler.guides.Guide(r.Context(), hostID, listingID)
	if err != nil {
		writeStayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, guide)
}

func (handler *HTTPHandler) SaveGuide(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := identify(w, r, "listing_id", "listing not found")
	if !ok {
		return
	}
	var input GuideInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGuideBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	guide, err := handler.guides.SaveGuide(r.Context(), hostID, listingID, input)
	if err != nil {
		writeStayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, guide)
}

func identify(w http.ResponseWriter, r *http.Request, pathValue, missing string) (uuid.UUID, uuid.UUID, bool) {
	actorID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue(pathValue))
	if err != nil {
		writeError(w, http.StatusNotFound, missing)
		return uuid.Nil, uuid.Nil, false
	}
	return actorID, id, true
}

func writeStayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrStayNotFound), errors.Is(err, ErrListingNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidGuide):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrGuestOnly):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrCheckInClosed), errors.Is(err, ErrCheckOutClosed), errors.Is(err, ErrScheduleLocked):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("Stay request", "error", err)
		writeError(w, http.StatusInternalServerError, "something went wrong")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
