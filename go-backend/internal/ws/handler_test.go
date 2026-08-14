package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"trpggame/internal/middleware"
)

const testJWTSecret = "ws-test-secret"

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

func newTestWSServer(t *testing.T, authz RoomAuthorizer) (string, *Hub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	hub := NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)

	engine := gin.New()
	engine.GET("/ws", HandleWebSocket(hub, testJWTSecret, authz))
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	return wsURL, hub
}

// dialWS 发起 WebSocket 升级。失败时返回 HTTP 状态码。
func dialWS(t *testing.T, wsURL, token, roomID string) (*websocket.Conn, int, error) {
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
	conn, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	return conn, 0, nil
}

func TestHandleWebSocketRejectsMissingToken(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{})
	_, status, err := dialWS(t, wsURL, "", "41")
	if err == nil || status != 401 {
		t.Fatalf("missing token: status = %d, err = %v, want 401", status, err)
	}
}

func TestHandleWebSocketRejectsInvalidToken(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{})
	_, status, err := dialWS(t, wsURL, "not-a-jwt", "41")
	if err == nil || status != 401 {
		t.Fatalf("invalid token: status = %d, err = %v, want 401", status, err)
	}
}

func TestHandleWebSocketRejectsInvalidRoomID(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{})
	token, err := middleware.GenerateToken(7, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	_, status, err := dialWS(t, wsURL, token, "abc")
	if err == nil || status != 400 {
		t.Fatalf("invalid room_id: status = %d, err = %v, want 400", status, err)
	}
}

func TestHandleWebSocketRejectsUnauthorizedRoom(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{allowed: map[uint]map[uint]bool{41: {7: true}}})
	token, err := middleware.GenerateToken(8, "outsider", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	_, status, err := dialWS(t, wsURL, token, "42")
	if err == nil || status != 403 {
		t.Fatalf("unauthorized room: status = %d, err = %v, want 403", status, err)
	}
}

func TestHandleWebSocketRejectsOnAuthorizerError(t *testing.T) {
	wsURL, _ := newTestWSServer(t, fakeAuthorizer{err: errors.New("db down")})
	token, err := middleware.GenerateToken(7, "investigator", testJWTSecret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	_, status, err := dialWS(t, wsURL, token, "41")
	if err == nil || status != 403 {
		t.Fatalf("authorizer error: status = %d, err = %v, want 403", status, err)
	}
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
