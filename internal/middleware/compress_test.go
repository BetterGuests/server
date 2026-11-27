package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var largeJSON = `{"value":"` + strings.Repeat("a", 4*minCompressedBytes) + `"}`

func compressed(t *testing.T, handler http.HandlerFunc, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	Compress(handler).ServeHTTP(response, request)
	return response
}

func respond(status int, contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func gunzip(t *testing.T, body []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(plain)
}

func TestCompressGzipsLargeJSONAndKeepsStatus(t *testing.T) {
	response := compressed(t, respond(http.StatusCreated, "application/json", largeJSON), map[string]string{"Accept-Encoding": "br, gzip"})

	if response.Code != http.StatusCreated || response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d, encoding %q", response.Code, response.Header().Get("Content-Encoding"))
	}
	if response.Body.Len() >= len(largeJSON) || gunzip(t, response.Body.Bytes()) != largeJSON {
		t.Fatalf("body was not a smaller gzip of the original: %d bytes", response.Body.Len())
	}
	if !strings.Contains(strings.Join(response.Header().Values("Vary"), ","), "Accept-Encoding") {
		t.Fatalf("Vary = %v", response.Header().Values("Vary"))
	}
}

func TestCompressHandlesJSONWrittenInSeveralPieces(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, largeJSON)
		_, _ = io.WriteString(w, "\n")
	}

	response := compressed(t, handler, map[string]string{"Accept-Encoding": "gzip"})

	if gunzip(t, response.Body.Bytes()) != largeJSON+"\n" {
		t.Fatal("pieces were lost")
	}
}

func TestCompressLeavesOtherResponsesUntouched(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		headers map[string]string
		status  int
		body    string
	}{
		{"client without gzip", respond(200, "application/json", largeJSON), nil, 200, largeJSON},
		{"client refusing gzip", respond(200, "application/json", largeJSON), map[string]string{"Accept-Encoding": "gzip;q=0, br"}, 200, largeJSON},
		{"small body", respond(404, "application/json", `{"error":"no"}`), map[string]string{"Accept-Encoding": "gzip"}, 404, `{"error":"no"}`},
		{"not JSON", respond(200, "text/plain", largeJSON), map[string]string{"Accept-Encoding": "gzip"}, 200, largeJSON},
		{"no body", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, map[string]string{"Accept-Encoding": "gzip"}, 204, ""},
		{"connection upgrade", respond(200, "application/json", largeJSON), map[string]string{"Accept-Encoding": "gzip", "Upgrade": "websocket"}, 200, largeJSON},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := compressed(t, test.handler, test.headers)

			if response.Code != test.status || response.Body.String() != test.body {
				t.Fatalf("status %d, %d bytes", response.Code, response.Body.Len())
			}
			if got := response.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("Content-Encoding = %q", got)
			}
		})
	}
}

func TestCompressPassesTheOriginalWriterToUpgrades(t *testing.T) {
	response := httptest.NewRecorder()
	var seen http.ResponseWriter
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Upgrade", "websocket")

	Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { seen = w })).ServeHTTP(response, request)

	if seen != http.ResponseWriter(response) {
		t.Fatal("the upgrade lost access to the connection")
	}
}

func TestCompressLetsRecoverAnswerAPanicBeforeAnyBody(t *testing.T) {
	handler := Recover(Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("boom")
	})))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", response.Code)
	}
}
