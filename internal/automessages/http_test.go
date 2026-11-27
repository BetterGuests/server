package automessages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type managerStub struct {
	err   error
	input Input
}

func (stub *managerStub) Overview(context.Context, uuid.UUID, uuid.UUID) (Overview, error) {
	return Overview{}, stub.err
}

func (stub *managerStub) Create(_ context.Context, _, _ uuid.UUID, input Input) (AutoMessage, error) {
	stub.input = input
	return AutoMessage{}, stub.err
}

func (stub *managerStub) Update(_ context.Context, _, _, _ uuid.UUID, input Input) (AutoMessage, error) {
	stub.input = input
	return AutoMessage{}, stub.err
}

func (stub *managerStub) Delete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return stub.err
}

func TestHandlersMapFailuresToStatuses(t *testing.T) {
	const valid = `{"name":"Hi","trigger":"check_in_day","body":"Hello","private":false,"enabled":true}`
	listingID, messageID := uuid.NewString(), uuid.NewString()
	handlerFor := func(handler *HTTPHandler, method string) http.HandlerFunc {
		switch method {
		case http.MethodPost:
			return handler.Create
		case http.MethodPut:
			return handler.Update
		case http.MethodDelete:
			return handler.Delete
		}
		return handler.List
	}
	tests := []struct {
		name      string
		method    string
		body      string
		listingID string
		signedOut bool
		err       error
		want      int
	}{
		{name: "list", method: http.MethodGet, want: http.StatusOK},
		{name: "signed out", method: http.MethodGet, signedOut: true, want: http.StatusUnauthorized},
		{name: "bad listing id", method: http.MethodGet, listingID: "nope", want: http.StatusNotFound},
		{name: "someone else's listing", method: http.MethodPost, body: valid, err: ErrListingNotFound, want: http.StatusNotFound},
		{name: "someone else's message", method: http.MethodPut, body: valid, err: ErrMessageNotFound, want: http.StatusNotFound},
		{name: "deleting someone else's message", method: http.MethodDelete, err: ErrMessageNotFound, want: http.StatusNotFound},
		{name: "created", method: http.MethodPost, body: valid, want: http.StatusCreated},
		{name: "deleted", method: http.MethodDelete, want: http.StatusNoContent},
		{name: "too many", method: http.MethodPost, body: valid, err: ErrTooMany, want: http.StatusConflict},
		{name: "invalid", method: http.MethodPost, body: valid, err: ErrInvalidMessage, want: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, body: `{"host_id":"x"}`, want: http.StatusBadRequest},
		{name: "two objects", method: http.MethodPost, body: valid + valid, want: http.StatusBadRequest},
		{name: "oversized", method: http.MethodPost, body: `{"body":"` + strings.Repeat("a", maxBodyBytes) + `"}`, want: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(&managerStub{err: test.err})
			request := httptest.NewRequest(test.method, "/", strings.NewReader(test.body))
			if test.listingID == "" {
				test.listingID = listingID
			}
			request.SetPathValue("listing_id", test.listingID)
			request.SetPathValue("auto_message_id", messageID)
			if !test.signedOut {
				request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
			}
			response := httptest.NewRecorder()
			handlerFor(handler, test.method).ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status %d, want %d: %s", response.Code, test.want, response.Body)
			}
		})
	}
}
