package ws

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"trpggame/internal/middleware"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // 开发环境允许所有来源
	},
}

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
// 错误码：1500 缺失 token、1501 token 校验失败、1502 非法 room_id、1503 无房间访问权。
func HandleWebSocket(hub *Hub, secret string, authz RoomAuthorizer) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Query("token")
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"code": 1500, "message": "missing token"})
			return
		}

		claims, err := middleware.ValidateToken(token, secret)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"code": 1501, "message": "invalid token"})
			return
		}

		roomIDStr := c.Query("room_id")
		value, parseErr := strconv.ParseUint(roomIDStr, 10, 64)
		if parseErr != nil || value == 0 || uint64(uint(value)) != value {
			c.JSON(http.StatusBadRequest, gin.H{"code": 1502, "message": "invalid room_id"})
			return
		}
		roomID := uint(value)

		if err := authz.Authorize(c.Request.Context(), claims.UserID, roomID); err != nil {
			log.Printf("[WS] User %d denied access to room %d: %v", claims.UserID, roomID, err)
			c.JSON(http.StatusForbidden, gin.H{"code": 1503, "message": "room access denied"})
			return
		}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Printf("[WS] Upgrade error: %v", err)
			return
		}

		client := NewClient(hub, conn, claims.UserID, roomID)
		hub.register <- client

		go client.writePump()
		go client.readPump()
	}
}
