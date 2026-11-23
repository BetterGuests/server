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
	if gotBody["meta"]["auto-create-inquiry-session"] != true || gotBody["meta"]["auto-create-account-reference-id"] != "acct-1" {
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
