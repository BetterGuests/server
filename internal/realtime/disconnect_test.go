package realtime

import (
	"testing"

	"github.com/google/uuid"
)

func TestDisconnectClosesEveryConnectionOfThatAccountOnly(t *testing.T) {
	service := NewRealtimeService()
	banned, other := uuid.New(), uuid.New()
	for _, account := range []uuid.UUID{banned, banned, other} {
		if _, err := service.Subscribe(account); err != nil {
			t.Fatal(err)
		}
	}
	service.Disconnect(banned)
	service.Disconnect(banned)
	if service.OpenConnections(banned) != 0 || service.OpenConnections(other) != 1 {
		t.Fatalf("open = %d and %d", service.OpenConnections(banned), service.OpenConnections(other))
	}
}
