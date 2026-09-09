package imws

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"trpggame/internal/middleware"
	"trpggame/internal/realtime"
)

const testJWTSecret = "imws-test-secret"
const testAllowedOrigin = "https://game.example.com"
const testWebSocketReadTimeout = 3 * time.Second

type handshakeErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newTestIMEngine(t *testing.T) (*gin.Engine, *Hub) {
	return newTestIMEngineWithOptions(t, defaultClientOptions())
}

func newTestIMEngineWithOptions(t *testing.T, options clientOptions) (*gin.Engine, *Hub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	origins, err := realtime.ParseAllowedOrigins(testAllowedOrigin)
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}
	hub := NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)

	engine := gin.New()
	engine.GET("/ws/im", handleWebSocketWithOptions(hub, testJWTSecret, origins, options))
	return engine, hub
}

func newTestIMServer(t *testing.T) (string, *Hub) {
	return newTestIMServerWithOptions(t, defaultClientOptions())
}

func newTestIMServerWithOptions(t *testing.T, options clientOptions) (string, *Hub) {
	t.Helper()
	engine, hub := newTestIMEngineWithOptions(t, options)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), hub
}

func generateTestToken(t *testing.T, userID uint) string {
	t.Helper()
	token, err := middleware.GenerateToken(userID, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	return token
}

func dialIM(
	t *testing.T,
	wsURL, token string,
	header http.Header,
) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	endpoint := wsURL + "/ws/im"
	if token != "" {
		endpoint += "?" + url.Values{"token": []string{token}}.Encode()
	}
	return websocket.DefaultDialer.Dial(endpoint, header)
}

func readServerMessage(t *testing.T, conn *websocket.Conn) ServerMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(testWebSocketReadTimeout))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	var message ServerMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("unmarshal server message: %v", err)
	}
	return message
}

func assertIMHandshakeError(
	t *testing.T,
	wsURL, token, origin string,
	wantStatus, wantCode int,
	wantMessage string,
) {
	t.Helper()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	conn, response, err := dialIM(t, wsURL, token, header)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("WebSocket upgrade unexpectedly succeeded")
	}
	if response == nil {
		t.Fatalf("WebSocket upgrade returned no HTTP response: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d", response.StatusCode, wantStatus)
	}
	assertIMErrorBody(t, response.Body, wantCode, wantMessage)
}

func assertIMErrorBody(t *testing.T, body io.Reader, wantCode int, wantMessage string) {
	t.Helper()
	var response handshakeErrorResponse
	if err := json.NewDecoder(body).Decode(&response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Code != wantCode || response.Message != wantMessage {
		t.Fatalf("response = %#v, want code=%d message=%q", response, wantCode, wantMessage)
	}
}

func TestHandleWebSocketConnectsWithAllowedOrMissingOrigin(t *testing.T) {
	wsURL, _ := newTestIMServer(t)
	token := generateTestToken(t, 7)
	tests := []struct {
		name   string
		header http.Header
	}{
		{name: "missing origin"},
		{name: "allowed origin", header: http.Header{"Origin": []string{testAllowedOrigin}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, response, err := dialIM(t, wsURL, token, tt.header)
			if err != nil {
				status := 0
				if response != nil {
					status = response.StatusCode
				}
				t.Fatalf("dial: status = %d, err = %v", status, err)
			}
			defer conn.Close()

			message := readServerMessage(t, conn)
			if message.Type != MsgConnected || message.Timestamp == 0 {
				t.Fatalf("first message = %#v", message)
			}
			var data ConnectedData
			if err := json.Unmarshal(message.Data, &data); err != nil {
				t.Fatalf("unmarshal connected data: %v", err)
			}
			if data.UserID != 7 {
				t.Fatalf("user_id = %d, want 7", data.UserID)
			}
			parsed, err := uuid.Parse(data.ConnectionID)
			if err != nil || parsed.String() != data.ConnectionID {
				t.Fatalf("connection_id = %q, want canonical UUID", data.ConnectionID)
			}
		})
	}
}

func TestHandleWebSocketRejectsHandshakeErrors(t *testing.T) {
	wsURL, _ := newTestIMServer(t)
	expiredToken, err := middleware.GenerateToken(7, "investigator", testJWTSecret, -1)
	if err != nil {
		t.Fatalf("generate expired token: %v", err)
	}
	tests := []struct {
		name        string
		token       string
		origin      string
		wantStatus  int
		wantCode    int
		wantMessage string
	}{
		{name: "missing token", wantStatus: http.StatusUnauthorized, wantCode: ErrorCodeMissingToken, wantMessage: "missing token"},
		{name: "invalid token", token: "not-a-jwt", wantStatus: http.StatusUnauthorized, wantCode: ErrorCodeInvalidToken, wantMessage: "invalid token"},
		{name: "expired token", token: expiredToken, wantStatus: http.StatusUnauthorized, wantCode: ErrorCodeInvalidToken, wantMessage: "invalid token"},
		{name: "unknown origin before auth", origin: "https://other.example.com", wantStatus: http.StatusForbidden, wantCode: ErrorCodeOriginNotAllowed, wantMessage: "origin not allowed"},
		{name: "null origin before auth", origin: "null", wantStatus: http.StatusForbidden, wantCode: ErrorCodeOriginNotAllowed, wantMessage: "origin not allowed"},
		{name: "comma origin before auth", origin: testAllowedOrigin + ", https://other.example.com", wantStatus: http.StatusForbidden, wantCode: ErrorCodeOriginNotAllowed, wantMessage: "origin not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIMHandshakeError(t, wsURL, tt.token, tt.origin, tt.wantStatus, tt.wantCode, tt.wantMessage)
		})
	}
}

func TestHandleWebSocketRejectsMultipleOriginHeaders(t *testing.T) {
	engine, _ := newTestIMEngine(t)
	request := httptest.NewRequest(http.MethodGet, "/ws/im", nil)
	request.Header.Add("Origin", testAllowedOrigin)
	request.Header.Add("Origin", "https://other.example.com")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	assertIMErrorBody(t, recorder.Body, ErrorCodeOriginNotAllowed, "origin not allowed")
}

func TestHandleWebSocketReplacesPreviousConnection(t *testing.T) {
	wsURL, _ := newTestIMServer(t)
	token := generateTestToken(t, 7)
	oldConnection, _, err := dialIM(t, wsURL, token, nil)
	if err != nil {
		t.Fatalf("dial old connection: %v", err)
	}
	defer oldConnection.Close()
	oldConnected := readServerMessage(t, oldConnection)

	newConnection, _, err := dialIM(t, wsURL, token, nil)
	if err != nil {
		t.Fatalf("dial new connection: %v", err)
	}
	defer newConnection.Close()
	newConnected := readServerMessage(t, newConnection)
	if oldConnected.Type != MsgConnected || newConnected.Type != MsgConnected {
		t.Fatalf("connected messages = %q, %q", oldConnected.Type, newConnected.Type)
	}

	replacement := readServerMessage(t, oldConnection)
	if replacement.Type != MsgConnectionReplaced {
		t.Fatalf("old connection message = %q", replacement.Type)
	}
	var data ConnectionReplacedData
	if err := json.Unmarshal(replacement.Data, &data); err != nil {
		t.Fatalf("unmarshal replacement data: %v", err)
	}
	if data.Reason != CloseReasonConnectionReplaced {
		t.Fatalf("replacement reason = %q", data.Reason)
	}

	_, _, err = oldConnection.ReadMessage()
	if !websocket.IsCloseError(err, CloseCodeConnectionReplaced) {
		t.Fatalf("old connection close error = %v, want code %d", err, CloseCodeConnectionReplaced)
	}

	requestID := "550e8400-e29b-41d4-a716-446655440000"
	if err := newConnection.WriteMessage(
		websocket.TextMessage,
		[]byte(`{"type":"ping","request_id":"`+requestID+`"}`),
	); err != nil {
		t.Fatalf("write ping to new connection: %v", err)
	}
	if pong := readServerMessage(t, newConnection); pong.Type != MsgPong || pong.RequestID != requestID {
		t.Fatalf("new connection pong = %#v", pong)
	}
}

func TestHandleWebSocketClosesWhenHubIsStopped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	origins, err := realtime.ParseAllowedOrigins(testAllowedOrigin)
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}
	hub := NewHub()
	go hub.Run()
	hub.Stop()
	engine := gin.New()
	engine.GET("/ws/im", HandleWebSocket(hub, testJWTSecret, origins))
	server := httptest.NewServer(engine)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_, _, err = conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseTryAgainLater) {
		t.Fatalf("close error = %v, want code %d", err, websocket.CloseTryAgainLater)
	}
}

func TestHandleWebSocketTakeoverStormLeavesLatestConnectionUsable(t *testing.T) {
	wsURL, _ := newTestIMServer(t)
	token := generateTestToken(t, 7)
	const connections = 50

	var previous *websocket.Conn
	for index := range connections {
		current, _, err := dialIM(t, wsURL, token, nil)
		if err != nil {
			t.Fatalf("dial connection %d: %v", index, err)
		}
		if message := readServerMessage(t, current); message.Type != MsgConnected {
			_ = current.Close()
			t.Fatalf("connection %d first message = %q", index, message.Type)
		}
		if previous != nil {
			if message := readServerMessage(t, previous); message.Type != MsgConnectionReplaced {
				_ = current.Close()
				t.Fatalf("connection %d replacement message = %q", index-1, message.Type)
			}
			_, _, closeErr := previous.ReadMessage()
			if !websocket.IsCloseError(closeErr, CloseCodeConnectionReplaced) {
				_ = current.Close()
				t.Fatalf("connection %d close error = %v", index-1, closeErr)
			}
			_ = previous.Close()
		}
		previous = current
	}
	defer previous.Close()

	requestID := "550e8400-e29b-41d4-a716-446655440000"
	if err := previous.WriteMessage(
		websocket.TextMessage,
		[]byte(`{"type":"ping","request_id":"`+requestID+`"}`),
	); err != nil {
		t.Fatalf("write ping to latest connection: %v", err)
	}
	if pong := readServerMessage(t, previous); pong.Type != MsgPong || pong.RequestID != requestID {
		t.Fatalf("latest connection pong = %#v", pong)
	}
}
