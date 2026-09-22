package ws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/realtimebus"
)

func newDistributedGameHub(
	t *testing.T,
	server *miniredis.Miniredis,
	instanceID string,
	capacity int,
) (*Hub, *realtimebus.Bus) {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	bus, err := realtimebus.New(client, realtimebus.Options{InstanceID: instanceID, RoomLogCapacity: capacity})
	if err != nil {
		t.Fatalf("realtimebus.New() error = %v", err)
	}
	hub := NewHub()
	hub.SetRealtimeBus(bus)
	bus.SetRoomHandler(hub.HandleRoomEvent)
	bus.SetControlHandler(hub.HandleControl)
	go hub.Run()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("bus.Start() error = %v", err)
	}
	t.Cleanup(func() {
		bus.Stop()
		hub.Stop()
		_ = client.Close()
	})
	return hub, bus
}

func receiveGameMessage(t *testing.T, client *Client) Message {
	t.Helper()
	select {
	case payload := <-client.Send:
		var message Message
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("decode game message: %v", err)
		}
		return message
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for game message")
		return Message{}
	}
}

func TestDistributedHubsShareRoomSequenceReplayAndDeduplicate(t *testing.T) {
	server := miniredis.RunT(t)
	first, _ := newDistributedGameHub(t, server, "game-a", 0)
	second, _ := newDistributedGameHub(t, server, "game-b", 0)
	alice := NewClient(first, nil, 1, 42)
	bob := NewClient(second, nil, 2, 42)
	if !first.Register(alice) || !second.Register(bob) {
		t.Fatal("distributed Register() failed")
	}
	_ = receiveGameMessage(t, alice)
	_ = receiveGameMessage(t, bob)

	first.BroadcastToRoom(42, MsgSystem, json.RawMessage(`{"text":"hello"}`))
	aliceEvent := receiveGameMessage(t, alice)
	bobEvent := receiveGameMessage(t, bob)
	if aliceEvent.Seq != 1 || bobEvent.Seq != 1 || aliceEvent.Type != MsgSystem || bobEvent.Type != MsgSystem {
		t.Fatalf("room events = %+v / %+v", aliceEvent, bobEvent)
	}

	second.HandleInbound(bob, &Message{Type: MsgSync, Data: json.RawMessage(`{"since_seq":0}`)})
	syncMessage := receiveGameMessage(t, bob)
	var batch SyncBatchData
	if syncMessage.Type != MsgSyncBatch || json.Unmarshal(syncMessage.Data, &batch) != nil ||
		batch.NextSeq != 1 || len(batch.Messages) != 1 || batch.Messages[0].Seq != 1 {
		t.Fatalf("sync message = %+v, batch = %+v", syncMessage, batch)
	}

	encoded, _ := json.Marshal(Message{Type: MsgSystem, RoomID: 42, Data: json.RawMessage(`{"text":"once"}`)})
	duplicate := realtimebus.RoomEvent{RoomID: 42, Seq: 2, Message: encoded}
	second.HandleRoomEvent(duplicate)
	second.HandleRoomEvent(duplicate)
	second.HandleRoomEvent(realtimebus.RoomEvent{RoomID: 42, Seq: 1, Message: encoded})
	if got := receiveGameMessage(t, bob); got.Seq != 2 {
		t.Fatalf("deduplicated event seq = %d", got.Seq)
	}
	select {
	case payload := <-bob.Send:
		t.Fatalf("duplicate or out-of-order event was delivered: %s", payload)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDistributedGameConnectionReplacementTargetsOldConnection(t *testing.T) {
	server := miniredis.RunT(t)
	first, _ := newDistributedGameHub(t, server, "game-a", 0)
	second, _ := newDistributedGameHub(t, server, "game-b", 0)
	oldClient := NewClient(first, nil, 7, 12)
	newClient := NewClient(second, nil, 7, 12)
	if !first.Register(oldClient) {
		t.Fatal("old Register() failed")
	}
	_ = receiveGameMessage(t, oldClient)
	if !second.Register(newClient) {
		t.Fatal("new Register() failed")
	}
	_ = receiveGameMessage(t, newClient)
	select {
	case command := <-oldClient.closeCommand:
		if command.code != 4001 {
			t.Fatalf("old close code = %d", command.code)
		}
	case <-time.After(time.Second):
		t.Fatal("old cross-instance connection was not replaced")
	}
}

func TestDistributedSyncRequiresSnapshotOutsideRetainedWindow(t *testing.T) {
	server := miniredis.RunT(t)
	hub, _ := newDistributedGameHub(t, server, "game-a", 2)
	client := NewClient(hub, nil, 3, 55)
	if !hub.Register(client) {
		t.Fatal("Register() failed")
	}
	_ = receiveGameMessage(t, client)
	for index := 0; index < 3; index++ {
		hub.BroadcastToRoom(55, MsgSystem, json.RawMessage(`{"text":"event"}`))
		_ = receiveGameMessage(t, client)
	}

	hub.HandleInbound(client, &Message{Type: MsgSync, Data: json.RawMessage(`{"since_seq":0}`)})
	response := receiveGameMessage(t, client)
	var data SnapshotRequiredData
	if response.Type != MsgSnapshotRequired || json.Unmarshal(response.Data, &data) != nil || data.NextSeq != 3 {
		t.Fatalf("snapshot response = %+v, data = %+v", response, data)
	}
}
