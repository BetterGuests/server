package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type HTTPHandler struct {
	service        *RealtimeService
	authenticator  Authenticator
	originPatterns []string
	receivers      map[string]Receiver
	now            func() time.Time
}

func NewHTTPHandler(service *RealtimeService, authenticator Authenticator, allowedOrigins []string) *HTTPHandler {
	return &HTTPHandler{
		service:        service,
		authenticator:  authenticator,
		originPatterns: originHosts(allowedOrigins),
		receivers:      map[string]Receiver{},
		now:            time.Now,
	}
}

func (handler *HTTPHandler) WithReceiver(kind string, receiver Receiver) *HTTPHandler {
	handler.receivers[kind] = receiver
	return handler
}

func originHosts(origins []string) []string {
	hosts := make([]string, 0, len(origins))
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" {
			continue
		}
		hosts = append(hosts, parsed.Host)
	}
	return hosts
}

func (handler *HTTPHandler) Connect(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: handler.originPatterns})
	if err != nil {
		slog.Warn("Realtime upgrade rejected", "error", err, "remote_addr", r.RemoteAddr)
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxInboundFrameBytes)

	ctx, cancel := context.WithTimeout(r.Context(), maxConnectionAge)
	defer cancel()

	userID, err := handler.authenticate(ctx, conn)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "authentication required")
		return
	}

	connection, err := handler.service.Subscribe(userID)
	if err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, err.Error())
		return
	}
	defer handler.service.Unsubscribe(connection)

	if err := writeFrame(ctx, conn, Frame{Type: FrameReady}); err != nil {
		return
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go handler.listen(ctx, stop, conn, connection)
	handler.pump(ctx, conn, connection)
}

func (handler *HTTPHandler) listen(ctx context.Context, stop context.CancelFunc, conn *websocket.Conn, connection *Connection) {
	defer stop()
	budget := inboundBudget{}
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText || !budget.spend(handler.now()) {
			continue
		}
		handler.receive(ctx, connection, data)
	}
}

func (handler *HTTPHandler) receive(ctx context.Context, connection *Connection, data []byte) {
	var frame inboundFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return
	}
	if frame.Type == FrameFocus {
		handler.focus(connection, frame.Data)
		return
	}
	userID := connection.userID
	receiver, known := handler.receivers[frame.Type]
	if !known {
		return
	}
	receiveCtx, cancel := context.WithTimeout(ctx, receiveTimeout)
	defer cancel()
	if err := receiver.Receive(receiveCtx, userID, frame.Data); err != nil {
		slog.Debug("Realtime frame refused", "type", frame.Type, "error", err)
	}
}

func (handler *HTTPHandler) focus(connection *Connection, data json.RawMessage) {
	var frame focusFrame
	if err := json.Unmarshal(data, &frame); err != nil || len(frame.Topic) > maxTopicBytes {
		return
	}
	handler.service.SetFocus(connection, frame.Topic)
}

func (budget *inboundBudget) spend(now time.Time) bool {
	if now.Sub(budget.windowStart) >= inboundWindow {
		budget.windowStart = now
		budget.used = 0
	}
	if budget.used >= maxInboundPerWindow {
		return false
	}
	budget.used++
	return true
}

func (handler *HTTPHandler) authenticate(ctx context.Context, conn *websocket.Conn) (uuid.UUID, error) {
	readCtx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()

	_, data, err := conn.Read(readCtx)
	if err != nil {
		return uuid.Nil, err
	}
	var frame authFrame
	if err := json.Unmarshal(data, &frame); err != nil || frame.Type != FrameAuth || frame.Token == "" {
		return uuid.Nil, errBadAuthFrame
	}
	userID, err := handler.authenticator.Authenticate(readCtx, "Bearer "+frame.Token)
	if err != nil {
		slog.Warn("Realtime authentication rejected", "error", err)
		return uuid.Nil, err
	}
	return userID, nil
}

func (handler *HTTPHandler) pump(ctx context.Context, conn *websocket.Conn, connection *Connection) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		case message, open := <-connection.outbound:
			if !open {
				_ = conn.Close(websocket.StatusTryAgainLater, "reconnect")
				return
			}
			if err := writeText(ctx, conn, message); err != nil {
				return
			}
		case <-ticker.C:
			if err := ping(ctx, conn); err != nil {
				return
			}
		}
	}
}

func ping(ctx context.Context, conn *websocket.Conn) error {
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return conn.Ping(pingCtx)
}

func writeFrame(ctx context.Context, conn *websocket.Conn, frame Frame) error {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return writeText(ctx, conn, encoded)
}

func writeText(ctx context.Context, conn *websocket.Conn, message []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	err := conn.Write(writeCtx, websocket.MessageText, message)
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Debug("Realtime write failed", "error", err)
	}
	return err
}
