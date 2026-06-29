package ws

import (
	"encoding/json"
	"log"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// 写入超时
	writeWait = 10 * time.Second

	// 读取 Pong 超时
	pongWait = 60 * time.Second

	// 心跳间隔（必须小于 pongWait）
	pingPeriod = (pongWait * 9) / 10

	// 最大消息大小
	maxMessageSize = 8192

	// 发送缓冲区大小
	sendBufferSize = 256
)

// Client 代表一个 WebSocket 连接
type Client struct {
	Hub    *Hub
	Conn   *websocket.Conn
	UserID uint
	RoomID uint
	Send   chan []byte
	isAlive atomic.Bool
}

// NewClient 创建新的 WebSocket 客户端
func NewClient(hub *Hub, conn *websocket.Conn, userID, roomID uint) *Client {
	c := &Client{
		Hub:    hub,
		Conn:   conn,
		UserID: userID,
		RoomID: roomID,
		Send:   make(chan []byte, sendBufferSize),
	}
	c.isAlive.Store(true)
	return c
}

// readPump 从 WebSocket 连接读取消息并分发
func (c *Client) readPump() {
	defer func() {
		c.Hub.unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		c.isAlive.Store(true)
		return nil
	})

	for {
		_, raw, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[WS] Read error: %v", err)
			}
			break
		}

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("[WS] Unmarshal error: %v", err)
			continue
		}

		// 设置 UserID/RoomID（客户端可能不传）
		msg.UserID = c.UserID
		msg.RoomID = c.RoomID

		// 处理心跳
		if msg.Type == MsgPing {
			pong := &Message{Type: MsgPong, Timestamp: time.Now().UnixMilli()}
			pongData, _ := json.Marshal(pong)
			select {
			case c.Send <- pongData:
			default:
			}
			continue
		}

		// 其他消息类型转发到 Hub 广播通道（后续由 game_service 处理）
		c.Hub.broadcast <- &msg
	}
}

// writePump 向 WebSocket 连接写入消息
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub 关闭了通道
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("[WS] Write error: %v", err)
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
