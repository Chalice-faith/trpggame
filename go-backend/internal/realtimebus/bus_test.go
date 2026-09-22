package realtimebus

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestBus(t *testing.T, server *miniredis.Miniredis, instanceID string, capacity int) *Bus {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	bus, err := New(client, Options{
		InstanceID: instanceID, RoomLogCapacity: capacity,
		RoomLogTTL: time.Hour, ConnectionTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(bus.Stop)
	return bus
}

func TestRoomEventsUseGlobalSequenceAndCrossInstanceDelivery(t *testing.T) {
	server := miniredis.RunT(t)
	first := newTestBus(t, server, "instance-a", 3)
	second := newTestBus(t, server, "instance-b", 3)
	received := make(chan RoomEvent, 2)
	second.SetRoomHandler(func(event RoomEvent) { received <- event })

	for index := 1; index <= 2; index++ {
		message, _ := json.Marshal(map[string]any{"type": "system", "value": index})
		publisher := first
		if index == 2 {
			publisher = second
		}
		seq, err := publisher.PublishRoom(context.Background(), RoomEvent{RoomID: 7, Message: message})
		if err != nil {
			t.Fatalf("PublishRoom(%d) error = %v", index, err)
		}
		if seq != int64(index) {
			t.Fatalf("PublishRoom(%d) seq = %d", index, seq)
		}
	}

	for wantSeq := int64(1); wantSeq <= 2; wantSeq++ {
		select {
		case event := <-received:
			if event.Seq != wantSeq || event.RoomID != 7 {
				t.Fatalf("event = %+v, want seq %d room 7", event, wantSeq)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for cross-instance room event")
		}
	}

	replay, err := second.ReplayRoom(context.Background(), 7, 11, 0)
	if err != nil {
		t.Fatalf("ReplayRoom() error = %v", err)
	}
	if replay.SnapshotRequired || replay.NextSeq != 2 || len(replay.Events) != 2 {
		t.Fatalf("ReplayRoom() = %+v", replay)
	}
}

func TestReplayRequiresSnapshotAfterBoundedLogGapAndFiltersTargetedEvents(t *testing.T) {
	server := miniredis.RunT(t)
	bus := newTestBus(t, server, "instance-a", 2)
	for index := 1; index <= 3; index++ {
		message, _ := json.Marshal(map[string]any{"index": index})
		userID := uint(0)
		if index == 2 {
			userID = 9
		}
		if _, err := bus.PublishRoom(context.Background(), RoomEvent{RoomID: 4, UserID: userID, Message: message}); err != nil {
			t.Fatalf("PublishRoom() error = %v", err)
		}
	}

	stale, err := bus.ReplayRoom(context.Background(), 4, 9, 0)
	if err != nil {
		t.Fatalf("ReplayRoom(stale) error = %v", err)
	}
	if !stale.SnapshotRequired || stale.NextSeq != 3 || len(stale.Events) != 0 {
		t.Fatalf("stale replay = %+v", stale)
	}

	otherUser, err := bus.ReplayRoom(context.Background(), 4, 10, 1)
	if err != nil {
		t.Fatalf("ReplayRoom(other user) error = %v", err)
	}
	if otherUser.SnapshotRequired || len(otherUser.Events) != 1 || otherUser.Events[0].Seq != 3 {
		t.Fatalf("other user replay = %+v", otherUser)
	}
	future, err := bus.ReplayRoom(context.Background(), 4, 10, 4)
	if err != nil || !future.SnapshotRequired || future.NextSeq != 3 {
		t.Fatalf("future replay = %+v, error = %v", future, err)
	}
}

func TestUserEventsAndConnectionReplacementCrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	first := newTestBus(t, server, "instance-a", 3)
	second := newTestBus(t, server, "instance-b", 3)
	users := make(chan UserEvent, 1)
	controls := make(chan ControlEvent, 1)
	second.SetUserHandler(func(event UserEvent) { users <- event })
	first.SetControlHandler(func(event ControlEvent) { controls <- event })

	if err := first.ClaimConnection(context.Background(), ScopeIM, 0, 8, "old"); err != nil {
		t.Fatalf("first ClaimConnection() error = %v", err)
	}
	if err := second.ClaimConnection(context.Background(), ScopeIM, 0, 8, "new"); err != nil {
		t.Fatalf("second ClaimConnection() error = %v", err)
	}
	select {
	case event := <-controls:
		if event.Scope != ScopeIM || event.UserID != 8 || event.ConnectionID != "old" {
			t.Fatalf("control event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement control")
	}

	refreshed, err := first.RefreshConnection(context.Background(), ScopeIM, 0, 8, "old")
	if err != nil || refreshed {
		t.Fatalf("old RefreshConnection() = %v, %v", refreshed, err)
	}
	released, err := first.ReleaseConnection(context.Background(), ScopeIM, 0, 8, "old")
	if err != nil || released {
		t.Fatalf("old ReleaseConnection() = %v, %v", released, err)
	}

	if err := first.PublishUser(context.Background(), UserEvent{UserID: 8, Payload: json.RawMessage(`{"type":"presence"}`)}); err != nil {
		t.Fatalf("PublishUser() error = %v", err)
	}
	select {
	case event := <-users:
		if event.UserID != 8 || string(event.Payload) != `{"type":"presence"}` {
			t.Fatalf("user event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for user event")
	}
}

func TestBusReportsRedisFailures(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), DialTimeout: 50 * time.Millisecond})
	defer client.Close()
	bus, err := New(client, Options{InstanceID: "failure-test"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := bus.PublishRoom(ctx, RoomEvent{RoomID: 1, Message: json.RawMessage(`{"type":"system"}`)}); err == nil {
		t.Fatal("PublishRoom() error = nil after Redis failure")
	}
	if err := bus.PublishUser(ctx, UserEvent{UserID: 1, Payload: json.RawMessage(`{"type":"presence"}`)}); err == nil {
		t.Fatal("PublishUser() error = nil after Redis failure")
	}
	if err := bus.ClaimConnection(ctx, ScopeIM, 0, 1, "connection"); err == nil {
		t.Fatal("ClaimConnection() error = nil after Redis failure")
	}
}

func TestBusResubscribesAfterRedisRestart(t *testing.T) {
	server := miniredis.RunT(t)
	bus := newTestBus(t, server, "resubscribe", 3)
	received := make(chan UserEvent, 1)
	bus.SetUserHandler(func(event UserEvent) {
		select {
		case received <- event:
		default:
		}
	})

	server.Close()
	time.Sleep(50 * time.Millisecond)
	if err := server.Restart(); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := bus.PublishUser(ctx, UserEvent{UserID: 3, Payload: json.RawMessage(`{"type":"presence"}`)})
		cancel()
		if err == nil {
			select {
			case event := <-received:
				if event.UserID != 3 {
					t.Fatalf("resubscribed event = %+v", event)
				}
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("subscriber did not recover after Redis restart")
}
