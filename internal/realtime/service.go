package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

type RealtimeService struct {
	mu            sync.Mutex
	connections   map[uuid.UUID]map[*Connection]struct{}
	focusListener FocusListener
}

func NewRealtimeService() *RealtimeService {
	return &RealtimeService{connections: make(map[uuid.UUID]map[*Connection]struct{})}
}

func (service *RealtimeService) WithFocusListener(listener FocusListener) *RealtimeService {
	service.focusListener = listener
	return service
}

func (service *RealtimeService) Subscribe(userID uuid.UUID) (*Connection, error) {
	service.mu.Lock()
	defer service.mu.Unlock()

	open := service.connections[userID]
	if len(open) >= maxConnectionsPerUser {
		return nil, ErrTooManyConnections
	}
	if open == nil {
		open = make(map[*Connection]struct{})
		service.connections[userID] = open
	}
	connection := &Connection{userID: userID, outbound: make(chan []byte, outboundBuffer)}
	open[connection] = struct{}{}
	return connection, nil
}

func (service *RealtimeService) Unsubscribe(connection *Connection) {
	service.SetFocus(connection, "")
	service.mu.Lock()
	defer service.mu.Unlock()
	service.drop(connection)
}

func (service *RealtimeService) SetFocus(connection *Connection, topic string) {
	service.mu.Lock()
	previous := connection.focus
	if previous == topic {
		service.mu.Unlock()
		return
	}
	connection.focus = topic
	var changes []focusChange
	if previous != "" && service.focusCountLocked(connection.userID, previous) == 0 {
		changes = append(changes, focusChange{userID: connection.userID, topic: previous, present: false})
	}
	if topic != "" && service.focusCountLocked(connection.userID, topic) == 1 {
		changes = append(changes, focusChange{userID: connection.userID, topic: topic, present: true})
	}
	listener := service.focusListener
	service.mu.Unlock()

	if listener == nil {
		return
	}
	for _, change := range changes {
		ctx, cancel := context.WithTimeout(context.Background(), focusNoticeTimeout)
		listener.FocusChanged(ctx, change.userID, change.topic, change.present)
		cancel()
	}
}

func (service *RealtimeService) Focused(userID uuid.UUID, topic string) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	return topic != "" && service.focusCountLocked(userID, topic) > 0
}

func (service *RealtimeService) PublishFocused(userID uuid.UUID, topic, kind string, payload any) {
	if topic == "" {
		return
	}
	service.publishWhere(userID, kind, payload, func(connection *Connection) bool {
		return connection.focus == topic
	})
}

func (service *RealtimeService) focusCountLocked(userID uuid.UUID, topic string) int {
	count := 0
	for connection := range service.connections[userID] {
		if connection.focus == topic {
			count++
		}
	}
	return count
}

func (service *RealtimeService) Publish(userID uuid.UUID, kind string, payload any) {
	service.publishWhere(userID, kind, payload, func(*Connection) bool { return true })
}

func (service *RealtimeService) publishWhere(userID uuid.UUID, kind string, payload any, wanted func(*Connection) bool) {
	encoded, err := json.Marshal(Frame{Type: kind, Data: payload})
	if err != nil {
		slog.Error("Encode realtime frame", "kind", kind, "error", err)
		return
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	for connection := range service.connections[userID] {
		if !wanted(connection) {
			continue
		}
		select {
		case connection.outbound <- encoded:
		default:
			slog.Warn("Dropping slow realtime connection", "kind", kind)
			service.drop(connection)
		}
	}
}

func (service *RealtimeService) OpenConnections(userID uuid.UUID) int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return len(service.connections[userID])
}

func (service *RealtimeService) Disconnect(userID uuid.UUID) {
	service.mu.Lock()
	defer service.mu.Unlock()
	for connection := range service.connections[userID] {
		service.drop(connection)
	}
}

func (service *RealtimeService) drop(connection *Connection) {
	open, subscribed := service.connections[connection.userID]
	if !subscribed {
		return
	}
	if _, present := open[connection]; !present {
		return
	}
	delete(open, connection)
	close(connection.outbound)
	if len(open) == 0 {
		delete(service.connections, connection.userID)
	}
}
