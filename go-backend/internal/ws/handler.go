package ws

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"trpggame/internal/realtime"
)

const (
	wsErrorMissingToken     = 1500
	wsErrorInvalidToken     = 1501
	wsErrorInvalidRoomID    = 1502
	wsErrorRoomAccessDenied = 1503
	wsErrorOriginNotAllowed = 1508
)

// RoomAuthorizer 校验用户是否有权订阅某房间。实现方通常在 main 中基于游戏仓储注入。
type RoomAuthorizer interface {
	Authorize(ctx context.Context, userID, roomID uint) error
}

// HandleWebSocket 处理 WebSocket 升级请求：JWT 鉴权 + 房间订阅校验。
//
// 浏览器端 WebSocket 无法携带 Authorization 头，因此 Access Token 通过查询参数传入：
//
//	GET /ws?token=<jwt>&room_id=<id>
//
// 错误码：1500 缺失 token、1501 token 校验失败、1502 非法 room_id、1503 无房间访问权、1508 Origin 不允许。
func HandleWebSocket(hub *Hub, secret string, origins *realtime.OriginSet, authz RoomAuthorizer) gin.HandlerFunc {
	upgrader := realtime.NewUpgrader(origins)

	return func(c *gin.Context) {
		if origins == nil || !origins.Allows(c.Request) {
			c.JSON(http.StatusForbidden, gin.H{"code": wsErrorOriginNotAllowed, "message": "origin not allowed"})
			return
		}

		claims, err := realtime.AuthenticateQueryToken(c.Query("token"), secret)
		if err != nil {
			var authError *realtime.AuthError
			if errors.As(err, &authError) && authError.Failure == realtime.AuthMissingToken {
				c.JSON(http.StatusUnauthorized, gin.H{"code": wsErrorMissingToken, "message": "missing token"})
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{"code": wsErrorInvalidToken, "message": "invalid token"})
			return
		}

		roomIDStr := c.Query("room_id")
		value, parseErr := strconv.ParseUint(roomIDStr, 10, 64)
		if parseErr != nil || value == 0 || uint64(uint(value)) != value {
			c.JSON(http.StatusBadRequest, gin.H{"code": wsErrorInvalidRoomID, "message": "invalid room_id"})
			return
		}
		roomID := uint(value)

		if err := authz.Authorize(c.Request.Context(), claims.UserID, roomID); err != nil {
			log.Printf("[WS] User %d denied access to room %d: %v", claims.UserID, roomID, err)
			c.JSON(http.StatusForbidden, gin.H{"code": wsErrorRoomAccessDenied, "message": "room access denied"})
			return
		}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Printf("[WS] Upgrade error: %v", err)
			return
		}

		client := NewClient(hub, conn, claims.UserID, roomID)
		if hub == nil || !hub.Register(client) {
			deadline := time.Now().Add(writeWait)
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(
					websocket.CloseTryAgainLater,
					realtime.CloseReasonServiceUnavailable,
				),
				deadline,
			)
			client.closeNow()
			return
		}

		go client.writePump()
		go client.readPump()
	}
}
