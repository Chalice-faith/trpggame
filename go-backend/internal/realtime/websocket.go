package realtime

import (
	"net/http"

	"github.com/gorilla/websocket"
)

const websocketBufferSize = 4096

const (
	// CloseCodeConnectionReplaced 表示同一实时通道中的旧连接已被新连接接管。
	CloseCodeConnectionReplaced = 4001
	// CloseReasonConnectionReplaced 是连接接管关闭帧的稳定原因文本。
	CloseReasonConnectionReplaced = "connection_replaced"
	// CloseReasonServiceUnavailable 用于升级成功后 Hub 已不可用的关闭帧。
	CloseReasonServiceUnavailable = "service unavailable"
)

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
