package imws

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodeClientMessage(t *testing.T) {
	requestID := "550e8400-e29b-41d4-a716-446655440000"
	message, err := DecodeClientMessage([]byte(
		`{"type":"ping","request_id":"` + requestID + `","data":{"n":1}}`,
	))
	if err != nil {
		t.Fatalf("DecodeClientMessage() error = %v", err)
	}
	if message.Type != MsgPing || message.RequestID != requestID || string(message.Data) != `{"n":1}` {
		t.Fatalf("message = %#v", message)
	}
}

func TestDecodeClientMessageAllowsEmptyOrNullData(t *testing.T) {
	for _, raw := range []string{
		`{"type":"ping"}`,
		`{"type":"ping","data":null}`,
		`{"type":"ping","data":{}}`,
	} {
		if _, err := DecodeClientMessage([]byte(raw)); err != nil {
			t.Fatalf("DecodeClientMessage(%s) error = %v", raw, err)
		}
	}
}

func TestDecodeClientMessageRejectsInvalidEnvelope(t *testing.T) {
	oversized := make([]byte, MaxTextMessageSize+1)
	for i := range oversized {
		oversized[i] = 'x'
	}
	tests := []struct {
		name       string
		raw        []byte
		wantTarget error
	}{
		{name: "empty", raw: nil, wantTarget: ErrInvalidClientMessage},
		{name: "malformed", raw: []byte(`{"type":`), wantTarget: ErrMalformedClientMessage},
		{name: "unknown field", raw: []byte(`{"type":"ping","user_id":7}`), wantTarget: ErrMalformedClientMessage},
		{name: "multiple values", raw: []byte(`{"type":"ping"} {}`), wantTarget: ErrMalformedClientMessage},
		{name: "missing type", raw: []byte(`{"data":{}}`), wantTarget: ErrInvalidClientMessage},
		{name: "invalid type characters", raw: []byte(`{"type":"Chat.Message"}`), wantTarget: ErrInvalidClientMessage},
		{name: "type too long", raw: []byte(`{"type":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), wantTarget: ErrInvalidClientMessage},
		{name: "non canonical UUID", raw: []byte(`{"type":"ping","request_id":"550E8400-E29B-41D4-A716-446655440000"}`), wantTarget: ErrInvalidClientMessage},
		{name: "invalid UUID", raw: []byte(`{"type":"ping","request_id":"not-a-uuid"}`), wantTarget: ErrInvalidClientMessage},
		{name: "array data", raw: []byte(`{"type":"ping","data":[]}`), wantTarget: ErrInvalidClientMessage},
		{name: "string data", raw: []byte(`{"type":"ping","data":"value"}`), wantTarget: ErrInvalidClientMessage},
		{name: "oversized", raw: oversized, wantTarget: ErrInvalidClientMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeClientMessage(tt.raw)
			if !errors.Is(err, tt.wantTarget) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, tt.wantTarget)
			}
		})
	}
}

func TestServerMessageJSONContract(t *testing.T) {
	data, err := json.Marshal(ServerMessage{
		Type:      MsgConnected,
		Timestamp: 1788796800000,
		Data: json.RawMessage(
			`{"user_id":7,"connection_id":"550e8400-e29b-41d4-a716-446655440000"}`,
		),
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	want := `{"type":"connected","timestamp":1788796800000,"data":{"user_id":7,"connection_id":"550e8400-e29b-41d4-a716-446655440000"}}`
	if string(data) != want {
		t.Fatalf("JSON = %s, want %s", data, want)
	}
}

func TestValidMessageType(t *testing.T) {
	for _, value := range []MessageType{MsgPing, MsgPong, MsgConnected, MsgError, MsgConnectionReplaced, "chat_message"} {
		if !ValidMessageType(value) {
			t.Fatalf("ValidMessageType(%q) = false", value)
		}
	}
	for _, value := range []MessageType{"", "Ping", " chat_message", "chat.message"} {
		if ValidMessageType(value) {
			t.Fatalf("ValidMessageType(%q) = true", value)
		}
	}
}
