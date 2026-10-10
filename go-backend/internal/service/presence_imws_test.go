package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"trpggame/internal/imws"
	"trpggame/internal/middleware"
	"trpggame/internal/model"
	"trpggame/internal/realtime"
	"trpggame/internal/repo"
)

// 本文件验证 service 层实时事件（presence、friendship_updated）在真实 IM
// WebSocket 通道上的端到端投递；因 service 依赖 imws 类型，测试归入 service 包。

const (
	imwsTestJWTSecret = "imws-test-secret"
	imwsTestOrigin    = "https://game.example.com"
	imwsTestReadWait  = 3 * time.Second
)

type acceptedFriendMap map[uint][]uint

func (m acceptedFriendMap) ListAcceptedPeerIDs(_ context.Context, userID uint) ([]uint, error) {
	return append([]uint(nil), m[userID]...), nil
}

type realtimeUserRepoStub struct{ users map[uint]model.User }

func (r realtimeUserRepoStub) FindActiveByID(_ context.Context, id uint) (*model.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}
func (r realtimeUserRepoStub) FindActiveByIDs(_ context.Context, ids []uint) ([]model.User, error) {
	users := make([]model.User, 0, len(ids))
	for _, id := range ids {
		if user, ok := r.users[id]; ok {
			users = append(users, user)
		}
	}
	return users, nil
}
func (r realtimeUserRepoStub) SearchActive(context.Context, uint, string) ([]model.User, error) {
	return nil, nil
}

func generateIMTestToken(t *testing.T, userID uint) string {
	t.Helper()
	token, err := middleware.GenerateToken(userID, "investigator", imwsTestJWTSecret, 15)
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	return token
}

func dialIMTest(t *testing.T, wsURL string, token string) *websocket.Conn {
	t.Helper()
	endpoint := wsURL + "/ws/im?" + url.Values{"token": []string{token}}.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if err != nil {
		t.Fatalf("dial IM websocket: %v", err)
	}
	return conn
}

func readIMServerMessage(t *testing.T, conn *websocket.Conn) imws.ServerMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(imwsTestReadWait))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	var message imws.ServerMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("unmarshal server message: %v", err)
	}
	return message
}

func readIMServerMessageOfType(t *testing.T, conn *websocket.Conn, want imws.MessageType) imws.ServerMessage {
	t.Helper()
	for range 4 {
		message := readIMServerMessage(t, conn)
		if message.Type == want {
			return message
		}
	}
	t.Fatalf("did not receive %q", want)
	return imws.ServerMessage{}
}

func TestPresenceAndFriendshipEventsOverRealWebSocket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	presenceRepo, err := repo.NewPresenceRepo(redisClient, repo.DefaultPresenceTTL)
	if err != nil {
		t.Fatal(err)
	}
	hub := imws.NewHub()
	coordinator := NewPresenceCoordinator(
		presenceRepo,
		acceptedFriendMap{7: {8}, 8: {7}},
		hub,
	)
	hub.SetPresenceObserver(coordinator)
	go coordinator.Run()
	go hub.Run()
	t.Cleanup(func() {
		hub.Stop()
		coordinator.Stop()
		_ = redisClient.Close()
	})

	origins, err := realtime.ParseAllowedOrigins(imwsTestOrigin)
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.GET("/ws/im", imws.HandleWebSocket(hub, imwsTestJWTSecret, origins))
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	friendConn := dialIMTest(t, wsURL, generateIMTestToken(t, 8))
	defer friendConn.Close()
	_ = readIMServerMessage(t, friendConn)

	userConn := dialIMTest(t, wsURL, generateIMTestToken(t, 7))
	_ = readIMServerMessage(t, userConn)
	online := readIMServerMessage(t, friendConn)
	assertPresenceMessage(t, online, 7, PresenceOnline)

	friendshipPublisher := NewRealtimeFriendshipPublisher(realtimeUserRepoStub{users: map[uint]model.User{
		7: {ID: 7, Username: "alice", Nickname: "Alice"},
		8: {ID: 8, Username: "bob", Nickname: "Bob"},
	}}, hub)
	updatedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := friendshipPublisher.PublishFriendshipUpdated(context.Background(), FriendshipUpdatedEvent{
		FriendshipID: 31, UserLowID: 7, UserHighID: 8, RequestedBy: 7,
		Status: model.FriendshipStatusAccepted, UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	userEvent := readIMServerMessageOfType(t, userConn, imws.MsgFriendshipUpdated)
	friendEvent := readIMServerMessageOfType(t, friendConn, imws.MsgFriendshipUpdated)
	assertFriendshipPeer(t, userEvent, 8)
	assertFriendshipPeer(t, friendEvent, 7)

	if err := userConn.UnderlyingConn().Close(); err != nil {
		t.Fatal(err)
	}
	offline := readIMServerMessage(t, friendConn)
	assertPresenceMessage(t, offline, 7, PresenceOffline)
}

func assertPresenceMessage(t *testing.T, message imws.ServerMessage, userID uint, status PresenceStatus) {
	t.Helper()
	if message.Type != imws.MsgPresence || message.Timestamp == 0 {
		t.Fatalf("presence message = %#v", message)
	}
	var data PresenceEventData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.UserID != userID || data.Status != status {
		t.Fatalf("presence data = %#v", data)
	}
}

func assertFriendshipPeer(t *testing.T, message imws.ServerMessage, peerID uint) {
	t.Helper()
	if message.Type != imws.MsgFriendshipUpdated || message.Timestamp == 0 {
		t.Fatalf("friendship message = %#v", message)
	}
	var data FriendshipEventData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.FriendshipID != 31 || data.Status != model.FriendshipStatusAccepted || data.Peer.ID != peerID || data.RequestedBy != 7 {
		t.Fatalf("friendship data = %#v", data)
	}
}
