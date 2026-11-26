package realtime

import (
	"context"
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

type focusRecorder struct {
	changes []focusChange
}

func (recorder *focusRecorder) FocusChanged(_ context.Context, userID uuid.UUID, topic string, present bool) {
	recorder.changes = append(recorder.changes, focusChange{userID: userID, topic: topic, present: present})
}

func TestFocusedPublishOnlyReachesConnectionsWithThatChatOpen(t *testing.T) {
	service := NewRealtimeService()
	userID := uuid.New()
	inChat, _ := service.Subscribe(userID)
	elsewhere, _ := service.Subscribe(userID)
	service.SetFocus(inChat, "conversation:a")
	service.SetFocus(elsewhere, "conversation:b")

	service.PublishFocused(userID, "conversation:a", "typing", map[string]string{"draft": "hi"})

	if frame := receive(t, inChat); frame.Type != "typing" {
		t.Fatalf("got %q", frame.Type)
	}
	select {
	case <-elsewhere.outbound:
		t.Fatal("a connection on another chat must not get the draft")
	default:
	}
}

func TestFocusedPublishWithNoTopicGoesNowhere(t *testing.T) {
	service := NewRealtimeService()
	userID := uuid.New()
	idle, _ := service.Subscribe(userID)

	service.PublishFocused(userID, "", "typing", nil)

	select {
	case <-idle.outbound:
		t.Fatal("a connection with no chat open must not get typing")
	default:
	}
}

func TestArriveAndLeaveAreReportedOncePerPersonAcrossTabs(t *testing.T) {
	recorder := &focusRecorder{}
	service := NewRealtimeService().WithFocusListener(recorder)
	userID := uuid.New()
	firstTab, _ := service.Subscribe(userID)
	secondTab, _ := service.Subscribe(userID)

	service.SetFocus(firstTab, "conversation:a")
	service.SetFocus(secondTab, "conversation:a")
	service.SetFocus(firstTab, "")
	if len(recorder.changes) != 1 || !recorder.changes[0].present {
		t.Fatalf("while one tab still has the chat open, only the arrival counts: %+v", recorder.changes)
	}

	service.SetFocus(secondTab, "")
	if len(recorder.changes) != 2 || recorder.changes[1].present || recorder.changes[1].topic != "conversation:a" {
		t.Fatalf("closing the last tab must report leaving: %+v", recorder.changes)
	}
	if service.Focused(userID, "conversation:a") {
		t.Fatal("nobody has the chat open any more")
	}
}

func TestDisconnectingCountsAsLeavingTheChat(t *testing.T) {
	recorder := &focusRecorder{}
	service := NewRealtimeService().WithFocusListener(recorder)
	userID := uuid.New()
	connection, _ := service.Subscribe(userID)
	service.SetFocus(connection, "conversation:a")

	service.Unsubscribe(connection)

	last := recorder.changes[len(recorder.changes)-1]
	if last.present || last.topic != "conversation:a" {
		t.Fatalf("last change = %+v", last)
	}
}

func TestSwitchingChatsLeavesOneAndArrivesInTheOther(t *testing.T) {
	recorder := &focusRecorder{}
	service := NewRealtimeService().WithFocusListener(recorder)
	connection, _ := service.Subscribe(uuid.New())
	service.SetFocus(connection, "conversation:a")

	service.SetFocus(connection, "conversation:b")

	if len(recorder.changes) != 3 ||
		recorder.changes[1] != (focusChange{userID: connection.userID, topic: "conversation:a", present: false}) ||
		recorder.changes[2] != (focusChange{userID: connection.userID, topic: "conversation:b", present: true}) {
		t.Fatalf("changes = %+v", recorder.changes)
	}
}
