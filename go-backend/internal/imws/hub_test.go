package imws

import (
	"encoding/json"
	"testing"
	"time"
)

func startTestHub(t *testing.T) *Hub {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	return hub
}

func testClient(hub *Hub, userID uint, connectionID string, bufferSize int) *Client {
	return newClient(hub, nil, userID, connectionID, bufferSize)
}

func receiveServerMessage(t *testing.T, channel <-chan []byte) ServerMessage {
	t.Helper()
	select {
	case payload := <-channel:
		var message ServerMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("unmarshal server message: %v", err)
		}
		return message
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server message")
		return ServerMessage{}
	}
}

func TestHubRegistersAndDeliversToCurrentClient(t *testing.T) {
	hub := startTestHub(t)
	client := testClient(hub, 7, "connection-1", 4)

	if !hub.Register(client) {
		t.Fatal("Register() = false")
	}
	connected := receiveServerMessage(t, client.send)
	if connected.Type != MsgConnected {
		t.Fatalf("connected type = %q", connected.Type)
	}
	var connectedData ConnectedData
	if err := json.Unmarshal(connected.Data, &connectedData); err != nil {
		t.Fatalf("unmarshal connected data: %v", err)
	}
	if connectedData.UserID != client.UserID || connectedData.ConnectionID != client.ConnectionID {
		t.Fatalf("connected data = %#v", connectedData)
	}

	want := ServerMessage{Type: MsgPong, RequestID: "request-1", Timestamp: 1234}
	if !hub.SendToUser(client.UserID, want) {
		t.Fatal("SendToUser() = false")
	}
	got := receiveServerMessage(t, client.send)
	if got.Type != want.Type || got.RequestID != want.RequestID || got.Timestamp != want.Timestamp {
		t.Fatalf("delivered message = %#v", got)
	}
}

func TestHubReplacesConnectionWithoutOldUnregisterDeletingNew(t *testing.T) {
	hub := startTestHub(t)
	oldClient := testClient(hub, 7, "connection-old", 4)
	newClient := testClient(hub, 7, "connection-new", 4)
	t.Cleanup(oldClient.closeNow)

	if !hub.Register(oldClient) {
		t.Fatal("register old client")
	}
	_ = receiveServerMessage(t, oldClient.send)
	if !hub.Register(newClient) {
		t.Fatal("register new client")
	}
	connected := receiveServerMessage(t, newClient.send)
	if connected.Type != MsgConnected {
		t.Fatalf("new client first message = %q", connected.Type)
	}

	select {
	case command := <-oldClient.closeCommand:
		if command.code != CloseCodeConnectionReplaced || command.reason != CloseReasonConnectionReplaced {
			t.Fatalf("close command = %#v", command)
		}
		var replacement ServerMessage
		if err := json.Unmarshal(command.payload, &replacement); err != nil {
			t.Fatalf("unmarshal replacement: %v", err)
		}
		if replacement.Type != MsgConnectionReplaced {
			t.Fatalf("replacement type = %q", replacement.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("old client did not receive replacement command")
	}

	hub.Unregister(oldClient)
	if !hub.SendToUser(7, ServerMessage{Type: MsgPong, Timestamp: 1234}) {
		t.Fatal("old unregister removed the new client")
	}
	if message := receiveServerMessage(t, newClient.send); message.Type != MsgPong {
		t.Fatalf("new client received %q, want pong", message.Type)
	}
}

func TestHubKeepsPreviousConnectionWhenReplacementCannotRegister(t *testing.T) {
	hub := startTestHub(t)
	oldClient := testClient(hub, 7, "connection-old", 4)
	if !hub.Register(oldClient) {
		t.Fatal("register old client")
	}
	_ = receiveServerMessage(t, oldClient.send)

	failedReplacement := testClient(hub, 7, "connection-failed", 0)
	if hub.Register(failedReplacement) {
		t.Fatal("replacement with unavailable send buffer unexpectedly registered")
	}
	select {
	case <-failedReplacement.done:
	default:
		t.Fatal("failed replacement was not closed")
	}

	if !hub.SendToUser(7, ServerMessage{Type: MsgPong, Timestamp: 1234}) {
		t.Fatal("previous connection was not restored")
	}
	if message := receiveServerMessage(t, oldClient.send); message.Type != MsgPong {
		t.Fatalf("old client received %q, want pong", message.Type)
	}
}

func TestHubKeepsDifferentUsersIndependent(t *testing.T) {
	hub := startTestHub(t)
	first := testClient(hub, 7, "connection-7", 4)
	second := testClient(hub, 8, "connection-8", 4)

	if !hub.Register(first) || !hub.Register(second) {
		t.Fatal("register clients")
	}
	_ = receiveServerMessage(t, first.send)
	_ = receiveServerMessage(t, second.send)

	if !hub.SendToUser(7, ServerMessage{Type: MsgPong, Timestamp: 1234}) {
		t.Fatal("deliver to first user")
	}
	if message := receiveServerMessage(t, first.send); message.Type != MsgPong {
		t.Fatalf("first user received %q", message.Type)
	}
	select {
	case payload := <-second.send:
		t.Fatalf("second user unexpectedly received %s", payload)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubClosesSlowClient(t *testing.T) {
	hub := startTestHub(t)
	client := testClient(hub, 7, "connection-slow", 1)
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	if hub.SendToUser(7, ServerMessage{Type: MsgPong, Timestamp: 1234}) {
		t.Fatal("delivery to full client buffer unexpectedly succeeded")
	}
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("slow client was not closed")
	}
	if hub.SendToUser(7, ServerMessage{Type: MsgPong, Timestamp: 1235}) {
		t.Fatal("removed slow client still received delivery")
	}
}

func TestHubStopIsIdempotentAndClosesClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	client := testClient(hub, 7, "connection-1", 4)
	if !hub.Register(client) {
		t.Fatal("register client")
	}

	hub.Stop()
	hub.Stop()

	select {
	case <-client.done:
	default:
		t.Fatal("client is still open after Stop")
	}
	if hub.Register(testClient(hub, 8, "connection-2", 4)) {
		t.Fatal("Register() succeeded after Stop")
	}
	if hub.SendToUser(7, ServerMessage{Type: MsgPong, Timestamp: 1234}) {
		t.Fatal("SendToUser() succeeded after Stop")
	}
}
