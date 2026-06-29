package ws

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // 开发环境允许所有来源
	},
}

// HandleWebSocket 处理 WebSocket 升级请求
// 期望 query 参数：user_id（后续改成 JWT token 鉴权）、room_id
func HandleWebSocket(hub *Hub) gin.HandlerFunc {
	return func(c *gin.Context) {
		// TODO: Phase 1 暂用 query 参数传 userId，后续改为 JWT token 鉴权
		userIDStr := c.Query("user_id")
		roomIDStr := c.Query("room_id")

		userID, err := strconv.ParseUint(userIDStr, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    1500,
				"message": "Invalid user_id",
			})
			return
		}

		roomID, err := strconv.ParseUint(roomIDStr, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    1501,
				"message": "Invalid room_id",
			})
			return
		}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Printf("[WS] Upgrade error: %v", err)
			return
		}

		client := NewClient(hub, conn, uint(userID), uint(roomID))
		hub.register <- client

		go client.writePump()
		go client.readPump()
	}
}
