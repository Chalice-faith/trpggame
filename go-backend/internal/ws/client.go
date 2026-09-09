package ws

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"trpggame/internal/realtime"
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

	closeCommandCapacity = 1
)

type clientCloseCommand struct {
	code   int
	reason string
}

// Client 代表一个 WebSocket 连接
type Client struct {
	Hub          *Hub
	Conn         *websocket.Conn
	UserID       uint
	RoomID       uint
	ConnectionID string
	Send         chan []byte

	closeCommand chan clientCloseCommand
	done         chan struct{}
	closeOnce    sync.Once
	isAlive      atomic.Bool
	closed       atomic.Bool
}

// NewClient 创建新的 WebSocket 客户端
func NewClient(hub *Hub, conn *websocket.Conn, userID, roomID uint) *Client {
	c := &Client{
		Hub:          hub,
		Conn:         conn,
		UserID:       userID,
		RoomID:       roomID,
		ConnectionID: uuid.NewString(),
		Send:         make(chan []byte, sendBufferSize),
		closeCommand: make(chan clientCloseCommand, closeCommandCapacity),
		done:         make(chan struct{}),
	}
	c.isAlive.Store(true)
	return c
}

// readPump 从 WebSocket 连接读取消息并分发
func (c *Client) readPump() {
	defer func() {
		c.closeNow()
		if c.Hub != nil {
			c.Hub.Unregister(c)
		}
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
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				realtime.CloseCodeConnectionReplaced,
			) {
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
			if c.Hub != nil {
				c.Hub.sendToClient(c, pongData)
			}
			continue
		}

		// 其他客户端消息交给 Hub 分发（例如 sync 重连补推）；服务端事件由 Hub 主动推送，不回显。
		if c.Hub != nil {
			c.Hub.HandleInbound(c, &msg)
		}
	}
}

// writePump 向 WebSocket 连接写入消息
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.closeNow()
	}()

	for {
		// 接管关闭优先于积压的业务消息，避免旧连接在替换后继续收到投递。
		select {
		case command := <-c.closeCommand:
			c.writeClose(command)
			return
		default:
		}
		select {
		case message := <-c.Send:
			if err := c.writeText(message); err != nil {
				log.Printf("[WS] Write error: %v", err)
				return
			}
		case command := <-c.closeCommand:
			c.writeClose(command)
			return
		case <-ticker.C:
			deadline := time.Now().Add(writeWait)
			if err := c.Conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) writeClose(command clientCloseCommand) {
	deadline := time.Now().Add(writeWait)
	_ = c.Conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(command.code, command.reason),
		deadline,
	)
}

func (c *Client) writeText(payload []byte) error {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.Conn.WriteMessage(websocket.TextMessage, payload)
}

func (c *Client) enqueue(payload []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.Send <- payload:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (c *Client) requestClose(command clientCloseCommand) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.closeCommand <- command:
	case <-c.done:
	default:
		c.closeNow()
	}
}

func (c *Client) closeNow() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.isAlive.Store(false)
		close(c.done)
		if c.Conn != nil {
			_ = c.Conn.Close()
		}
	})
}

func (c *Client) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return c.closed.Load()
	}
}
