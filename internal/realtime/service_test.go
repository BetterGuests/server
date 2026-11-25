package realtime

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func receive(t *testing.T, connection *Connection) Frame {
	t.Helper()
	select {
	case raw, open := <-connection.outbound:
		if !open {
			t.Fatal("connection was closed")
		}
		var frame Frame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	default:
		t.Fatal("expected a queued frame")
		return Frame{}
	}
}

func TestPublishReachesEveryConnectionOfThatUserOnly(t *testing.T) {
	service := NewRealtimeService()
	alice, bob := uuid.New(), uuid.New()
	phone, _ := service.Subscribe(alice)
	laptop, _ := service.Subscribe(alice)
	other, _ := service.Subscribe(bob)

	service.Publish(alice, "message", map[string]string{"body": "hi"})

	for name, connection := range map[string]*Connection{"phone": phone, "laptop": laptop} {
		if frame := receive(t, connection); frame.Type != "message" {
			t.Fatalf("%s got frame %q", name, frame.Type)
		}
	}
	select {
	case <-other.outbound:
		t.Fatal("another user's connection must not receive the frame")
	default:
	}
}

func TestPublishToAnOfflineUserIsANoOp(t *testing.T) {
	NewRealtimeService().Publish(uuid.New(), "message", nil)
}

func TestUnsubscribeStopsDeliveryAndForgetsTheUser(t *testing.T) {
	service := NewRealtimeService()
	userID := uuid.New()
	connection, _ := service.Subscribe(userID)

	service.Unsubscribe(connection)
	service.Unsubscribe(connection)
	service.Publish(userID, "message", nil)

	if _, open := <-connection.outbound; open {
		t.Fatal("outbound channel must be closed after unsubscribe")
	}
	if service.OpenConnections(userID) != 0 {
		t.Fatal("user must have no open connections")
	}
}

func TestSlowConnectionIsDroppedInsteadOfBlockingPublishers(t *testing.T) {
	service := NewRealtimeService()
	userID := uuid.New()
	slow, _ := service.Subscribe(userID)
	healthy, _ := service.Subscribe(userID)

	for range outboundBuffer {
		service.Publish(userID, "message", nil)
		<-healthy.outbound
	}
	service.Publish(userID, "message", nil)

	if got := service.OpenConnections(userID); got != 1 {
		t.Fatalf("open connections = %d, want only the healthy one", got)
	}
	drained := 0
	for range slow.outbound {
		drained++
	}
	if drained != outboundBuffer {
		t.Fatalf("slow connection kept %d buffered frames, want %d", drained, outboundBuffer)
	}
	if frame := receive(t, healthy); frame.Type != "message" {
		t.Fatalf("healthy connection missed the overflow frame: %+v", frame)
	}
}

func TestSubscribeRefusesTooManyConnectionsForOneUser(t *testing.T) {
	service := NewRealtimeService()
	userID := uuid.New()
	for range maxConnectionsPerUser {
		if _, err := service.Subscribe(userID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Subscribe(userID); err != ErrTooManyConnections {
		t.Fatalf("expected ErrTooManyConnections, got %v", err)
	}
	if _, err := service.Subscribe(uuid.New()); err != nil {
		t.Fatalf("another user must be unaffected: %v", err)
	}
}
