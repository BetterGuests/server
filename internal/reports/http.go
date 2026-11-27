package reports

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
	filer Filer
	desk  Desk
}

func NewHTTPHandler(filer Filer, desk Desk) *HTTPHandler {
	return &HTTPHandler{filer: filer, desk: desk}
}

func (handler *HTTPHandler) File(w http.ResponseWriter, r *http.Request) {
	reporterID, ok := authenticated(w, r)
	if !ok {
		return
	}
	var submission Submission
	if !decode(w, r, &submission) {
		return
	}
	receipt, err := handler.filer.File(r.Context(), reporterID, submission)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]Receipt{"report": receipt})
}

func (handler *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	adminID, ok := authenticated(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "open" && status != "closed" {
		writeError(w, http.StatusBadRequest, "status must be open or closed")
		return
	}
	found, err := handler.desk.List(r.Context(), adminID, status != "closed")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]Report{"reports": found})
}

func (handler *HTTPHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	adminID, ok := authenticated(w, r)
	if !ok {
		return
	}
	reportID, err := uuid.Parse(r.PathValue("report_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrReportNotFound.Error())
		return
	}
	var decision Decision
	if !decode(w, r, &decision) {
		return
	}
	if err := handler.desk.Resolve(r.Context(), adminID, reportID, decision); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func authenticated(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	accountID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
	return accountID, ok
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
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

func fail(w http.ResponseWriter, err error) {
	for _, known := range []struct {
		err    error
		status int
	}{
		{ErrInvalidReport, http.StatusBadRequest},
		{ErrInvalidDetails, http.StatusBadRequest},
		{ErrOwnReport, http.StatusBadRequest},
		{ErrInvalidDecision, http.StatusBadRequest},
		{ErrTargetNotFound, http.StatusNotFound},
		{ErrReportNotFound, http.StatusNotFound},
		{ErrNotAdmin, http.StatusNotFound},
		{ErrAlreadyReported, http.StatusConflict},
	} {
		if errors.Is(err, known.err) {
			writeError(w, known.status, known.err.Error())
			return
		}
	}
	slog.Error("Report request", "error", err)
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
