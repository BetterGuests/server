package proofs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type failingQueue struct{ err error }

func (queue failingQueue) ListSubmitted(context.Context, uuid.UUID) ([]ReviewItem, error) {
	return nil, queue.err
}

func (queue failingQueue) ListApproved(context.Context, uuid.UUID) ([]ReviewItem, error) {
	return nil, queue.err
}

func (queue failingQueue) ListRejected(context.Context, uuid.UUID) ([]ReviewItem, error) {
	return nil, queue.err
}

func (queue failingQueue) Overturn(context.Context, uuid.UUID, uuid.UUID, string) error {
	return queue.err
}

func (queue failingQueue) Erase(context.Context, uuid.UUID, uuid.UUID) error {
	return queue.err
}

func (queue failingQueue) Revoke(context.Context, uuid.UUID, uuid.UUID, string) error {
	return queue.err
}

func (queue failingQueue) Decide(context.Context, uuid.UUID, uuid.UUID, Decision, string) error {
	return queue.err
}

func TestAdminEndpointsMapErrorsToStatusCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"allowed", nil, http.StatusOK},
		{"not an admin", ErrNotAdmin, http.StatusForbidden},
		{"already decided", ErrNotAwaitingReview, http.StatusConflict},
		{"missing proof", ErrProofNotFound, http.StatusNotFound},
		{"missing reason", ErrReasonRequired, http.StatusBadRequest},
		{"unexpected failure", errors.New("database password is hunter2"), http.StatusInternalServerError},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler := NewHTTPHandler(nil, nil, nil, nil, failingQueue{err: testCase.err})
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"decision":"approve"}`))
			request.SetPathValue("proof_id", uuid.NewString())
			request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
			response := httptest.NewRecorder()

			handler.Decide(response, request)

			if response.Code != testCase.want {
				t.Fatalf("expected %d, got %d", testCase.want, response.Code)
			}
			if strings.Contains(response.Body.String(), "hunter2") {
				t.Fatal("internal error detail leaked to the client")
			}
		})
	}
}

func TestAdminEndpointsRequireAuthentication(t *testing.T) {
	handler := NewHTTPHandler(nil, nil, nil, nil, failingQueue{})
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"list":   handler.ListForReview,
		"revoke": handler.Revoke,
		"decide": handler.Decide,
		"me":     handler.Me,
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			call(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", response.Code)
			}
		})
	}
}

func TestDecideRejectsUnknownFields(t *testing.T) {
	handler := NewHTTPHandler(nil, nil, nil, nil, failingQueue{})
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"decision":"approve","reviewed_by":"x"}`))
	request.SetPathValue("proof_id", uuid.NewString())
	request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
	response := httptest.NewRecorder()

	handler.Decide(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}
