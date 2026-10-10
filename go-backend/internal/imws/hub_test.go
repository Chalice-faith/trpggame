package imws

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

const imHubTestTimeout = 5 * time.Second

type observedPresenceEvent struct {
	kind         string
	userID       uint
	connectionID string
}

type recordingPresenceObserver struct{ events chan observedPresenceEvent }

func newRecordingPresenceObserver() *recordingPresenceObserver {
	return &recordingPresenceObserver{events: make(chan observedPresenceEvent, 32)}
}

func (o *recordingPresenceObserver) OnConnected(userID uint, connectionID string) {
	o.events <- observedPresenceEvent{kind: "connected", userID: userID, connectionID: connectionID}
}
func (o *recordingPresenceObserver) OnRefreshed(userID uint, connectionID string) {
	o.events <- observedPresenceEvent{kind: "refreshed", userID: userID, connectionID: connectionID}
}
func (o *recordingPresenceObserver) OnDisconnected(userID uint, connectionID string) {
	o.events <- observedPresenceEvent{kind: "disconnected", userID: userID, connectionID: connectionID}
}

func receivePresenceEvent(t *testing.T, observer *recordingPresenceObserver) observedPresenceEvent {
	t.Helper()
	select {
	case event := <-observer.events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for presence event")
		return observedPresenceEvent{}
	}
}

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

func TestHubPresenceObserverOnlySeesCurrentConnectionLifecycle(t *testing.T) {
	observer := newRecordingPresenceObserver()
	hub := NewHub()
	hub.SetPresenceObserver(observer)
	go hub.Run()
	t.Cleanup(hub.Stop)
	oldClient := testClient(hub, 7, "old", 4)
	newClient := testClient(hub, 7, "new", 4)

	if !hub.Register(oldClient) {
		t.Fatal("register old")
	}
	_ = receiveServerMessage(t, oldClient.send)
	if event := receivePresenceEvent(t, observer); event.kind != "connected" || event.connectionID != "old" {
		t.Fatalf("old connected event = %#v", event)
	}
	if !hub.Register(newClient) {
		t.Fatal("register new")
	}
	_ = receiveServerMessage(t, newClient.send)
	if event := receivePresenceEvent(t, observer); event.kind != "connected" || event.connectionID != "new" {
		t.Fatalf("new connected event = %#v", event)
	}

	hub.Unregister(oldClient)
	if hub.RefreshPresence(oldClient) {
		t.Fatal("old connection refreshed presence")
	}
	if !hub.RefreshPresence(newClient) {
		t.Fatal("current connection did not refresh presence")
	}
	if event := receivePresenceEvent(t, observer); event.kind != "refreshed" || event.connectionID != "new" {
		t.Fatalf("refresh event = %#v", event)
	}
	select {
	case event := <-observer.events:
		t.Fatalf("old connection emitted lifecycle event: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}

	hub.Unregister(newClient)
	if event := receivePresenceEvent(t, observer); event.kind != "disconnected" || event.connectionID != "new" {
		t.Fatalf("disconnect event = %#v", event)
	}
}

func TestHubPresenceObserverSeesSlowRemovalAndStop(t *testing.T) {
	observer := newRecordingPresenceObserver()
	hub := NewHub()
	hub.SetPresenceObserver(observer)
	go hub.Run()
	slow := testClient(hub, 7, "slow", 1)
	if !hub.Register(slow) {
		t.Fatal("register slow")
	}
	_ = receivePresenceEvent(t, observer)
	if hub.SendToUser(7, ServerMessage{Type: MsgPong}) {
		t.Fatal("slow delivery succeeded")
	}
	if event := receivePresenceEvent(t, observer); event.kind != "disconnected" || event.connectionID != "slow" {
		t.Fatalf("slow disconnect = %#v", event)
	}

	current := testClient(hub, 8, "current", 4)
	if !hub.Register(current) {
		t.Fatal("register current")
	}
	_ = receiveServerMessage(t, current.send)
	_ = receivePresenceEvent(t, observer)
	hub.Stop()
	if event := receivePresenceEvent(t, observer); event.kind != "disconnected" || event.connectionID != "current" {
		t.Fatalf("stop disconnect = %#v", event)
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
	case <-time.After(imHubTestTimeout):
		t.Fatal("concurrent Stop calls did not return")
	}

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

func TestHubHandlesConcurrentUsersAcrossRepeatedRounds(t *testing.T) {
	hub := startTestHub(t)
	const (
		users  = 64
		rounds = 100
	)

	for round := range rounds {
		errorsFound := make(chan error, users)
		var waitGroup sync.WaitGroup
		waitGroup.Add(users)
		for index := range users {
			go func() {
				defer waitGroup.Done()
				userID := uint(index + 1)
				client := testClient(
					hub,
					userID,
					fmt.Sprintf("connection-%d-%d", round, index),
					4,
				)
				if !hub.Register(client) {
					errorsFound <- fmt.Errorf("round %d user %d: register failed", round, userID)
					return
				}
				connected, err := receiveServerMessageResult(client.send)
				if err != nil || connected.Type != MsgConnected {
					errorsFound <- fmt.Errorf(
						"round %d user %d: connected = %q, err = %v",
						round,
						userID,
						connected.Type,
						err,
					)
					return
				}
				if !hub.SendToUser(userID, ServerMessage{Type: MsgPong}) {
					errorsFound <- fmt.Errorf("round %d user %d: delivery failed", round, userID)
					return
				}
				delivered, err := receiveServerMessageResult(client.send)
				if err != nil || delivered.Type != MsgPong {
					errorsFound <- fmt.Errorf(
						"round %d user %d: delivered = %q, err = %v",
						round,
						userID,
						delivered.Type,
						err,
					)
					return
				}
				hub.Unregister(client)
			}()
		}
		waitGroup.Wait()
		close(errorsFound)
		for err := range errorsFound {
			t.Error(err)
		}
		if t.Failed() {
			return
		}
	}
}

func TestHubSameUserTakeoverStormKeepsLatestConnection(t *testing.T) {
	hub := startTestHub(t)
	const replacements = 50
	clients := make([]*Client, 0, replacements)

	for index := range replacements {
		client := testClient(hub, 7, fmt.Sprintf("connection-%d", index), 4)
		if !hub.Register(client) {
			t.Fatalf("register replacement %d", index)
		}
		if connected := receiveServerMessage(t, client.send); connected.Type != MsgConnected {
			t.Fatalf("replacement %d first message = %q", index, connected.Type)
		}
		if index > 0 {
			select {
			case command := <-clients[index-1].closeCommand:
				if command.code != CloseCodeConnectionReplaced {
					t.Fatalf("replacement %d close code = %d", index, command.code)
				}
			case <-time.After(time.Second):
				t.Fatalf("replacement %d did not close previous connection", index)
			}
		}
		clients = append(clients, client)
	}

	for index := len(clients) - 2; index >= 0; index-- {
		hub.Unregister(clients[index])
		clients[index].closeNow()
	}
	latest := clients[len(clients)-1]
	if !hub.SendToUser(7, ServerMessage{Type: MsgPong, Timestamp: 1234}) {
		t.Fatal("latest connection was lost after out-of-order unregisters")
	}
	if message := receiveServerMessage(t, latest.send); message.Type != MsgPong {
		t.Fatalf("latest connection received %q, want pong", message.Type)
	}
}

func TestHubRegisterDeliverAndUnregisterRaceWithStopDoesNotBlock(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	const callers = 64
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	waitGroup.Add(callers)
	for index := range callers {
		go func() {
			defer waitGroup.Done()
			<-start
			client := testClient(hub, uint(index+1), fmt.Sprintf("connection-%d", index), 4)
			if hub.Register(client) {
				hub.SendToUser(client.UserID, ServerMessage{Type: MsgPong})
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
	case <-time.After(imHubTestTimeout):
		t.Fatal("Hub methods blocked while racing with Stop")
	}
	hub.Stop()
}

func receiveServerMessageResult(channel <-chan []byte) (ServerMessage, error) {
	select {
	case payload := <-channel:
		var message ServerMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			return ServerMessage{}, err
		}
		return message, nil
	case <-time.After(time.Second):
		return ServerMessage{}, fmt.Errorf("timed out waiting for server message")
	}
}
