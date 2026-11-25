package realtime

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

type RealtimeService struct {
	mu          sync.Mutex
	connections map[uuid.UUID]map[*Connection]struct{}
}

func NewRealtimeService() *RealtimeService {
	return &RealtimeService{connections: make(map[uuid.UUID]map[*Connection]struct{})}
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
	service.mu.Lock()
	defer service.mu.Unlock()
	service.drop(connection)
}

func (service *RealtimeService) Publish(userID uuid.UUID, kind string, payload any) {
	encoded, err := json.Marshal(Frame{Type: kind, Data: payload})
	if err != nil {
		slog.Error("Encode realtime frame", "kind", kind, "error", err)
		return
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	for connection := range service.connections[userID] {
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
