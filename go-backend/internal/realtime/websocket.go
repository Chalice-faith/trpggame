package realtime

import (
	"net/http"

	"github.com/gorilla/websocket"
)

const websocketBufferSize = 4096

// NewUpgrader 创建使用统一 Origin 白名单的 WebSocket Upgrader。
func NewUpgrader(origins *OriginSet) websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  websocketBufferSize,
		WriteBufferSize: websocketBufferSize,
		CheckOrigin: func(request *http.Request) bool {
			return origins != nil && origins.Allows(request)
		},
	}
}
