package imws

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"trpggame/internal/realtime"
)

// HandleWebSocket 处理用户级 IM WebSocket 升级请求。
func HandleWebSocket(hub *Hub, secret string, origins *realtime.OriginSet) gin.HandlerFunc {
	return handleWebSocketWithOptions(hub, secret, origins, defaultClientOptions())
}

func handleWebSocketWithOptions(
	hub *Hub,
	secret string,
	origins *realtime.OriginSet,
	options clientOptions,
) gin.HandlerFunc {
	upgrader := realtime.NewUpgrader(origins)

	return func(c *gin.Context) {
		if origins == nil || !origins.Allows(c.Request) {
			c.JSON(http.StatusForbidden, gin.H{
				"code": ErrorCodeOriginNotAllowed, "message": "origin not allowed",
			})
			return
		}

		claims, err := realtime.AuthenticateQueryToken(c.Query("token"), secret)
		if err != nil {
			var authError *realtime.AuthError
			if errors.As(err, &authError) && authError.Failure == realtime.AuthMissingToken {
				c.JSON(http.StatusUnauthorized, gin.H{
					"code": ErrorCodeMissingToken, "message": "missing token",
				})
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{
				"code": ErrorCodeInvalidToken, "message": "invalid token",
			})
			return
		}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Printf("[IMWS] Upgrade error: %v", err)
			return
		}

		client := newClientWithOptions(hub, conn, claims.UserID, options)
		if hub == nil || !hub.Register(client) {
			deadline := time.Now().Add(options.writeWait)
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseTryAgainLater, CloseReasonServiceUnavailable),
				deadline,
			)
			client.closeNow()
			return
		}

		go client.writePump()
		go client.readPump()
	}
}
