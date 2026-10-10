package imws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/realtimebus"
)

func newDistributedIMHub(
	t *testing.T,
	server *miniredis.Miniredis,
	instanceID string,
) (*Hub, *realtimebus.Bus) {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	bus, err := realtimebus.New(client, realtimebus.Options{InstanceID: instanceID})
	if err != nil {
		t.Fatalf("realtimebus.New() error = %v", err)
	}
	hub := NewHub()
	hub.SetRealtimeBus(bus)
	bus.SetUserHandler(hub.HandleUserEvent)
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

func receiveIMPayload(t *testing.T, client *Client) []byte {
	t.Helper()
	select {
	case payload := <-client.send:
		return payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for IM payload")
		return nil
	}
}

func TestDistributedIMUserDeliveryAndConnectionReplacement(t *testing.T) {
	server := miniredis.RunT(t)
	first, _ := newDistributedIMHub(t, server, "im-a")
	second, _ := newDistributedIMHub(t, server, "im-b")
	oldClient := NewClient(first, nil, 5)
	if !first.Register(oldClient) {
		t.Fatal("old Register() failed")
	}
	_ = receiveIMPayload(t, oldClient)

	if !second.PublishUserEvent(5, string(MsgPresence), map[string]any{"status": "online"}) {
		t.Fatal("PublishUserEvent() failed")
	}
	payload := receiveIMPayload(t, oldClient)
	var delivered ServerMessage
	if err := json.Unmarshal(payload, &delivered); err != nil || delivered.Type != MsgPresence {
		t.Fatalf("delivered payload = %s, error = %v", payload, err)
	}

	newClient := NewClient(second, nil, 5)
	if !second.Register(newClient) {
		t.Fatal("new Register() failed")
	}
	_ = receiveIMPayload(t, newClient)
	select {
	case command := <-oldClient.closeCommand:
		if command.code != CloseCodeConnectionReplaced || len(command.payload) == 0 {
			t.Fatalf("replacement command = %+v", command)
		}
	case <-time.After(time.Second):
		t.Fatal("old cross-instance IM connection was not replaced")
	}
}
