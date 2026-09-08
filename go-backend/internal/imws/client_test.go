package imws

import (
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
)

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
