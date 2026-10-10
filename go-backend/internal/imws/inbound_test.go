package imws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"trpggame/internal/realtime"
)

type stubInbound struct {
	mu      sync.Mutex
	handled []ClientMessage
	handler func(*stubInbound, context.Context, uint, ClientMessage) (*ServerMessage, error)
}

func (s *stubInbound) HandleIM(ctx context.Context, userID uint, message ClientMessage) (*ServerMessage, error) {
	s.mu.Lock()
	s.handled = append(s.handled, message)
	hook := s.handler
	s.mu.Unlock()
	if hook != nil {
		return hook(s, ctx, userID, message)
	}
	return &ServerMessage{
		Type:      MsgChatAck,
		RequestID: message.RequestID,
		Data:      json.RawMessage(`{"ok":true}`),
	}, nil
}

func (s *stubInbound) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.handled)
}

func newTestIMServerWithInbound(t *testing.T, handler InboundHandler) (string, *Hub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	origins, err := realtime.ParseAllowedOrigins(testAllowedOrigin)
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}
	hub := NewHub()
	hub.SetInboundHandler(handler)
	go hub.Run()
	t.Cleanup(hub.Stop)

	engine := gin.New()
	engine.GET("/ws/im", HandleWebSocket(hub, testJWTSecret, origins))
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), hub
}

func TestClientDispatchesBusinessReplyAndErrors(t *testing.T) {
	handler := &stubInbound{}
	wsURL, _ := newTestIMServerWithInbound(t, handler)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	requestID := "550e8400-e29b-41d4-a716-446655440000"
	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"type":"chat_message","request_id":"`+requestID+`","data":{"conversation_id":41}}`,
	)); err != nil {
		t.Fatalf("write chat_message: %v", err)
	}
	ack := readServerMessage(t, conn)
	if ack.Type != MsgChatAck || ack.RequestID != requestID || string(ack.Data) != `{"ok":true}` {
		t.Fatalf("ack = %#v", ack)
	}
	if handler.count() != 1 {
		t.Fatalf("handled count = %d", handler.count())
	}

	handler.mu.Lock()
	handler.handler = func(*stubInbound, context.Context, uint, ClientMessage) (*ServerMessage, error) {
		return nil, &BusinessError{Code: ErrorCodeInvalidChatMessage, Message: "invalid chat message"}
	}
	handler.mu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"type":"chat_message","request_id":"`+requestID+`","data":{"conversation_id":41}}`,
	)); err != nil {
		t.Fatalf("write second chat_message: %v", err)
	}
	errorFrame := readServerMessage(t, conn)
	if errorFrame.Type != MsgError || errorFrame.RequestID != requestID {
		t.Fatalf("error frame = %#v", errorFrame)
	}
	var data ErrorData
	if err := json.Unmarshal(errorFrame.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Code != ErrorCodeInvalidChatMessage || data.Message != "invalid chat message" {
		t.Fatalf("error data = %#v", data)
	}
}

func TestClientWithoutHandlerRejectsBusinessTypes(t *testing.T) {
	wsURL, _ := newTestIMServer(t)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"type":"chat_message","request_id":"550e8400-e29b-41d4-a716-446655440000","data":{}}`,
	)); err != nil {
		t.Fatalf("write chat_message: %v", err)
	}
	frame := readServerMessage(t, conn)
	if frame.Type != MsgError {
		t.Fatalf("frame type = %q", frame.Type)
	}
	var data ErrorData
	if err := json.Unmarshal(frame.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Code != ErrorCodeUnsupportedMessageType {
		t.Fatalf("error code = %d", data.Code)
	}
}

func TestClientProcessesBusinessMessagesInReadOrder(t *testing.T) {
	handler := &stubInbound{}
	release := make(chan struct{})
	var sequence atomic.Int64
	handler.mu.Lock()
	handler.handler = func(_ *stubInbound, _ context.Context, _ uint, message ClientMessage) (*ServerMessage, error) {
		if sequence.Add(1) == 1 {
			<-release
		}
		return &ServerMessage{
			Type: MsgChatAck, RequestID: message.RequestID, Data: json.RawMessage(`{}`),
		}, nil
	}
	handler.mu.Unlock()

	wsURL, _ := newTestIMServerWithInbound(t, handler)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"type":"chat_message","request_id":"550e8400-e29b-41d4-a716-446655440000","data":{}}`,
	)); err != nil {
		t.Fatalf("write blocking chat_message: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"type":"ping","request_id":"8c21c14d-cf36-4fd2-845d-1496d9c154b2"}`,
	)); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	close(release)

	first := readServerMessage(t, conn)
	second := readServerMessage(t, conn)
	if first.Type != MsgChatAck || first.RequestID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("first frame = %#v, want chat ack before pong", first)
	}
	if second.Type != MsgPong {
		t.Fatalf("second frame = %#v, want pong after ack", second)
	}
}

func TestClientAcceptsMaxChatContentFrame(t *testing.T) {
	handler := &stubInbound{}
	wsURL, _ := newTestIMServerWithInbound(t, handler)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	content := strings.Repeat("骰", 4000)
	frame, err := json.Marshal(map[string]any{
		"type": "chat_message", "request_id": "550e8400-e29b-41d4-a716-446655440000",
		"data": map[string]any{"conversation_id": 41, "message_type": "text", "content": content},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) <= 8192 || len(frame) > MaxTextMessageSize {
		t.Fatalf("frame size = %d, want within legacy and new limits", len(frame))
	}
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write large frame: %v", err)
	}
	if ack := readServerMessage(t, conn); ack.Type != MsgChatAck {
		t.Fatalf("frame rejected: %#v", ack)
	}
	handler.mu.Lock()
	var payload chatPayloadProbe
	handled := append([]ClientMessage(nil), handler.handled...)
	handler.mu.Unlock()
	if len(handled) == 1 {
		_ = json.Unmarshal(handled[0].Data, &payload)
	}
	if payload.Content != content {
		t.Fatalf("content length = %d, want 4000", len([]rune(payload.Content)))
	}
}

type chatPayloadProbe struct {
	Content string `json:"content"`
}

func TestClientBusinessDeadlineIsBounded(t *testing.T) {
	var observed time.Time
	var mu sync.Mutex
	handler := &stubInbound{}
	handler.mu.Lock()
	handler.handler = func(_ *stubInbound, ctx context.Context, _ uint, _ ClientMessage) (*ServerMessage, error) {
		deadline, ok := ctx.Deadline()
		mu.Lock()
		observed = deadline
		mu.Unlock()
		if !ok {
			return nil, errors.New("missing deadline")
		}
		return &ServerMessage{Type: MsgChatAck, Data: json.RawMessage(`{}`)}, nil
	}
	handler.mu.Unlock()

	wsURL, _ := newTestIMServerWithInbound(t, handler)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	if err := conn.WriteMessage(websocket.TextMessage, []byte(
		`{"type":"chat_message","request_id":"550e8400-e29b-41d4-a716-446655440000","data":{}}`,
	)); err != nil {
		t.Fatalf("write chat_message: %v", err)
	}
	if ack := readServerMessage(t, conn); ack.Type != MsgChatAck {
		t.Fatalf("ack = %#v", ack)
	}
	mu.Lock()
	defer mu.Unlock()
	remaining := time.Until(observed)
	if remaining <= 0 || remaining > 6*time.Second {
		t.Fatalf("business deadline remaining = %v", remaining)
	}
}

func TestClientBusinessDuringHubStopDoesNotDeadlock(t *testing.T) {
	handler := &stubInbound{}
	gin.SetMode(gin.TestMode)
	origins, err := realtime.ParseAllowedOrigins(testAllowedOrigin)
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}
	hub := NewHub()
	hub.SetInboundHandler(handler)
	go hub.Run()

	handler.mu.Lock()
	handler.handler = func(_ *stubInbound, _ context.Context, _ uint, message ClientMessage) (*ServerMessage, error) {
		hub.Stop()
		return &ServerMessage{Type: MsgChatAck, RequestID: message.RequestID, Data: json.RawMessage(`{}`)}, nil
	}
	handler.mu.Unlock()

	engine := gin.New()
	engine.GET("/ws/im", HandleWebSocket(hub, testJWTSecret, origins))
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(
			`{"type":"chat_message","request_id":"550e8400-e29b-41d4-a716-446655440000","data":{}}`,
		))
		_ = conn.SetReadDeadline(time.Now().Add(testWebSocketReadTimeout))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("business handling deadlocked during hub stop")
	}
}

func TestClientRateLimitSkipsHandlerAndKeepsConnectionOpen(t *testing.T) {
	handler := &stubInbound{}
	wsURL, _ := newTestIMServerWithInbound(t, handler)
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = readServerMessage(t, conn)

	rateLimited := 0
	for index := 1; index <= 30; index++ {
		requestID := fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", index)
		frame := `{"type":"chat_message","request_id":"` + requestID + `","data":{"conversation_id":41}}`
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write request %d: %v", index, err)
		}
	}
	for index := 1; index <= 30; index++ {
		requestID := fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", index)
		reply := readServerMessage(t, conn)
		if reply.Type == MsgError {
			var data ErrorData
			if err := json.Unmarshal(reply.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Code != ErrorCodeRateLimited || data.Message != "rate limited" || reply.RequestID != requestID {
				t.Fatalf("rate-limit reply = %#v, data = %#v", reply, data)
			}
			rateLimited++
		}
	}
	if rateLimited == 0 {
		t.Fatal("rapid burst did not trigger rate limiting")
	}
	if got, want := handler.count(), 30-rateLimited; got != want {
		t.Fatalf("handler count = %d, want %d", got, want)
	}

	pingID := "8c21c14d-cf36-4fd2-845d-1496d9c154b2"
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping","request_id":"`+pingID+`","data":{}}`)); err != nil {
		t.Fatalf("write ping after limit: %v", err)
	}
	if pong := readServerMessage(t, conn); pong.Type != MsgPong || pong.RequestID != pingID {
		t.Fatalf("connection closed or ping limited: %#v", pong)
	}
}
