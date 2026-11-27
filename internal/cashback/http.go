package cashback

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type HTTPHandler struct {
	uploads   UploadRequester
	plans     PlanEditor
	host      HostReader
	trips     TripReader
	submitter PhotoSubmitter
	reviewer  Reviewer
}

func NewHTTPHandler(trips TripReader, submitter PhotoSubmitter, reviewer Reviewer, host HostReader, uploads UploadRequester) *HTTPHandler {
	return &HTTPHandler{uploads: uploads, host: host, trips: trips, submitter: submitter, reviewer: reviewer}
}

func (handler *HTTPHandler) WithPlans(plans PlanEditor) *HTTPHandler {
	handler.plans = plans
	return handler
}

func (handler *HTTPHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := actorAndPathID(w, r, "listing_id")
	if !ok {
		return
	}
	plan, err := handler.plans.ListingPlan(r.Context(), hostID, listingID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (handler *HTTPHandler) SavePlan(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := actorAndPathID(w, r, "listing_id")
	if !ok {
		return
	}
	var request PlanView
	if !decodeBody(w, r, &request) {
		return
	}
	plan, err := handler.plans.SaveListingPlan(r.Context(), hostID, listingID, request.Groups)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (handler *HTTPHandler) RequestExampleUpload(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := actorAndPathID(w, r, "listing_id")
	if !ok {
		return
	}
	var request UploadRequest
	if !decodeBody(w, r, &request) {
		return
	}
	ticket, err := handler.plans.RequestExampleUpload(r.Context(), hostID, listingID, request.ContentType, request.SizeBytes)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ticket)
}

type SubmitPhotoRequest struct {
	PhotoKey string `json:"photo_key"`
}

type UploadRequest struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type ReviewRequest struct {
	Decision Decision `json:"decision"`
	Note     string   `json:"note"`
}

type ReviewResponse struct {
	Submission Submission `json:"submission"`
}

func (handler *HTTPHandler) GetTrip(w http.ResponseWriter, r *http.Request) {
	guestID, bookingID, ok := actorAndPathID(w, r, "booking_id")
	if !ok {
		return
	}
	trip, err := handler.trips.Trip(r.Context(), guestID, bookingID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trip)
}

func (handler *HTTPHandler) GetHostTrip(w http.ResponseWriter, r *http.Request) {
	hostID, bookingID, ok := actorAndPathID(w, r, "booking_id")
	if !ok {
		return
	}
	trip, err := handler.host.HostTrip(r.Context(), hostID, bookingID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trip)
}

func (handler *HTTPHandler) ListPending(w http.ResponseWriter, r *http.Request) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	pending, err := handler.host.PendingForHost(r.Context(), hostID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"submissions": pending})
}

func (handler *HTTPHandler) SubmitPhoto(w http.ResponseWriter, r *http.Request) {
	guestID, bookingID, ok := actorAndPathID(w, r, "booking_id")
	if !ok {
		return
	}
	var request SubmitPhotoRequest
	if !decodeBody(w, r, &request) {
		return
	}
	submission, err := handler.submitter.SubmitPhoto(r.Context(), guestID, bookingID, r.PathValue("task_key"), request.PhotoKey)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, submission)
}

func (handler *HTTPHandler) RequestUpload(w http.ResponseWriter, r *http.Request) {
	guestID, bookingID, ok := actorAndPathID(w, r, "booking_id")
	if !ok {
		return
	}
	var request UploadRequest
	if !decodeBody(w, r, &request) {
		return
	}
	ticket, err := handler.uploads.RequestUpload(r.Context(), guestID, bookingID, r.PathValue("task_key"), request.ContentType, request.SizeBytes)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ticket)
}

func (handler *HTTPHandler) ReviewSubmission(w http.ResponseWriter, r *http.Request) {
	hostID, submissionID, ok := actorAndPathID(w, r, "submission_id")
	if !ok {
		return
	}
	var request ReviewRequest
	if !decodeBody(w, r, &request) {
		return
	}
	submission, err := handler.reviewer.Review(r.Context(), hostID, submissionID, request.Decision, request.Note)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ReviewResponse{Submission: submission})
}

func actorAndPathID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, uuid.UUID, bool) {
	actorID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid "+name)
		return uuid.Nil, uuid.Nil, false
	}
	return actorID, id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	return true
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUploadsDisabled):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrNotVerified):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrNotHost):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrBookingNotFound), errors.Is(err, ErrSubmissionNotFound), errors.Is(err, ErrListingNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrBookingNotEarning), errors.Is(err, ErrSubmissionLocked), errors.Is(err, ErrNotAwaitingReview), errors.Is(err, ErrPhotoWindowClosed), errors.Is(err, ErrReviewClosed):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidPlan), errors.Is(err, ErrUnknownTask), errors.Is(err, ErrInvalidPhotoKey), errors.Is(err, ErrPhotoRejected), errors.Is(err, ErrPhotoNotUploaded), errors.Is(err, ErrInvalidDecision), errors.Is(err, ErrRejectNeedsNote):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("Cashback request", "error", err)
		writeError(w, http.StatusInternalServerError, "could not complete request")
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
