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
	checks  CheckStarter
}

type checkRequest struct {
	DocumentType DocumentType `json:"document_type"`
}

func (handler *HTTPHandler) WithChecks(checks CheckStarter) *HTTPHandler {
	handler.checks = checks
	return handler
}

func (handler *HTTPHandler) StartCheck(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	if handler.checks == nil {
		writeServiceError(w, ErrChecksDisabled)
		return
	}
	var request checkRequest
	if !decodeBody(w, r, &request) {
		return
	}
	ticket, err := handler.checks.Start(r.Context(), hostID, listingID, request.DocumentType)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ticket)
}

func NewHTTPHandler(uploads UploadRequester, submits Submitter, status StatusReader, admins AdminChecker, queue ReviewQueue) *HTTPHandler {
	return &HTTPHandler{uploads: uploads, submits: submits, status: status, admins: admins, queue: queue}
}

type uploadRequest struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type submitRequest struct {
	DocumentType DocumentType `json:"document_type"`
	ObjectKey    string       `json:"object_key"`
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
	var request submitRequest
	if !decodeBody(w, r, &request) {
		return
	}
	proof, err := handler.submits.Submit(r.Context(), hostID, listingID, request.DocumentType, request.ObjectKey)
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
	response := map[string]any{"proof": nil, "persona_check": handler.checks != nil}
	if found {
		response["proof"] = proof
	}
	writeJSON(w, http.StatusOK, response)
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

func (handler *HTTPHandler) ListSubmitted(w http.ResponseWriter, r *http.Request) {
	adminID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	items, err := handler.queue.ListSubmitted(r.Context(), adminID)
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
	{ErrInvalidDocument, http.StatusBadRequest},
	{ErrInvalidObjectKey, http.StatusBadRequest},
	{ErrNotUploaded, http.StatusBadRequest},
	{ErrInvalidDecision, http.StatusBadRequest},
	{ErrReasonRequired, http.StatusBadRequest},
	{ErrListingNotFound, http.StatusNotFound},
	{ErrProofNotFound, http.StatusNotFound},
	{ErrNotListingOwner, http.StatusForbidden},
	{ErrNotAdmin, http.StatusForbidden},
	{ErrOwnListing, http.StatusForbidden},
	{ErrProofPending, http.StatusConflict},
	{ErrNotAwaitingReview, http.StatusConflict},
	{ErrUploadsDisabled, http.StatusServiceUnavailable},
	{ErrChecksDisabled, http.StatusServiceUnavailable},
	{ErrIDRequired, http.StatusForbidden},
	{ErrAlreadyProven, http.StatusConflict},
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
