package proofs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

const maxBodyBytes = 1 << 14

type HTTPHandler struct {
	uploads UploadRequester
	submits Submitter
	status  StatusReader
	admins  AdminChecker
	queue   ReviewQueue
}

func NewHTTPHandler(uploads UploadRequester, submits Submitter, status StatusReader, admins AdminChecker, queue ReviewQueue) *HTTPHandler {
	return &HTTPHandler{uploads: uploads, submits: submits, status: status, admins: admins, queue: queue}
}

type uploadRequest struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type revokeRequest struct {
	Reason string `json:"reason"`
}

type overturnRequest struct {
	Note string `json:"note"`
}

type decisionRequest struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
}

func (handler *HTTPHandler) RequestUpload(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	var request uploadRequest
	if !decodeBody(w, r, &request) {
		return
	}
	ticket, err := handler.uploads.RequestUpload(r.Context(), hostID, listingID, request.ContentType, request.SizeBytes)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ticket)
}

func (handler *HTTPHandler) Submit(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	var request Submission
	if !decodeBody(w, r, &request) {
		return
	}
	proof, err := handler.submits.SubmitAuthority(r.Context(), hostID, listingID, request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, proof)
}

func (handler *HTTPHandler) Status(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	proof, found, err := handler.status.Status(r.Context(), hostID, listingID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"proof": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proof": proof})
}

func (handler *HTTPHandler) Me(w http.ResponseWriter, r *http.Request) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	admin, err := handler.admins.IsAdmin(r.Context(), accountID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"admin": admin})
}

func (handler *HTTPHandler) ListForReview(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	list := handler.queue.ListSubmitted
	switch Status(r.URL.Query().Get("status")) {
	case Approved:
		list = handler.queue.ListApproved
	case Rejected:
		list = handler.queue.ListRejected
	}
	items, err := list(r.Context(), adminID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if items == nil {
		items = []ReviewItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proofs": items})
}

func (handler *HTTPHandler) Decide(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	proofID, err := uuid.Parse(r.PathValue("proof_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "proof not found")
		return
	}
	var request decisionRequest
	if !decodeBody(w, r, &request) {
		return
	}
	if err := handler.queue.Decide(r.Context(), adminID, proofID, request.Decision, request.Reason); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": proofID.String(), "decision": string(request.Decision)})
}

func (handler *HTTPHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	proofID, err := uuid.Parse(r.PathValue("proof_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "proof not found")
		return
	}
	var request revokeRequest
	if !decodeBody(w, r, &request) {
		return
	}
	if err := handler.queue.Revoke(r.Context(), adminID, proofID, request.Reason); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": proofID.String()})
}

func (handler *HTTPHandler) Overturn(w http.ResponseWriter, r *http.Request) {
	adminID, proofID, ok := adminAndProof(w, r)
	if !ok {
		return
	}
	var request overturnRequest
	if !decodeBody(w, r, &request) {
		return
	}
	if err := handler.queue.Overturn(r.Context(), adminID, proofID, request.Note); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": proofID.String()})
}

func (handler *HTTPHandler) Erase(w http.ResponseWriter, r *http.Request) {
	adminID, proofID, ok := adminAndProof(w, r)
	if !ok {
		return
	}
	if err := handler.queue.Erase(r.Context(), adminID, proofID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": proofID.String()})
}

func adminAndProof(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, uuid.Nil, false
	}
	proofID, err := uuid.Parse(r.PathValue("proof_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "proof not found")
		return uuid.Nil, uuid.Nil, false
	}
	return adminID, proofID, true
}

func hostAndListing(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, uuid.Nil, false
	}
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "listing not found")
		return uuid.Nil, uuid.Nil, false
	}
	return hostID, listingID, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return false
	}
	return true
}

var statusByError = []struct {
	err    error
	status int
}{
	{ErrDocumentRejected, http.StatusBadRequest},
	{ErrInvalidRelationship, http.StatusBadRequest},
	{ErrWrongDocument, http.StatusBadRequest},
	{ErrAttachments, http.StatusBadRequest},
	{ErrOwnerDetails, http.StatusBadRequest},
	{ErrExpiry, http.StatusBadRequest},
	{ErrAuthorityExpired, http.StatusConflict},
	{ErrInvalidDocument, http.StatusBadRequest},
	{ErrInvalidObjectKey, http.StatusBadRequest},
	{ErrNotUploaded, http.StatusBadRequest},
	{ErrInvalidDecision, http.StatusBadRequest},
	{ErrReasonRequired, http.StatusBadRequest},
	{ErrNoteRequired, http.StatusBadRequest},
	{ErrNotRejected, http.StatusConflict},
	{ErrDocumentErased, http.StatusConflict},
	{ErrSuperseded, http.StatusConflict},
	{ErrListingNotFound, http.StatusNotFound},
	{ErrProofNotFound, http.StatusNotFound},
	{ErrNotListingOwner, http.StatusForbidden},
	{ErrNotAdmin, http.StatusForbidden},
	{ErrProofPending, http.StatusConflict},
	{ErrNotAwaitingReview, http.StatusConflict},
	{ErrNotApproved, http.StatusConflict},
	{ErrUploadsDisabled, http.StatusServiceUnavailable},
}

func writeServiceError(w http.ResponseWriter, err error) {
	for _, mapped := range statusByError {
		if errors.Is(err, mapped.err) {
			writeError(w, mapped.status, mapped.err.Error())
			return
		}
	}
	slog.Error("property proof request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "something went wrong")
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
