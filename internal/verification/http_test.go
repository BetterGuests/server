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
	"time"

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

func (stub stubStatus) Attempts(context.Context, uuid.UUID) ([]Attempt, error) {
	if !stub.found {
		return nil, stub.err
	}
	return []Attempt{{InquiryID: "inq_1", Status: stub.status, CompletedAt: time.Now()}}, stub.err
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

type stubHandoffStarter struct {
	stubStarter
	handoffResult  StartResult
	handoffErr     error
	handoffCalls   int
	handoffReplace bool
}

func (stub *stubHandoffStarter) StartHandoff(_ context.Context, accountID uuid.UUID, replace bool) (StartResult, error) {
	stub.handoffCalls++
	stub.accountID = accountID
	stub.handoffReplace = replace
	return stub.handoffResult, stub.handoffErr
}

func handoffRequest(target string, accountID uuid.UUID) *http.Request {
	request := httptest.NewRequest(http.MethodPost, target, nil)
	return request.WithContext(accounts.WithUserID(request.Context(), accountID))
}

func TestStartVerificationHandoffReturnsOneTimeLinkAndNeverTouchesTheEmbeddedPath(t *testing.T) {
	accountID := uuid.New()
	starter := &stubHandoffStarter{handoffResult: StartResult{InquiryID: "inq_9", OneTimeLink: "https://inquiry.withpersona.com/verify?code=abc", Status: StatusCreated}}
	handler := NewHTTPHandler(starter, &stubProcessor{})
	recorder := httptest.NewRecorder()
	handler.StartVerification(recorder, handoffRequest("/api/v1/verification/inquiries?handoff=true&replace=true", accountID))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["one_time_link"] != "https://inquiry.withpersona.com/verify?code=abc" || body["inquiry_id"] != "inq_9" {
		t.Fatalf("body = %v", body)
	}
	if _, leaked := body["session_token"]; leaked && body["session_token"] != "" {
		t.Fatalf("handoff response must not carry a session token: %v", body)
	}
	if starter.handoffCalls != 1 || !starter.handoffReplace || starter.accountID != accountID {
		t.Fatalf("handoff calls = %d replace = %v account = %s", starter.handoffCalls, starter.handoffReplace, starter.accountID)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("one-time link response must not be cacheable")
	}
}

func TestStartVerificationHandoffFailureModes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"already verified", ErrAlreadyVerified, http.StatusConflict},
		{"consent missing", ErrConsentRequired, http.StatusForbidden},
		{"handoff unsupported", ErrHandoffUnavailable, http.StatusNotFound},
		{"persona down", errors.New("persona request failed: status 503"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(&stubHandoffStarter{handoffErr: test.err}, &stubProcessor{})
			recorder := httptest.NewRecorder()
			handler.StartVerification(recorder, handoffRequest("/api/v1/verification/inquiries?handoff=true", uuid.New()))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if strings.Contains(recorder.Body.String(), "persona request failed") {
				t.Fatalf("upstream error text leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestStartVerificationHandoffWithoutHandoffStarterIsNotFound(t *testing.T) {
	handler := NewHTTPHandler(&stubStarter{}, &stubProcessor{})
	recorder := httptest.NewRecorder()
	handler.StartVerification(recorder, handoffRequest("/api/v1/verification/inquiries?handoff=true", uuid.New()))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestStartVerificationHandoffRequiresAuthentication(t *testing.T) {
	starter := &stubHandoffStarter{}
	handler := NewHTTPHandler(starter, &stubProcessor{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/verification/inquiries?handoff=true", nil)
	recorder := httptest.NewRecorder()
	handler.StartVerification(recorder, request)
	if recorder.Code != http.StatusUnauthorized || starter.handoffCalls != 0 {
		t.Fatalf("status = %d calls = %d", recorder.Code, starter.handoffCalls)
	}
}

func TestStartVerificationHandoffFlagOnlyMatchesTrue(t *testing.T) {
	for _, value := range []string{"false", "1", "yes", ""} {
		starter := &stubHandoffStarter{stubStarter: stubStarter{result: StartResult{InquiryID: "inq_1", Status: StatusCreated}}}
		handler := NewHTTPHandler(starter, &stubProcessor{})
		handler.StartVerification(httptest.NewRecorder(), handoffRequest("/api/v1/verification/inquiries?handoff="+value, uuid.New()))
		if starter.handoffCalls != 0 {
			t.Fatalf("handoff=%q must use the embedded path", value)
		}
	}
}
