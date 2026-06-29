package ws

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// Hub 管理所有 WebSocket 连接
type Hub struct {
	// 按房间分组：roomID -> userID -> *Client
	rooms map[uint]map[uint]*Client

	// 全局注册/注销通道
	register   chan *Client
	unregister chan *Client

	// 广播消息通道
	broadcast chan *Message

	mu    sync.RWMutex
	stop  chan struct{}
	seq   int64 // 全局消息序号
}

// NewHub 创建新的 Hub 实例
func NewHub() *Hub {
	return &Hub{
		rooms:      make(map[uint]map[uint]*Client),
		register:   make(chan *Client, 256),
		unregister: make(chan *Client, 256),
		broadcast:  make(chan *Message, 512),
		stop:       make(chan struct{}),
	}
}

// Run 启动 Hub 主循环
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if h.rooms[client.RoomID] == nil {
				h.rooms[client.RoomID] = make(map[uint]*Client)
			}
			h.rooms[client.RoomID][client.UserID] = client
			h.mu.Unlock()
			log.Printf("[WS] User %d joined room %d", client.UserID, client.RoomID)

		case client := <-h.unregister:
			h.mu.Lock()
			if clients, ok := h.rooms[client.RoomID]; ok {
				if _, exists := clients[client.UserID]; exists {
					delete(clients, client.UserID)
					close(client.Send)
					if len(clients) == 0 {
						delete(h.rooms, client.RoomID)
					}
				}
			}
			h.mu.Unlock()
			log.Printf("[WS] User %d left room %d", client.UserID, client.RoomID)

		case msg := <-h.broadcast:
			h.mu.RLock()
			if clients, ok := h.rooms[msg.RoomID]; ok {
				data, err := json.Marshal(msg)
				if err != nil {
					log.Printf("[WS] Marshal error: %v", err)
					continue
				}
				for _, client := range clients {
					select {
					case client.Send <- data:
					default:
						// 客户端发送缓冲区满，跳过
						log.Printf("[WS] Client %d send buffer full, dropping message", client.UserID)
					}
				}
			}
			h.mu.RUnlock()

		case <-h.stop:
			log.Println("[WS] Hub stopping...")
			return
		}
	}
}

// Stop 停止 Hub
func (h *Hub) Stop() {
	close(h.stop)
}

// BroadcastToRoom 向房间内所有玩家广播消息
func (h *Hub) BroadcastToRoom(roomID uint, msgType MessageType, data json.RawMessage) {
	h.seq++
	msg := &Message{
		Type:      msgType,
		RoomID:    roomID,
		Data:      data,
		Timestamp: time.Now().UnixMilli(),
		Seq:       h.seq,
	}
	h.broadcast <- msg
}

// SendToUser 向房间内指定玩家发送消息
func (h *Hub) SendToUser(roomID, userID uint, msgType MessageType, data json.RawMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if clients, ok := h.rooms[roomID]; ok {
		if client, exists := clients[userID]; exists {
			h.seq++
			msg := &Message{
				Type:      msgType,
				RoomID:    roomID,
				UserID:    userID,
				Data:      data,
				Timestamp: time.Now().UnixMilli(),
				Seq:       h.seq,
			}
			payload, err := json.Marshal(msg)
			if err != nil {
				log.Printf("[WS] Marshal error: %v", err)
				return
			}
			select {
			case client.Send <- payload:
			default:
				log.Printf("[WS] Client %d send buffer full", userID)
			}
		}
	}
}

// RoomClients 返回房间内所有客户端
func (h *Hub) RoomClients(roomID uint) map[uint]*Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if clients, ok := h.rooms[roomID]; ok {
		return clients
	}
	return nil
}
