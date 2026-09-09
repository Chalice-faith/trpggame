package ws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"trpggame/internal/middleware"
	"trpggame/internal/realtime"
)

const testJWTSecret = "ws-test-secret"
const testAllowedOrigin = "https://game.example.com"

// fakeAuthorizer 允许指定的 (roomID, userID) 订阅。
type fakeAuthorizer struct {
	allowed map[uint]map[uint]bool
	err     error
}

func (f fakeAuthorizer) Authorize(_ context.Context, userID, roomID uint) error {
	if f.err != nil {
		return f.err
	}
	if f.allowed[roomID][userID] {
		return nil
	}
	return errors.New("denied")
}

func newTestWSEngine(t *testing.T, authz RoomAuthorizer) (*gin.Engine, *Hub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	hub := NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)
	origins, err := realtime.ParseAllowedOrigins(testAllowedOrigin)
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}

	engine := gin.New()
	engine.GET("/ws", HandleWebSocket(hub, testJWTSecret, origins, authz))
	return engine, hub
}

func newTestWSServer(t *testing.T, authz RoomAuthorizer) (string, *Hub) {
	t.Helper()
	engine, hub := newTestWSEngine(t, authz)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	return wsURL, hub
}

func dialWSResponse(
	t *testing.T,
	wsURL, token, roomID string,
	header http.Header,
) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	query := url.Values{}
	if token != "" {
		query.Set("token", token)
	}
	if roomID != "" {
		query.Set("room_id", roomID)
	}
	u := wsURL + "/ws"
	if encoded := query.Encode(); encoded != "" {
		u += "?" + encoded
	}
	return websocket.DefaultDialer.Dial(u, header)
}

// dialWS 发起 WebSocket 升级。失败时返回 HTTP 状态码。
func dialWS(t *testing.T, wsURL, token, roomID string) (*websocket.Conn, int, error) {
	t.Helper()
	conn, resp, err := dialWSResponse(t, wsURL, token, roomID, nil)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return conn, 0, nil
}

type handshakeErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func assertHandshakeErrorBody(
	t *testing.T,
	body io.Reader,
	wantCode int,
	wantMessage string,
) {
	t.Helper()
	var response handshakeErrorResponse
	if err := json.NewDecoder(body).Decode(&response); err != nil {
		t.Fatalf("decode handshake error: %v", err)
	}
	if response.Code != wantCode || response.Message != wantMessage {
		t.Fatalf("handshake error = %#v, want code=%d message=%q", response, wantCode, wantMessage)
	}
}

func assertHandshakeError(
	t *testing.T,
	wsURL, token, roomID, origin string,
	wantStatus, wantCode int,
	wantMessage string,
) {
	t.Helper()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	conn, response, err := dialWSResponse(t, wsURL, token, roomID, header)
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
	assertHandshakeErrorBody(t, response.Body, wantCode, wantMessage)
}

func TestHandleWebSocketRejectsMissingToken(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{})
	assertHandshakeError(t, wsURL, "", "41", "", http.StatusUnauthorized, wsErrorMissingToken, "missing token")
}

func TestHandleWebSocketRejectsInvalidToken(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{})
	assertHandshakeError(t, wsURL, "not-a-jwt", "41", "", http.StatusUnauthorized, wsErrorInvalidToken, "invalid token")
}

func TestHandleWebSocketRejectsInvalidRoomID(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{})
	token, err := middleware.GenerateToken(7, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	assertHandshakeError(t, wsURL, token, "abc", "", http.StatusBadRequest, wsErrorInvalidRoomID, "invalid room_id")
}

func TestHandleWebSocketRejectsUnauthorizedRoom(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{allowed: map[uint]map[uint]bool{41: {7: true}}})
	token, err := middleware.GenerateToken(8, "outsider", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	assertHandshakeError(t, wsURL, token, "42", "", http.StatusForbidden, wsErrorRoomAccessDenied, "room access denied")
}

func TestHandleWebSocketRejectsOnAuthorizerError(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{err: errors.New("db down")})
	token, err := middleware.GenerateToken(7, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	assertHandshakeError(t, wsURL, token, "41", "", http.StatusForbidden, wsErrorRoomAccessDenied, "room access denied")
}

func TestHandleWebSocketAcceptsAllowedOrigin(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{allowed: map[uint]map[uint]bool{41: {7: true}}})
	token, err := middleware.GenerateToken(7, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	header := http.Header{"Origin": []string{testAllowedOrigin}}
	conn, response, err := dialWSResponse(t, wsURL, token, "41", header)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("dial allowed origin: status = %d, err = %v", status, err)
	}
	defer conn.Close()
}

func TestHandleWebSocketRejectsDisallowedOriginsBeforeAuth(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{})
	tests := []struct {
		name   string
		origin string
	}{
		{name: "unknown", origin: "https://other.example.com"},
		{name: "null", origin: "null"},
		{name: "comma separated", origin: testAllowedOrigin + ", https://other.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertHandshakeError(
				t,
				wsURL,
				"",
				"41",
				tt.origin,
				http.StatusForbidden,
				wsErrorOriginNotAllowed,
				"origin not allowed",
			)
		})
	}
}

func TestHandleWebSocketRejectsMultipleOriginHeaders(t *testing.T) {
	engine, _ := newTestWSEngine(t, fakeAuthorizer{})
	request := httptest.NewRequest(http.MethodGet, "/ws", nil)
	request.Header.Add("Origin", testAllowedOrigin)
	request.Header.Add("Origin", "https://other.example.com")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	assertHandshakeErrorBody(t, recorder.Body, wsErrorOriginNotAllowed, "origin not allowed")
}

func TestHandleWebSocketSubscribesAndAcceptsSync(t *testing.T) {
	wsURL, hub := newTestWSServer(t, fakeAuthorizer{allowed: map[uint]map[uint]bool{41: {7: true}}})

	token, err := middleware.GenerateToken(7, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	conn, status, err := dialWS(t, wsURL, token, "41")
	if err != nil {
		t.Fatalf("dial: status = %d, err = %v", status, err)
	}
	defer conn.Close()

	// 订阅确认
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read subscribed: %v", err)
	}
	var subscribed Message
	if err := json.Unmarshal(raw, &subscribed); err != nil {
		t.Fatalf("unmarshal subscribed: %v", err)
	}
	if subscribed.Type != MsgSubscribed {
		t.Fatalf("first message type = %q, want subscribed", subscribed.Type)
	}

	// 服务端向房间广播，客户端应收到带 seq 的消息
	hub.BroadcastToRoom(41, MsgSystem, json.RawMessage(`{"n":1}`))
	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read broadcast: %v", err)
	}
	var event Message
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.Type != MsgSystem || event.Seq != 1 {
		t.Fatalf("event = %#v, want system seq=1", event)
	}

	// 客户端请求补推：since_seq=0 应返回 seq=1
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"sync","data":{"since_seq":0}}`)); err != nil {
		t.Fatalf("write sync: %v", err)
	}
	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read sync_batch: %v", err)
	}
	var sync Message
	if err := json.Unmarshal(raw, &sync); err != nil {
		t.Fatalf("unmarshal sync_batch: %v", err)
	}
	if sync.Type != MsgSyncBatch {
		t.Fatalf("sync response type = %q, want sync_batch", sync.Type)
	}
	var batch SyncBatchData
	if err := json.Unmarshal(sync.Data, &batch); err != nil {
		t.Fatalf("unmarshal sync data: %v", err)
	}
	if batch.NextSeq != 1 || len(batch.Messages) != 1 || batch.Messages[0].Seq != 1 {
		t.Fatalf("sync_batch = %#v, want [seq=1] next=1", batch)
	}
}

func TestHandleWebSocketLatestConnectionReplacesOld(t *testing.T) {
	wsURL, hub := newTestWSServer(t, fakeAuthorizer{
		allowed: map[uint]map[uint]bool{41: {7: true}},
	})
	token, err := middleware.GenerateToken(7, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	oldConnection, status, err := dialWS(t, wsURL, token, "41")
	if err != nil {
		t.Fatalf("dial old connection: status = %d, err = %v", status, err)
	}
	defer oldConnection.Close()
	if message := readGameServerMessage(t, oldConnection); message.Type != MsgSubscribed {
		t.Fatalf("old first message = %q, want subscribed", message.Type)
	}

	newConnection, status, err := dialWS(t, wsURL, token, "41")
	if err != nil {
		t.Fatalf("dial new connection: status = %d, err = %v", status, err)
	}
	defer newConnection.Close()
	if message := readGameServerMessage(t, newConnection); message.Type != MsgSubscribed {
		t.Fatalf("new first message = %q, want subscribed", message.Type)
	}

	_ = oldConnection.SetReadDeadline(time.Now().Add(testRecvTimeout))
	_, _, err = oldConnection.ReadMessage()
	if !websocket.IsCloseError(err, realtime.CloseCodeConnectionReplaced) {
		t.Fatalf(
			"old connection close error = %v, want code %d",
			err,
			realtime.CloseCodeConnectionReplaced,
		)
	}

	hub.SendToUser(41, 7, MsgSystem, json.RawMessage(`{"current":true}`))
	message := readGameServerMessage(t, newConnection)
	if message.Type != MsgSystem || message.Seq != 1 {
		t.Fatalf("new connection message = %#v, want system seq=1", message)
	}
}

func TestHandleWebSocketClosesUpgradedConnectionWhenHubStopped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := NewHub()
	go hub.Run()
	hub.Stop()
	origins, err := realtime.ParseAllowedOrigins(testAllowedOrigin)
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}
	engine := gin.New()
	engine.GET("/ws", HandleWebSocket(
		hub,
		testJWTSecret,
		origins,
		fakeAuthorizer{allowed: map[uint]map[uint]bool{41: {7: true}}},
	))
	server := httptest.NewServer(engine)
	defer server.Close()

	token, err := middleware.GenerateToken(7, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	connection, status, err := dialWS(t, wsURL, token, "41")
	if err != nil {
		t.Fatalf("dial stopped hub: status = %d, err = %v", status, err)
	}
	defer connection.Close()

	_ = connection.SetReadDeadline(time.Now().Add(testRecvTimeout))
	_, _, err = connection.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseTryAgainLater) {
		t.Fatalf("close error = %v, want code %d", err, websocket.CloseTryAgainLater)
	}
}

func readGameServerMessage(t *testing.T, connection *websocket.Conn) Message {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(testRecvTimeout))
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatalf("read server message: %v", err)
	}
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("unmarshal server message: %v", err)
	}
	return message
}
