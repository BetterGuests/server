package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type tokenAuthenticator map[string]uuid.UUID

func (tokens tokenAuthenticator) Authenticate(_ context.Context, authorization string) (uuid.UUID, error) {
	token := strings.TrimPrefix(authorization, "Bearer ")
	if id, known := tokens[token]; known {
		return id, nil
	}
	return uuid.Nil, errors.New("unknown token")
}

type socketFixture struct {
	server  *httptest.Server
	service *RealtimeService
	userID  uuid.UUID
}

func newSocketFixture(t *testing.T) socketFixture {
	t.Helper()
	userID := uuid.New()
	service := NewRealtimeService()
	handler := NewHTTPHandler(service, tokenAuthenticator{"good": userID}, []string{"https://app.example.test"})
	server := httptest.NewServer(http.HandlerFunc(handler.Connect))
	t.Cleanup(server.Close)
	return socketFixture{server: server, service: service, userID: userID}
}

func (fixture socketFixture) dial(t *testing.T, origin string) (*websocket.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(fixture.server.URL, "http"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{origin}},
	})
	if err == nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}
	return conn, err
}

func readFrame(t *testing.T, conn *websocket.Conn) Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var frame Frame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func sendText(t *testing.T, conn *websocket.Conn, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(text)); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestAuthenticatedSocketReceivesPublishedFrames(t *testing.T) {
	fixture := newSocketFixture(t)
	conn, err := fixture.dial(t, "https://app.example.test")
	if err != nil {
		t.Fatal(err)
	}

	sendText(t, conn, `{"type":"auth","token":"good"}`)
	if frame := readFrame(t, conn); frame.Type != FrameReady {
		t.Fatalf("expected ready, got %+v", frame)
	}

	fixture.service.Publish(fixture.userID, "message", map[string]string{"body": "hello"})

	frame := readFrame(t, conn)
	if frame.Type != "message" {
		t.Fatalf("expected message frame, got %+v", frame)
	}
	data, ok := frame.Data.(map[string]any)
	if !ok || data["body"] != "hello" {
		t.Fatalf("unexpected payload %+v", frame.Data)
	}
}

func TestSocketWithBadTokenIsClosedWithoutSubscribing(t *testing.T) {
	fixture := newSocketFixture(t)
	conn, err := fixture.dial(t, "https://app.example.test")
	if err != nil {
		t.Fatal(err)
	}

	sendText(t, conn, `{"type":"auth","token":"stolen"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("expected policy violation close, got %v", err)
	}
	if fixture.service.OpenConnections(fixture.userID) != 0 {
		t.Fatal("a rejected socket must not be subscribed")
	}
}

func TestSocketThatSkipsAuthFrameIsClosed(t *testing.T) {
	fixture := newSocketFixture(t)
	conn, err := fixture.dial(t, "https://app.example.test")
	if err != nil {
		t.Fatal(err)
	}

	sendText(t, conn, `{"type":"subscribe"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("expected policy violation close, got %v", err)
	}
}

func TestSocketFromUnlistedOriginIsRefused(t *testing.T) {
	fixture := newSocketFixture(t)

	if _, err := fixture.dial(t, "https://evil.example.test"); err == nil {
		t.Fatal("a cross-site origin must not be able to open a socket")
	}
}

func TestClosingTheSocketUnsubscribesTheUser(t *testing.T) {
	fixture := newSocketFixture(t)
	conn, err := fixture.dial(t, "https://app.example.test")
	if err != nil {
		t.Fatal(err)
	}
	sendText(t, conn, `{"type":"auth","token":"good"}`)
	readFrame(t, conn)
	if fixture.service.OpenConnections(fixture.userID) != 1 {
		t.Fatal("expected one open connection")
	}

	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for fixture.service.OpenConnections(fixture.userID) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("connection was never released after close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOriginHostsKeepsOnlyParsableOrigins(t *testing.T) {
	got := originHosts([]string{"https://app.example.test", "http://localhost:5173", "not a url", ""})
	want := []string{"app.example.test", "localhost:5173"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("originHosts = %v, want %v", got, want)
	}
}
