package imws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/realtime"
	"trpggame/internal/repo"
	"trpggame/internal/service"
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

func TestPresenceAndFriendshipEventsOverRealWebSocket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	presenceRepo, err := repo.NewPresenceRepo(redisClient, repo.DefaultPresenceTTL)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	coordinator := service.NewPresenceCoordinator(
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

	origins, err := realtime.ParseAllowedOrigins(testAllowedOrigin)
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.GET("/ws/im", HandleWebSocket(hub, testJWTSecret, origins))
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	friendConn, _, err := dialIM(t, wsURL, generateTestToken(t, 8), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer friendConn.Close()
	_ = readServerMessage(t, friendConn)

	userConn, _, err := dialIM(t, wsURL, generateTestToken(t, 7), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = readServerMessage(t, userConn)
	online := readServerMessage(t, friendConn)
	assertPresenceMessage(t, online, 7, service.PresenceOnline)

	friendshipPublisher := service.NewRealtimeFriendshipPublisher(realtimeUserRepoStub{users: map[uint]model.User{
		7: {ID: 7, Username: "alice", Nickname: "Alice"},
		8: {ID: 8, Username: "bob", Nickname: "Bob"},
	}}, hub)
	updatedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := friendshipPublisher.PublishFriendshipUpdated(context.Background(), service.FriendshipUpdatedEvent{
		FriendshipID: 31, UserLowID: 7, UserHighID: 8, RequestedBy: 7,
		Status: model.FriendshipStatusAccepted, UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	userEvent := readServerMessageOfType(t, userConn, MsgFriendshipUpdated)
	friendEvent := readServerMessageOfType(t, friendConn, MsgFriendshipUpdated)
	assertFriendshipPeer(t, userEvent, 8)
	assertFriendshipPeer(t, friendEvent, 7)

	if err := userConn.UnderlyingConn().Close(); err != nil {
		t.Fatal(err)
	}
	offline := readServerMessage(t, friendConn)
	assertPresenceMessage(t, offline, 7, service.PresenceOffline)
}

func readServerMessageOfType(t *testing.T, conn *websocket.Conn, want MessageType) ServerMessage {
	t.Helper()
	for range 4 {
		message := readServerMessage(t, conn)
		if message.Type == want {
			return message
		}
	}
	t.Fatalf("did not receive %q", want)
	return ServerMessage{}
}

func assertPresenceMessage(t *testing.T, message ServerMessage, userID uint, status service.PresenceStatus) {
	t.Helper()
	if message.Type != MsgPresence || message.Timestamp == 0 {
		t.Fatalf("presence message = %#v", message)
	}
	var data service.PresenceEventData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.UserID != userID || data.Status != status {
		t.Fatalf("presence data = %#v", data)
	}
}

func assertFriendshipPeer(t *testing.T, message ServerMessage, peerID uint) {
	t.Helper()
	if message.Type != MsgFriendshipUpdated || message.Timestamp == 0 {
		t.Fatalf("friendship message = %#v", message)
	}
	var data service.FriendshipEventData
	if err := json.Unmarshal(message.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.FriendshipID != 31 || data.Status != model.FriendshipStatusAccepted || data.Peer.ID != peerID || data.RequestedBy != 7 {
		t.Fatalf("friendship data = %#v", data)
	}
}
