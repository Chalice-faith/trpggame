package imws

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func heartbeatTestOptions() clientOptions {
	return clientOptions{
		writeWait:      100 * time.Millisecond,
		pongWait:       150 * time.Millisecond,
		pingPeriod:     25 * time.Millisecond,
		maxMessageSize: MaxTextMessageSize,
		sendBufferSize: 16,
	}
}

func TestClientHandlesPingAndProtocolErrors(t *testing.T) {
	wsURL, _ := newTestIMServer(t)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	tests := []struct {
		name      string
		payload   string
		wantType  MessageType
		wantCode  int
		wantReqID string
	}{
		{
			name:      "ping",
			payload:   `{"type":"ping","request_id":"550e8400-e29b-41d4-a716-446655440000","data":{}}`,
			wantType:  MsgPong,
			wantReqID: "550e8400-e29b-41d4-a716-446655440000",
		},
		{name: "malformed", payload: `{"type":`, wantType: MsgError, wantCode: ErrorCodeMalformedMessage},
		{name: "unknown field", payload: `{"type":"ping","user_id":7}`, wantType: MsgError, wantCode: ErrorCodeMalformedMessage},
		{name: "invalid ping data", payload: `{"type":"ping","data":{"n":1}}`, wantType: MsgError, wantCode: ErrorCodeInvalidMessage},
		{name: "unsupported type", payload: `{"type":"chat_message"}`, wantType: MsgError, wantCode: ErrorCodeUnsupportedMessageType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(tt.payload)); err != nil {
				t.Fatalf("WriteMessage() error = %v", err)
			}
			message := readServerMessage(t, conn)
			if message.Type != tt.wantType || message.RequestID != tt.wantReqID {
				t.Fatalf("message = %#v", message)
			}
			if tt.wantCode != 0 {
				var data ErrorData
				if err := json.Unmarshal(message.Data, &data); err != nil {
					t.Fatalf("unmarshal error data: %v", err)
				}
				if data.Code != tt.wantCode {
					t.Fatalf("error code = %d, want %d", data.Code, tt.wantCode)
				}
			}
		})
	}
}

func TestClientRejectsBinaryMessages(t *testing.T) {
	wsURL, _ := newTestIMServer(t)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3}); err != nil {
		t.Fatalf("write binary message: %v", err)
	}
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseUnsupportedData) {
		t.Fatalf("close error = %v, want code %d", err, websocket.CloseUnsupportedData)
	}
}

func TestClientRejectsOversizedMessages(t *testing.T) {
	wsURL, _ := newTestIMServer(t)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	payload := make([]byte, MaxTextMessageSize+1)
	for index := range payload {
		payload[index] = 'x'
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write oversized message: %v", err)
	}
	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("close error = %v, want code %d", err, websocket.CloseMessageTooBig)
	}
}

func TestClientDisconnectsAfterMissingPong(t *testing.T) {
	observer := newRecordingPresenceObserver()
	wsURL, hub := newTestIMServerWithObserver(t, heartbeatTestOptions(), observer)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)
	if event := receivePresenceEvent(t, observer); event.kind != "connected" {
		t.Fatalf("connected event = %#v", event)
	}

	// 继续读取控制帧，但故意不回复 Pong。
	conn.SetPingHandler(func(string) error { return nil })
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("connection stayed open without Pong")
	}
	waitForUserDisconnected(t, hub, 7)
	if event := receivePresenceEvent(t, observer); event.kind != "disconnected" || event.userID != 7 {
		t.Fatalf("timeout disconnect event = %#v", event)
	}
}

func TestClientPongExtendsReadDeadline(t *testing.T) {
	wsURL, hub := newTestIMServerWithOptions(t, heartbeatTestOptions())
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	pingSeen := make(chan struct{}, 8)
	defaultPingHandler := conn.PingHandler()
	conn.SetPingHandler(func(data string) error {
		select {
		case pingSeen <- struct{}{}:
		default:
		}
		return defaultPingHandler(data)
	})

	messageResult := make(chan ServerMessage, 1)
	errorResult := make(chan error, 1)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	go func() {
		_, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			errorResult <- readErr
			return
		}
		var message ServerMessage
		if unmarshalErr := json.Unmarshal(payload, &message); unmarshalErr != nil {
			errorResult <- unmarshalErr
			return
		}
		messageResult <- message
	}()

	for index := 0; index < 3; index++ {
		select {
		case <-pingSeen:
		case err := <-errorResult:
			t.Fatalf("connection closed while replying to Ping: %v", err)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for Ping %d", index+1)
		}
	}
	if !hub.SendToUser(7, ServerMessage{Type: MsgPong, Timestamp: 1234}) {
		t.Fatal("current connection was removed despite replying to Ping")
	}
	select {
	case message := <-messageResult:
		if message.Type != MsgPong || message.Timestamp != 1234 {
			t.Fatalf("delivered message = %#v", message)
		}
	case err := <-errorResult:
		t.Fatalf("read delivered message: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out reading message after Pong heartbeats")
	}
}

func TestClientProtocolPongRefreshesCurrentPresence(t *testing.T) {
	observer := newRecordingPresenceObserver()
	wsURL, _ := newTestIMServerWithObserver(t, defaultClientOptions(), observer)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)
	if event := receivePresenceEvent(t, observer); event.kind != "connected" {
		t.Fatalf("connected event = %#v", event)
	}
	if err := conn.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("write Pong: %v", err)
	}
	if event := receivePresenceEvent(t, observer); event.kind != "refreshed" || event.userID != 7 {
		t.Fatalf("refresh event = %#v", event)
	}
}

func TestClientNormalAndAbruptDisconnectRemoveCurrentConnection(t *testing.T) {
	tests := []struct {
		name       string
		disconnect func(*websocket.Conn) error
	}{
		{
			name: "normal close",
			disconnect: func(conn *websocket.Conn) error {
				return conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"),
					time.Now().Add(time.Second),
				)
			},
		},
		{
			name: "abrupt TCP close",
			disconnect: func(conn *websocket.Conn) error {
				return conn.UnderlyingConn().Close()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wsURL, hub := newTestIMServer(t)
			conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			_ = readServerMessage(t, conn)

			if err := test.disconnect(conn); err != nil {
				t.Fatalf("disconnect: %v", err)
			}
			waitForUserDisconnected(t, hub, 7)
		})
	}
}

func TestClientHubStopClosesActiveConnection(t *testing.T) {
	wsURL, hub := newTestIMServer(t)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	stopped := make(chan struct{})
	go func() {
		hub.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Hub Stop did not return")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("active connection remained open after Hub Stop")
	}
	if hub.SendToUser(7, ServerMessage{Type: MsgPong}) {
		t.Fatal("Hub delivered after Stop")
	}
}

func waitForUserDisconnected(t *testing.T, hub *Hub, userID uint) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		if !hub.SendToUser(userID, ServerMessage{Type: MsgPong}) {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("current connection was not removed after disconnect")
		}
	}
}
