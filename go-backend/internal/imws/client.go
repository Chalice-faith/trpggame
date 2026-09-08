package imws

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	writeWait       = 10 * time.Second
	pongWait        = 60 * time.Second
	pingPeriod      = (pongWait * 9) / 10
	sendBufferSize  = 256
	closeCommandCap = 1
)

type closeCommand struct {
	payload []byte
	code    int
	reason  string
}

// Client 代表一个用户级 IM WebSocket 连接。
type Client struct {
	Hub          *Hub
	Conn         *websocket.Conn
	UserID       uint
	ConnectionID string

	send         chan []byte
	closeCommand chan closeCommand
	done         chan struct{}
	closeOnce    sync.Once
}

// NewClient 创建带有不可复用 connection_id 的 IM Client。
func NewClient(hub *Hub, conn *websocket.Conn, userID uint) *Client {
	return newClient(hub, conn, userID, uuid.NewString(), sendBufferSize)
}

func newClient(hub *Hub, conn *websocket.Conn, userID uint, connectionID string, bufferSize int) *Client {
	return &Client{
		Hub:          hub,
		Conn:         conn,
		UserID:       userID,
		ConnectionID: connectionID,
		send:         make(chan []byte, bufferSize),
		closeCommand: make(chan closeCommand, closeCommandCap),
		done:         make(chan struct{}),
	}
}

func (c *Client) readPump() {
	defer func() {
		c.closeNow()
		if c.Hub != nil {
			c.Hub.Unregister(c)
		}
	}()

	c.Conn.SetReadLimit(MaxTextMessageSize)
	_ = c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		return c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		messageType, raw, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(
				err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				CloseCodeConnectionReplaced,
			) {
				log.Printf("[IMWS] Read error for user %d connection %s: %v", c.UserID, c.ConnectionID, err)
			}
			return
		}
		if messageType != websocket.TextMessage {
			c.requestClose(closeCommand{code: websocket.CloseUnsupportedData, reason: "unsupported data"})
			c.waitForClose()
			return
		}

		message, decodeErr := DecodeClientMessage(raw)
		if decodeErr != nil {
			code := ErrorCodeInvalidMessage
			messageText := "invalid message"
			if errors.Is(decodeErr, ErrMalformedClientMessage) {
				code = ErrorCodeMalformedMessage
				messageText = "malformed message"
			}
			c.sendError(code, messageText, "")
			continue
		}

		switch message.Type {
		case MsgPing:
			if !validPingData(message.Data) {
				c.sendError(ErrorCodeInvalidMessage, "invalid message", message.RequestID)
				continue
			}
			payload, marshalErr := MarshalServerMessage(
				MsgPong,
				message.RequestID,
				time.Now().UnixMilli(),
				nil,
			)
			if marshalErr == nil && c.Hub != nil {
				c.Hub.sendToClient(c, payload)
			}
		default:
			c.sendError(
				ErrorCodeUnsupportedMessageType,
				"unsupported message type",
				message.RequestID,
			)
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.closeNow()
	}()

	for {
		select {
		case payload := <-c.send:
			if err := c.writeText(payload); err != nil {
				return
			}
		case command := <-c.closeCommand:
			if len(command.payload) > 0 {
				if err := c.writeText(command.payload); err != nil {
					return
				}
			}
			deadline := time.Now().Add(writeWait)
			_ = c.Conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(command.code, command.reason),
				deadline,
			)
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

func (c *Client) writeText(payload []byte) error {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.Conn.WriteMessage(websocket.TextMessage, payload)
}

func (c *Client) sendError(code int, message, requestID string) {
	payload, err := MarshalServerMessage(
		MsgError,
		requestID,
		time.Now().UnixMilli(),
		ErrorData{Code: code, Message: message},
	)
	if err == nil && c.Hub != nil {
		c.Hub.sendToClient(c, payload)
	}
}

func (c *Client) enqueue(payload []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- payload:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (c *Client) requestClose(command closeCommand) {
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

func (c *Client) waitForClose() {
	timer := time.NewTimer(writeWait)
	defer timer.Stop()
	select {
	case <-c.done:
	case <-timer.C:
		c.closeNow()
	}
}

func (c *Client) closeNow() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.Conn != nil {
			_ = c.Conn.Close()
		}
	})
}

func validPingData(data json.RawMessage) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(trimmed, &object) == nil && len(object) == 0
}
