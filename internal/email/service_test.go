package email

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendPostsOnceWithAnIdempotencyKey(t *testing.T) {
	var seen *http.Request
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewResendClient(Config{APIKey: "re_test", From: "Better Guests <hello@example.com>"}, server.Client())
	client.endpoint = server.URL

	err := client.Send(context.Background(), Message{To: "delivered@resend.dev", Subject: "Hi", Text: "Body", IdempotencyKey: "notification/1"})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Header.Get("Authorization") != "Bearer re_test" || seen.Header.Get("Idempotency-Key") != "notification/1" || body["subject"] != "Hi" || body["html"] != nil {
		t.Fatalf("request = %v body = %v", seen.Header, body)
	}
}

func TestARejectedEmailIsAnErrorWithoutTheAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"name":"validation_error","message":"private@example.com is not allowed"}`))
	}))
	defer server.Close()
	client := NewResendClient(Config{APIKey: "re_test"}, server.Client())
	client.endpoint = server.URL

	err := client.Send(context.Background(), Message{To: "private@example.com"})
	if !errors.Is(err, ErrRejected) || err.Error() != "the email provider rejected the message: status 403 validation_error" {
		t.Fatalf("err = %v", err)
	}
}

func TestEmailIsOffWithoutAKeyAndStrictWithOne(t *testing.T) {
	complete := map[string]string{"RESEND_API_KEY": "re_x", "EMAIL_FROM": "Better Guests <hello@example.com>", "APP_WEB_URL": "https://app.example.com"}
	if _, enabled, err := configFromLookup(func(string) string { return "" }); enabled || err != nil {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	if _, enabled, err := configFromLookup(func(name string) string { return complete[name] }); !enabled || err != nil {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	for _, missing := range []string{"EMAIL_FROM", "APP_WEB_URL"} {
		_, _, err := configFromLookup(func(name string) string {
			if name == missing {
				return ""
			}
			return complete[name]
		})
		if err == nil {
			t.Fatalf("accepted a config without %s", missing)
		}
	}
}
