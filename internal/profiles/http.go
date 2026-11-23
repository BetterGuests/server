package profiles

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type HTTPHandler struct {
	reader  BannerReader
	uploads UploadRequester
	saver   BannerSaver
	remover BannerRemover
}

func NewHTTPHandler(reader BannerReader, uploads UploadRequester, saver BannerSaver, remover BannerRemover) *HTTPHandler {
	return &HTTPHandler{reader: reader, uploads: uploads, saver: saver, remover: remover}
}

type UploadRequest struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type SaveRequest struct {
	BannerKey string `json:"banner_key"`
}

func (handler *HTTPHandler) GetBanner(w http.ResponseWriter, r *http.Request) {
	userID, ok := actor(w, r)
	if !ok {
		return
	}
	banner, err := handler.reader.Banner(r.Context(), userID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, banner)
}

func (handler *HTTPHandler) RequestUpload(w http.ResponseWriter, r *http.Request) {
	userID, ok := actor(w, r)
	if !ok {
		return
	}
	var request UploadRequest
	if !decodeBody(w, r, &request) {
		return
	}
	ticket, err := handler.uploads.RequestBannerUpload(r.Context(), userID, request.ContentType, request.SizeBytes)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ticket)
}

func (handler *HTTPHandler) SaveBanner(w http.ResponseWriter, r *http.Request) {
	userID, ok := actor(w, r)
	if !ok {
		return
	}
	var request SaveRequest
	if !decodeBody(w, r, &request) {
		return
	}
	banner, err := handler.saver.SaveBanner(r.Context(), userID, request.BannerKey)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, banner)
}

func (handler *HTTPHandler) RemoveBanner(w http.ResponseWriter, r *http.Request) {
	userID, ok := actor(w, r)
	if !ok {
		return
	}
	if err := handler.remover.RemoveBanner(r.Context(), userID); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func actor(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	userID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, false
	}
	return userID, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	if decoder.More() {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	return true
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUploadsDisabled):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrBannerRejected), errors.Is(err, ErrInvalidBannerKey), errors.Is(err, ErrBannerNotUploaded):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("Profile request", "error", err)
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
