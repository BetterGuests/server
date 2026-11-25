package messages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

const maxRequestBytes = 1 << 16

type Inbox interface {
	List(ctx context.Context, actorID uuid.UUID) ([]Summary, error)
	Get(ctx context.Context, actorID, conversationID uuid.UUID) (Summary, error)
	Messages(ctx context.Context, actorID, conversationID uuid.UUID, before *time.Time, limit int) (MessagePage, error)
	MarkRead(ctx context.Context, actorID, conversationID uuid.UUID) error
}

type Opener interface {
	Open(ctx context.Context, actorID uuid.UUID, target OpenTarget) (Summary, error)
}

type Sender interface {
	Send(ctx context.Context, actorID, conversationID uuid.UUID, body string) (Message, error)
}

type HTTPHandler struct {
	inbox  Inbox
	opener Opener
	sender Sender
}

func NewHTTPHandler(inbox Inbox, opener Opener, sender Sender) *HTTPHandler {
	return &HTTPHandler{inbox: inbox, opener: opener, sender: sender}
}

type OpenRequest struct {
	BookingID uuid.UUID `json:"booking_id"`
	ListingID uuid.UUID `json:"listing_id"`
}

type SendRequest struct {
	Body string `json:"body"`
}

func (handler *HTTPHandler) ListConversations(w http.ResponseWriter, r *http.Request) {
	actorID, ok := actor(w, r)
	if !ok {
		return
	}
	summaries, err := handler.inbox.List(r.Context(), actorID)
	if err != nil {
		writeServiceError(w, "List conversations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]Summary{"conversations": summaries})
}

func (handler *HTTPHandler) OpenConversation(w http.ResponseWriter, r *http.Request) {
	actorID, ok := actor(w, r)
	if !ok {
		return
	}
	var request OpenRequest
	if !decode(w, r, &request) {
		return
	}
	summary, err := handler.opener.Open(r.Context(), actorID, OpenTarget{BookingID: request.BookingID, ListingID: request.ListingID})
	if err != nil {
		writeServiceError(w, "Open conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (handler *HTTPHandler) GetConversation(w http.ResponseWriter, r *http.Request) {
	actorID, conversationID, ok := actorAndConversation(w, r)
	if !ok {
		return
	}
	summary, err := handler.inbox.Get(r.Context(), actorID, conversationID)
	if err != nil {
		writeServiceError(w, "Get conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (handler *HTTPHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	actorID, conversationID, ok := actorAndConversation(w, r)
	if !ok {
		return
	}
	before, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	page, err := handler.inbox.Messages(r.Context(), actorID, conversationID, before, limit)
	if err != nil {
		writeServiceError(w, "List messages", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (handler *HTTPHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	actorID, conversationID, ok := actorAndConversation(w, r)
	if !ok {
		return
	}
	var request SendRequest
	if !decode(w, r, &request) {
		return
	}
	message, err := handler.sender.Send(r.Context(), actorID, conversationID, request.Body)
	if err != nil {
		writeServiceError(w, "Send message", err)
		return
	}
	writeJSON(w, http.StatusCreated, message)
}

func (handler *HTTPHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	actorID, conversationID, ok := actorAndConversation(w, r)
	if !ok {
		return
	}
	if err := handler.inbox.MarkRead(r.Context(), actorID, conversationID); err != nil {
		writeServiceError(w, "Mark conversation read", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func actor(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	actorID, ok := accounts.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
	}
	return actorID, ok
}

func actorAndConversation(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	actorID, ok := actor(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "conversation_id must be a UUID")
		return uuid.Nil, uuid.Nil, false
	}
	return actorID, conversationID, true
}

func pageParams(w http.ResponseWriter, r *http.Request) (*time.Time, int, bool) {
	var before *time.Time
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be an RFC 3339 timestamp")
			return nil, 0, false
		}
		before = &parsed
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return nil, 0, false
		}
		limit = parsed
	}
	return before, limit, true
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
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

func writeServiceError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, ErrConversationNotFound):
		writeError(w, http.StatusNotFound, "conversation not found")
	case errors.Is(err, ErrInvalidConversation), errors.Is(err, ErrInvalidMessage):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error(operation, "error", err)
		writeError(w, http.StatusInternalServerError, "could not complete the request")
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
