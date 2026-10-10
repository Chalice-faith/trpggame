package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"trpggame/internal/config"
	"trpggame/internal/imws"
	"trpggame/internal/middleware"
	"trpggame/internal/realtime"
	"trpggame/internal/ws"
)

const dualChannelTestTimeout = 3 * time.Second

type dualChannelAuthorizer struct {
	userID uint
	roomID uint
}

func (a dualChannelAuthorizer) Authorize(_ context.Context, userID, roomID uint) error {
	if userID == a.userID && roomID == a.roomID {
		return nil
	}
	return errors.New("denied")
}

func TestSetupKeepsGameAndIMWebSocketConnectionsIndependent(t *testing.T) {
	const (
		jwtSecret = "dual-channel-test-secret"
		userID    = uint(7)
		roomID    = uint(41)
	)

	allowedOrigins, err := realtime.ParseAllowedOrigins("https://game.example.com")
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}
	gameHub := ws.NewHub()
	gameHub.SetGameActionHandler(func(context.Context, *ws.Client, ws.GameActionData) {})
	go gameHub.Run()
	t.Cleanup(gameHub.Stop)
	imHub := imws.NewHub()
	go imHub.Run()
	t.Cleanup(imHub.Stop)

	engine := Setup(
		&config.Config{
			JWT:      config.JWTConfig{Secret: jwtSecret, AccessTokenTTL: 15},
			Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
		},
		nil,
		WebSocketHandlers{
			Game: ws.HandleWebSocket(
				gameHub,
				jwtSecret,
				allowedOrigins,
				dualChannelAuthorizer{userID: userID, roomID: roomID},
			),
			IM: imws.HandleWebSocket(imHub, jwtSecret, allowedOrigins),
		},
		nil,
		nil,
		nil,
	)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	wsBaseURL := "ws" + strings.TrimPrefix(server.URL, "http")
	token, err := middleware.GenerateToken(userID, "investigator", jwtSecret, 15)
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	gameConnection := dialRouterWebSocket(t, wsBaseURL, "/ws", url.Values{
		"token":   []string{token},
		"room_id": []string{"41"},
	})
	defer gameConnection.Close()
	gameSubscribed := readRouterGameMessage(t, gameConnection)
	if gameSubscribed.Type != ws.MsgSubscribed || gameSubscribed.RoomID != roomID {
		t.Fatalf("game first message = %#v", gameSubscribed)
	}

	imConnection := dialRouterWebSocket(t, wsBaseURL, "/ws/im", url.Values{
		"token": []string{token},
	})
	defer imConnection.Close()
	imConnected := readRouterIMMessage(t, imConnection)
	if imConnected.Type != imws.MsgConnected {
		t.Fatalf("IM first message = %#v", imConnected)
	}
	var connectedData imws.ConnectedData
	if err := json.Unmarshal(imConnected.Data, &connectedData); err != nil {
		t.Fatalf("unmarshal IM connected data: %v", err)
	}
	if connectedData.UserID != userID || connectedData.ConnectionID == "" {
		t.Fatalf("IM connected data = %#v", connectedData)
	}

	newIMConnection := dialRouterWebSocket(t, wsBaseURL, "/ws/im", url.Values{
		"token": []string{token},
	})
	defer newIMConnection.Close()
	if message := readRouterIMMessage(t, newIMConnection); message.Type != imws.MsgConnected {
		t.Fatalf("new IM first message = %q", message.Type)
	}
	if message := readRouterIMMessage(t, imConnection); message.Type != imws.MsgConnectionReplaced {
		t.Fatalf("old IM replacement message = %q", message.Type)
	}
	assertRouterCloseCode(t, imConnection, realtime.CloseCodeConnectionReplaced)
	assertRouterGamePingPong(t, gameConnection)

	newGameConnection := dialRouterWebSocket(t, wsBaseURL, "/ws", url.Values{
		"token":   []string{token},
		"room_id": []string{"41"},
	})
	defer newGameConnection.Close()
	if message := readRouterGameMessage(t, newGameConnection); message.Type != ws.MsgSubscribed {
		t.Fatalf("new game first message = %q", message.Type)
	}
	assertRouterCloseCode(t, gameConnection, realtime.CloseCodeConnectionReplaced)
	assertRouterIMPingPong(t, newIMConnection)
	assertRouterGamePingPong(t, newGameConnection)
}

func dialRouterWebSocket(
	t *testing.T,
	baseURL, path string,
	query url.Values,
) *websocket.Conn {
	t.Helper()
	connection, response, err := websocket.DefaultDialer.Dial(baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("dial %s: status = %d, err = %v", path, status, err)
	}
	return connection
}

func readRouterGameMessage(t *testing.T, connection *websocket.Conn) ws.Message {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(dualChannelTestTimeout))
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatalf("read game message: %v", err)
	}
	var message ws.Message
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("unmarshal game message: %v", err)
	}
	return message
}

func readRouterIMMessage(t *testing.T, connection *websocket.Conn) imws.ServerMessage {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(dualChannelTestTimeout))
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatalf("read IM message: %v", err)
	}
	var message imws.ServerMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("unmarshal IM message: %v", err)
	}
	return message
}

func assertRouterCloseCode(t *testing.T, connection *websocket.Conn, wantCode int) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(dualChannelTestTimeout))
	_, _, err := connection.ReadMessage()
	if !websocket.IsCloseError(err, wantCode) {
		t.Fatalf("close error = %v, want code %d", err, wantCode)
	}
}

func assertRouterGamePingPong(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	if err := connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("write game ping: %v", err)
	}
	if message := readRouterGameMessage(t, connection); message.Type != ws.MsgPong {
		t.Fatalf("game ping response = %q, want pong", message.Type)
	}
}

func assertRouterIMPingPong(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	const requestID = "550e8400-e29b-41d4-a716-446655440000"
	if err := connection.WriteMessage(
		websocket.TextMessage,
		[]byte(`{"type":"ping","request_id":"`+requestID+`"}`),
	); err != nil {
		t.Fatalf("write IM ping: %v", err)
	}
	message := readRouterIMMessage(t, connection)
	if message.Type != imws.MsgPong || message.RequestID != requestID {
		t.Fatalf("IM ping response = %#v", message)
	}
}
