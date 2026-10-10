package ws

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"trpggame/internal/realtime"
)

const testRecvTimeout = 3 * time.Second

// startTestHub 启动一个 Hub 并注册清理。
func startTestHub(t *testing.T) *Hub {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	return hub
}

// registerTestClient 构造客户端并注册到房间，等待订阅确认后返回。
func registerTestClient(t *testing.T, hub *Hub, roomID, userID uint) *Client {
	t.Helper()
	client := NewClient(hub, nil, userID, roomID)
	if !hub.Register(client) {
		t.Fatal("Register() = false")
	}

	subscribed := recvMessage(t, client)
	if subscribed.Type != MsgSubscribed {
		t.Fatalf("first message type = %q, want %q", subscribed.Type, MsgSubscribed)
	}
	var data SubscribedData
	if err := json.Unmarshal(subscribed.Data, &data); err != nil {
		t.Fatalf("unmarshal subscribed data: %v", err)
	}
	if data.RoomID != roomID {
		t.Fatalf("subscribed room_id = %d, want %d", data.RoomID, roomID)
	}
	return client
}

// recvMessage 从客户端发送通道读取一条消息并解码。
func recvMessage(t *testing.T, client *Client) *Message {
	t.Helper()
	select {
	case raw := <-client.Send:
		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal message: %v", err)
		}
		return &msg
	case <-time.After(testRecvTimeout):
		t.Fatal("timed out waiting for message")
		return nil
	}
}

func recvNone(t *testing.T, client *Client) {
	t.Helper()
	select {
	case raw := <-client.Send:
		t.Fatalf("expected no message, got %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBroadcastDeliversToAllClientsWithMonotonicSeq(t *testing.T) {
	hub := startTestHub(t)

	room := uint(1)
	alice := registerTestClient(t, hub, room, 10)
	bob := registerTestClient(t, hub, room, 11)

	for i := 1; i <= 3; i++ {
		hub.BroadcastToRoom(room, MsgSystem, json.RawMessage(`{"n":"`+string(rune('0'+i))+`"}`))

		msgAlice := recvMessage(t, alice)
		msgBob := recvMessage(t, bob)
		if msgAlice.Seq != int64(i) || msgBob.Seq != int64(i) {
			t.Fatalf("broadcast %d seq = (%d, %d), want (%d, %d)", i, msgAlice.Seq, msgBob.Seq, i, i)
		}
		if msgAlice.Type != MsgSystem || msgBob.Type != MsgSystem {
			t.Fatalf("broadcast %d type = (%q, %q), want system", i, msgAlice.Type, msgBob.Type)
		}
	}
}

func TestSendToUserOnlyDeliversToTarget(t *testing.T) {
	hub := startTestHub(t)

	room := uint(1)
	alice := registerTestClient(t, hub, room, 10)
	bob := registerTestClient(t, hub, room, 11)
	carol := registerTestClient(t, hub, room, 12)

	hub.SendToUser(room, bob.UserID, MsgStatusUpdate, json.RawMessage(`{"player_id":11}`))

	if msg := recvMessage(t, bob); msg.UserID != 0 || msg.Seq != 1 || msg.Type != MsgStatusUpdate {
		t.Fatalf("target message = %#v, want status_update seq=1", msg)
	}
	recvNone(t, alice)
	recvNone(t, carol)
}

func TestSendToUnknownUserIsSilentlyDropped(t *testing.T) {
	hub := startTestHub(t)

	room := uint(1)
	alice := registerTestClient(t, hub, room, 10)
	hub.SendToUser(room, 99, MsgSystem, json.RawMessage(`{}`))
	recvNone(t, alice)
}

func TestSyncReplaysMissingMessagesSinceSeq(t *testing.T) {
	hub := startTestHub(t)

	room := uint(1)
	client := registerTestClient(t, hub, room, 10)

	hub.BroadcastToRoom(room, MsgSystem, json.RawMessage(`{"n":1}`))
	hub.BroadcastToRoom(room, MsgSystem, json.RawMessage(`{"n":2}`))
	hub.BroadcastToRoom(room, MsgSystem, json.RawMessage(`{"n":3}`))
	recvMessage(t, client) // seq=1
	recvMessage(t, client) // seq=2
	recvMessage(t, client) // seq=3

	req, _ := json.Marshal(SyncRequestData{SinceSeq: 2})
	hub.HandleInbound(client, &Message{Type: MsgSync, Data: req})

	sync := recvMessage(t, client)
	if sync.Type != MsgSyncBatch {
		t.Fatalf("sync response type = %q, want sync_batch", sync.Type)
	}
	var batch SyncBatchData
	if err := json.Unmarshal(sync.Data, &batch); err != nil {
		t.Fatalf("unmarshal sync_batch: %v", err)
	}
	if batch.NextSeq != 3 {
		t.Fatalf("next_seq = %d, want 3", batch.NextSeq)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].Seq != 3 {
		t.Fatalf("replayed messages = %#v, want [seq=3]", batch.Messages)
	}
}

func TestUnknownInboundMessageReturnsError(t *testing.T) {
	hub := startTestHub(t)

	room := uint(1)
	client := registerTestClient(t, hub, room, 10)
	hub.HandleInbound(client, &Message{Type: MsgChatMessage})

	errMsg := recvMessage(t, client)
	if errMsg.Type != MsgError {
		t.Fatalf("response type = %q, want error", errMsg.Type)
	}
	var data ErrorData
	if err := json.Unmarshal(errMsg.Data, &data); err != nil {
		t.Fatalf("unmarshal error data: %v", err)
	}
	if data.Code != 1505 {
		t.Fatalf("error code = %d, want 1505", data.Code)
	}
}

func TestGameActionInboundDispatchesAuthenticatedRoomData(t *testing.T) {
	hub := startTestHub(t)
	client := registerTestClient(t, hub, 41, 7)
	dispatched := make(chan GameActionData, 1)
	hub.SetGameActionHandler(func(ctx context.Context, got *Client, request GameActionData) {
		if ctx == nil || got != client {
			t.Errorf("handler context/client = (%v, %p), want client %p", ctx, got, client)
		}
		dispatched <- request
	})

	hub.HandleInbound(client, &Message{
		Type: MsgGameAction,
		Data: mustJSON(GameActionData{
			RequestID:    "550e8400-e29b-41d4-a716-446655440000",
			ExpectedTurn: intPointer(3),
			ActionText:   "调查书房",
		}),
	})

	select {
	case request := <-dispatched:
		if request.RequestID == "" || request.ExpectedTurn == nil || *request.ExpectedTurn != 3 || request.ActionText != "调查书房" {
			t.Fatalf("dispatched request = %#v", request)
		}
	case <-time.After(testRecvTimeout):
		t.Fatal("game action handler was not called")
	}
}

func intPointer(value int) *int { return &value }

func TestRegisterSkippedForClosedClient(t *testing.T) {
	hub := startTestHub(t)

	client := NewClient(hub, nil, 7, 41)
	client.closeNow()
	if hub.Register(client) {
		t.Fatal("closed client must not be registered")
	}

	hub.mu.RLock()
	defer hub.mu.RUnlock()
	if _, ok := hub.rooms[41]; ok {
		t.Fatal("closed client must not be registered into the room")
	}
}

func TestRegisterThenUnregisterCleansUpRoom(t *testing.T) {
	hub := startTestHub(t)

	client := NewClient(hub, nil, 7, 41)
	if !hub.Register(client) {
		t.Fatal("Register() = false")
	}
	_ = recvMessage(t, client) // 等待订阅确认，确保 register 已处理
	hub.Unregister(client)

	deadline := time.After(testRecvTimeout)
	for {
		hub.mu.RLock()
		_, ok := hub.rooms[41]
		hub.mu.RUnlock()
		if !ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("room 41 was not cleaned up after last client unregistered")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestRegisterAssignsCanonicalConnectionID(t *testing.T) {
	hub := startTestHub(t)
	client := registerTestClient(t, hub, 41, 7)

	parsed, err := uuid.Parse(client.ConnectionID)
	if err != nil || parsed.String() != client.ConnectionID {
		t.Fatalf("connection ID = %q, want canonical UUID", client.ConnectionID)
	}
}

func TestLatestConnectionReplacesOldWithoutDelayedUnregisterRemovingNew(t *testing.T) {
	hub := startTestHub(t)
	oldClient := registerTestClient(t, hub, 41, 7)
	newClient := registerTestClient(t, hub, 41, 7)

	select {
	case command := <-oldClient.closeCommand:
		if command.code != realtime.CloseCodeConnectionReplaced ||
			command.reason != realtime.CloseReasonConnectionReplaced {
			t.Fatalf("replacement close command = %#v", command)
		}
	case <-time.After(testRecvTimeout):
		t.Fatal("old connection did not receive replacement close command")
	}

	hub.Unregister(oldClient)
	hub.SendToUser(41, 7, MsgSystem, json.RawMessage(`{"current":true}`))
	if message := recvMessage(t, newClient); message.Type != MsgSystem || message.Seq != 1 {
		t.Fatalf("new client message = %#v, want system seq=1", message)
	}
	recvNone(t, oldClient)
}

func TestReplacedConnectionCannotSubmitActionOrSync(t *testing.T) {
	hub := startTestHub(t)
	oldClient := registerTestClient(t, hub, 41, 7)
	newClient := registerTestClient(t, hub, 41, 7)
	<-oldClient.closeCommand

	dispatched := make(chan *Client, 1)
	hub.SetGameActionHandler(func(_ context.Context, client *Client, _ GameActionData) {
		dispatched <- client
	})
	action := &Message{
		Type: MsgGameAction,
		Data: mustJSON(GameActionData{
			RequestID:    "550e8400-e29b-41d4-a716-446655440000",
			ExpectedTurn: intPointer(3),
			ActionText:   "调查书房",
		}),
	}
	hub.HandleInbound(oldClient, action)
	select {
	case client := <-dispatched:
		t.Fatalf("replaced client dispatched action through %p", client)
	case <-time.After(50 * time.Millisecond):
	}

	hub.HandleInbound(oldClient, &Message{Type: MsgSync})
	recvNone(t, oldClient)

	hub.HandleInbound(newClient, action)
	select {
	case client := <-dispatched:
		if client != newClient {
			t.Fatalf("action client = %p, want %p", client, newClient)
		}
	case <-time.After(testRecvTimeout):
		t.Fatal("current client action was not dispatched")
	}
}

func TestHubStopIsConcurrentSafeAndPublicMethodsReturn(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	client := registerTestClient(t, hub, 41, 7)

	const callers = 20
	var waitGroup sync.WaitGroup
	waitGroup.Add(callers)
	for range callers {
		go func() {
			defer waitGroup.Done()
			hub.Stop()
		}()
	}
	stopped := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(testRecvTimeout):
		t.Fatal("concurrent Stop calls did not return")
	}

	select {
	case <-client.done:
	default:
		t.Fatal("client remained open after Stop")
	}
	if hub.Register(NewClient(hub, nil, 8, 41)) {
		t.Fatal("Register() succeeded after Stop")
	}

	returned := make(chan struct{})
	go func() {
		hub.BroadcastToRoom(41, MsgSystem, json.RawMessage(`{}`))
		hub.SendToUser(41, 7, MsgSystem, json.RawMessage(`{}`))
		hub.Unregister(client)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("public methods blocked after Stop")
	}
}

func TestHubStopRacingWithRegisterDeliverAndUnregisterDoesNotBlock(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	const clients = 64
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	waitGroup.Add(clients)
	for index := range clients {
		go func() {
			defer waitGroup.Done()
			<-start
			client := NewClient(hub, nil, uint(index+1), 41)
			if hub.Register(client) {
				hub.SendToUser(41, client.UserID, MsgSystem, json.RawMessage(`{}`))
				hub.Unregister(client)
			}
		}()
	}
	close(start)
	go hub.Stop()

	finished := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(testRecvTimeout):
		t.Fatal("Hub methods blocked while racing with Stop")
	}
	hub.Stop()
}

func TestAppendRecentTrimsToCapacity(t *testing.T) {
	recent := []Message(nil)
	for i := 1; i <= recentCapacity+10; i++ {
		recent = appendRecent(recent, Message{Seq: int64(i)}, recentCapacity)
	}
	if len(recent) != recentCapacity {
		t.Fatalf("len(recent) = %d, want %d", len(recent), recentCapacity)
	}
	if recent[0].Seq != 11 {
		t.Fatalf("oldest kept seq = %d, want 11", recent[0].Seq)
	}
	if recent[len(recent)-1].Seq != int64(recentCapacity+10) {
		t.Fatalf("newest seq = %d, want %d", recent[len(recent)-1].Seq, recentCapacity+10)
	}
}

func TestRecentSinceFiltersBySeq(t *testing.T) {
	recent := []Message{{Seq: 1}, {Seq: 2}, {Seq: 3}}
	got := recentSince(recent, 1)
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("recentSince = %#v, want seq [2 3]", got)
	}
}
