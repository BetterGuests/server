package verification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPersonaClientCreateInquiry(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	var gotBody map[string]map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"type":"inquiry","id":"inq_42"},"meta":{"session-token":"sess_abc"}}`))
	}))
	defer server.Close()

	client := NewPersonaClient(Config{APIKey: "persona_sandbox_key", BaseURL: server.URL + "/"}, nil)
	inquiry, err := client.CreateInquiry(context.Background(), "itmpl_sandbox", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if inquiry.ID != "inq_42" || inquiry.SessionToken != "sess_abc" {
		t.Fatalf("inquiry = %+v", inquiry)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/inquiries" || gotAuth != "Bearer persona_sandbox_key" {
		t.Fatalf("request = %s %s auth %q", gotMethod, gotPath, gotAuth)
	}
	attributes, _ := gotBody["data"]["attributes"].(map[string]any)
	if attributes["inquiry-template-id"] != "itmpl_sandbox" || attributes["reference-id"] != "acct-1" {
		t.Fatalf("body attributes = %v", attributes)
	}
	if _, present := gotBody["meta"]["auto-create-account-reference-id"]; present {
		t.Fatalf("body meta must not carry auto-create-account-reference-id alongside attributes.reference-id: %v", gotBody["meta"])
	}
	if gotBody["meta"]["auto-create-inquiry-session"] != true {
		t.Fatalf("body meta = %v", gotBody["meta"])
	}
}

func TestPersonaClientCreateInquiryFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, `{}`},
		{"unauthorized", http.StatusUnauthorized, `{}`},
		{"malformed json", http.StatusCreated, `nope`},
		{"missing session token", http.StatusCreated, `{"data":{"id":"inq_1"},"meta":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
			if _, err := client.CreateInquiry(context.Background(), "t", "r"); !errors.Is(err, ErrPersonaRequest) {
				t.Fatalf("expected ErrPersonaRequest, got %v", err)
			}
		})
	}
}

func TestPersonaClientCreateHandoffInquiryRequestsOneTimeLinkInsteadOfSession(t *testing.T) {
	var gotBody map[string]map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"type":"inquiry","id":"inq_7"},"meta":{"session-token":null,"one-time-link":"https://inquiry.withpersona.com/verify?code=xyz"}}`))
	}))
	defer server.Close()

	client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
	inquiry, err := client.CreateHandoffInquiry(context.Background(), "itmpl_1", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if inquiry.ID != "inq_7" || inquiry.OneTimeLink != "https://inquiry.withpersona.com/verify?code=xyz" {
		t.Fatalf("inquiry = %+v", inquiry)
	}
	if gotBody["meta"]["auto-create-one-time-link"] != true || gotBody["meta"]["auto-create-inquiry-session"] != false {
		t.Fatalf("body meta = %v", gotBody["meta"])
	}
	if _, present := gotBody["meta"]["auto-create-account-reference-id"]; present {
		t.Fatalf("body meta = %v", gotBody["meta"])
	}
}

func TestPersonaClientCreateHandoffInquiryFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"missing one-time link", http.StatusCreated, `{"data":{"id":"inq_1"},"meta":{"one-time-link":""}}`},
		{"null meta", http.StatusCreated, `{"data":{"id":"inq_1"},"meta":null}`},
		{"missing inquiry id", http.StatusCreated, `{"data":{},"meta":{"one-time-link":"https://x"}}`},
		{"malformed json", http.StatusCreated, `{"data":`},
		{"template not deployed", http.StatusBadRequest, `{"errors":[{"title":"Bad request","details":"Inquiry template is not deployed to this environment"}]}`},
		{"rate limited", http.StatusTooManyRequests, `{}`},
		{"unauthorized", http.StatusUnauthorized, `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
			if _, err := client.CreateHandoffInquiry(context.Background(), "t", "r"); !errors.Is(err, ErrPersonaRequest) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPersonaClientHonorsCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
	if _, err := client.CreateHandoffInquiry(ctx, "t", "r"); !errors.Is(err, ErrPersonaRequest) {
		t.Fatalf("err = %v", err)
	}
}

func TestPersonaClientRedactInquiry(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotVersion = r.Header.Get("Authorization"), r.Header.Get("Persona-Version")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
	if err := client.RedactInquiry(context.Background(), "inq_abc123"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v1/inquiries/inq_abc123" || gotAuth != "Bearer k" || gotVersion != personaVersion {
		t.Fatalf("request = %s %s auth %q version %q", gotMethod, gotPath, gotAuth, gotVersion)
	}
}

func TestPersonaClientRedactInquiryTreatsMissingInquiryAsDone(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
		if err := client.RedactInquiry(context.Background(), "inq_gone"); err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		server.Close()
	}
}

func TestPersonaClientRedactInquiryFailures(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"errors":[{"title":"nope"}]}`))
		}))
		client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
		if err := client.RedactInquiry(context.Background(), "inq_abc"); !errors.Is(err, ErrPersonaRequest) {
			t.Fatalf("status %d: err = %v", status, err)
		}
		server.Close()
	}
}

func TestPersonaClientRedactInquiryRejectsMalformedIDsWithoutSendingARequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()
	client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
	for _, id := range []string{"", "abc", "inq_", "inq_../accounts/acct_1", "inq_a/b", "inq_a?x=1", "inq_ a", "INQ_abc", "inq_abc\n"} {
		if err := client.RedactInquiry(context.Background(), id); !errors.Is(err, ErrPersonaRequest) {
			t.Fatalf("id %q: err = %v", id, err)
		}
	}
	if called {
		t.Fatal("a malformed inquiry id must never reach Persona")
	}
}

func TestPersonaClientRedactInquiryHonorsCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
	if err := client.RedactInquiry(ctx, "inq_abc"); !errors.Is(err, ErrPersonaRequest) {
		t.Fatalf("err = %v", err)
	}
}
