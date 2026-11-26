package screening

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

func serviceAnswering(t *testing.T, status int, stopReason, text string) (*ScreeningService, *string) {
	t.Helper()
	sent := new(string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*sent = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		content := []map[string]string{}
		if text != "" {
			content = append(content, map[string]string{"type": "text", "text": text})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": string(screeningModel),
			"stop_reason": stopReason, "content": content,
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	t.Cleanup(server.Close)
	return NewScreeningService(Config{APIKey: "test-key"}, option.WithBaseURL(server.URL), option.WithMaxRetries(0)), sent
}

func TestScreenReadsTheVerdict(t *testing.T) {
	cases := []struct {
		name, stopReason, text string
		want                   listings.PhotoRefusal
	}{
		{"a room is accepted", "end_turn", `{"category":"property"}`, ""},
		{"nudity is refused", "end_turn", `{"category":"explicit"}`, listings.RefusedExplicit},
		{"a meme is refused", "end_turn", `{"category":"unrelated"}`, listings.RefusedUnrelated},
		{"a safety refusal refuses the photo", "refusal", "", listings.RefusedUnsafe},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, _ := serviceAnswering(t, http.StatusOK, test.stopReason, test.text)
			refusal, err := service.Screen(context.Background(), []byte("photo"), "image/jpeg")
			if err != nil || refusal != test.want {
				t.Fatalf("refusal = %q, error = %v", refusal, err)
			}
		})
	}
}

func TestScreenNeverAcceptsAPhotoWithoutAVerdict(t *testing.T) {
	cases := []struct {
		name             string
		status           int
		stopReason, text string
	}{
		{"provider error", http.StatusInternalServerError, "end_turn", ""},
		{"empty answer", http.StatusOK, "end_turn", ""},
		{"cut off answer", http.StatusOK, "max_tokens", `{"category":"property"}`},
		{"unknown category", http.StatusOK, "end_turn", `{"category":"fine"}`},
		{"not json", http.StatusOK, "end_turn", "property"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, _ := serviceAnswering(t, test.status, test.stopReason, test.text)
			if _, err := service.Screen(context.Background(), []byte("photo"), "image/jpeg"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestScreenSendsThePhotoAndConstrainsTheAnswer(t *testing.T) {
	service, sent := serviceAnswering(t, http.StatusOK, "end_turn", `{"category":"property"}`)
	if _, err := service.Screen(context.Background(), []byte("photo"), "image/png"); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"media_type":"image/png"`, `"data":"cGhvdG8="`, `"json_schema"`, `"fallbacks":"default"`, string(screeningModel)} {
		if !strings.Contains(*sent, part) {
			t.Fatalf("request is missing %s: %s", part, *sent)
		}
	}
}

func TestScreeningIsOffWithoutAKey(t *testing.T) {
	if _, enabled := ConfigFromLookup(func(string) string { return "  " }); enabled {
		t.Fatal("a blank key must not enable screening")
	}
}

func TestAnUnscopedKeyNamesItsWorkspace(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get(workspaceHeader)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	config, _ := ConfigFromLookup(func(name string) string {
		return map[string]string{"ANTHROPIC_API_KEY": "key", "ANTHROPIC_WORKSPACE_ID": " wrkspc_1 "}[name]
	})
	service := NewScreeningService(config, option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	_, _ = service.Screen(context.Background(), []byte("photo"), "image/jpeg")
	if got := <-received; got != "wrkspc_1" {
		t.Fatalf("workspace header = %q", got)
	}
}
