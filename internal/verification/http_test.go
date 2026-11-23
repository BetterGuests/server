package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type stubStarter struct {
	result    StartResult
	err       error
	accountID uuid.UUID
	replace   bool
}

func (stub *stubStarter) StartVerification(_ context.Context, accountID uuid.UUID, replace bool) (StartResult, error) {
	stub.accountID = accountID
	stub.replace = replace
	return stub.result, stub.err
}

type stubProcessor struct {
	err    error
	header string
	body   []byte
}

func (stub *stubProcessor) HandleWebhook(_ context.Context, header string, body []byte) error {
	stub.header = header
	stub.body = body
	return stub.err
}

func TestStartVerificationEndpoint(t *testing.T) {
	accountID := uuid.New()
	tests := []struct {
		name         string
		authenticate bool
		err          error
		want         int
	}{
		{"unauthenticated", false, nil, http.StatusUnauthorized},
		{"created", true, nil, http.StatusCreated},
		{"already verified", true, ErrAlreadyVerified, http.StatusConflict},
		{"unexpected failure", true, errors.New("boom"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			starter := &stubStarter{result: StartResult{InquiryID: "inq_1", SessionToken: "tok", Status: StatusCreated}, err: test.err}
			handler := NewHTTPHandler(starter, &stubProcessor{})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/verification/inquiries", nil)
			if test.authenticate {
				request = request.WithContext(accounts.WithUserID(request.Context(), accountID))
			}
			recorder := httptest.NewRecorder()
			handler.StartVerification(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if test.want == http.StatusCreated {
				var body StartResult
				if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil || body.SessionToken != "tok" || starter.accountID != accountID {
					t.Fatalf("body = %+v err = %v account = %s", body, err, starter.accountID)
				}
				if recorder.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("session token response must not be cacheable")
				}
			}
		})
	}
}

func TestPersonaWebhookEndpointStatusMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"ok", nil, http.StatusOK},
		{"bad signature", ErrInvalidSignature, http.StatusUnauthorized},
		{"bad payload", ErrInvalidPayload, http.StatusBadRequest},
		{"unknown inquiry", ErrInquiryNotFound, http.StatusNotFound},
		{"unexpected failure", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor := &stubProcessor{err: test.err}
			handler := NewHTTPHandler(&stubStarter{}, processor)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/persona", bytes.NewReader([]byte(`{"a":1}`)))
			request.Header.Set("Persona-Signature", "t=1,v1=ab")
			recorder := httptest.NewRecorder()
			handler.PersonaWebhook(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if processor.header != "t=1,v1=ab" || string(processor.body) != `{"a":1}` {
				t.Fatalf("processor got header %q body %q", processor.header, processor.body)
			}
		})
	}
}

func TestPersonaWebhookEndpointRejectsOversizedBody(t *testing.T) {
	handler := NewHTTPHandler(&stubStarter{}, &stubProcessor{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/persona", bytes.NewReader(make([]byte, maxWebhookBodyBytes+1)))
	recorder := httptest.NewRecorder()
	handler.PersonaWebhook(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestWebhookEndToEndThroughHandler(t *testing.T) {
	service, store, accountID := seededService(t)
	handler := NewHTTPHandler(&stubStarter{}, service)
	body := eventBody(EventDeclined, "inq_1", accountID.String(), testNow)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/persona", bytes.NewReader(body))
	request.Header.Set("Persona-Signature", signedHeader(testSecret, testNow, body))
	recorder := httptest.NewRecorder()
	handler.PersonaWebhook(recorder, request)
	if recorder.Code != http.StatusOK || store.rows["inq_1"].Status != StatusDeclined {
		t.Fatalf("status = %d, row = %+v", recorder.Code, store.rows["inq_1"])
	}
}

type stubStatus struct {
	status Status
	found  bool
	err    error
}

func (stub stubStatus) LatestStatus(context.Context, uuid.UUID) (Status, bool, error) {
	return stub.status, stub.found, stub.err
}

func statusRequest(authenticated bool) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/verification/status", nil)
	if authenticated {
		request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
	}
	return request
}

func TestStatusEndpointReportsEveryState(t *testing.T) {
	cases := []struct {
		name         string
		status       stubStatus
		wantStatus   string
		wantVerified bool
	}{
		{"never started", stubStatus{}, "none", false},
		{"in progress", stubStatus{status: StatusPending, found: true}, "pending", false},
		{"needs review", stubStatus{status: StatusNeedsReview, found: true}, "needs_review", false},
		{"declined", stubStatus{status: StatusDeclined, found: true}, "declined", false},
		{"failed", stubStatus{status: StatusFailed, found: true}, "failed", false},
		{"expired", stubStatus{status: StatusExpired, found: true}, "expired", false},
		{"completed", stubStatus{status: StatusCompleted, found: true}, "completed", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(&stubStarter{}, &stubProcessor{}).WithStatusReader(test.status)
			response := httptest.NewRecorder()
			handler.GetStatus(response, statusRequest(true))
			var decoded StatusResponse
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || response.Code != http.StatusOK {
				t.Fatalf("code = %d, err = %v", response.Code, err)
			}
			if decoded.Status != test.wantStatus || decoded.Verified != test.wantVerified {
				t.Fatalf("decoded = %+v", decoded)
			}
		})
	}
}

func TestStatusEndpointRequiresAuthenticationAndHidesErrors(t *testing.T) {
	handler := NewHTTPHandler(&stubStarter{}, &stubProcessor{}).WithStatusReader(stubStatus{err: errors.New("pq: secret detail")})
	unauthenticated := httptest.NewRecorder()
	handler.GetStatus(unauthenticated, statusRequest(false))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", unauthenticated.Code)
	}
	failing := httptest.NewRecorder()
	handler.GetStatus(failing, statusRequest(true))
	if failing.Code != http.StatusInternalServerError || strings.Contains(failing.Body.String(), "secret detail") {
		t.Fatalf("code = %d body = %s", failing.Code, failing.Body)
	}
}

func TestStartVerificationEndpointPassesReplaceFlag(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{"/api/v1/verification/inquiries", false},
		{"/api/v1/verification/inquiries?replace=true", true},
		{"/api/v1/verification/inquiries?replace=yes", false},
	}
	for _, test := range tests {
		t.Run(test.target, func(t *testing.T) {
			starter := &stubStarter{result: StartResult{InquiryID: "inq_1", Status: StatusCreated}}
			handler := NewHTTPHandler(starter, &stubProcessor{})
			request := httptest.NewRequest(http.MethodPost, test.target, nil)
			request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
			handler.StartVerification(httptest.NewRecorder(), request)
			if starter.replace != test.want {
				t.Fatalf("replace = %v, want %v", starter.replace, test.want)
			}
		})
	}
}
