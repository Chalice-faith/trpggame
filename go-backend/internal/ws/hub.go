package ws

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"
)

// recentCapacity 每个房间保留的近期服务端消息数，超出后丢弃最旧的，用于重连补推。
const recentCapacity = 200

// room 一个房间的订阅状态。内部字段只由 Hub.Run goroutine 触碰。
type room struct {
	clients map[uint]*Client
	seq     int64 // 按房间单调递增的消息序号
	recent  []Message
}

// deliverRequest 一次服务端 → 客户端投递请求。userID 为 0 表示广播给整个房间。
type deliverRequest struct {
	roomID    uint
	userID    uint
	msgType   MessageType
	data      json.RawMessage
	requestID string
}

// GameActionHandler 处理已通过 JWT 与房间订阅鉴权的客户端行动。
type GameActionHandler func(context.Context, *Client, GameActionData)

// Hub 管理所有 WebSocket 连接。
type Hub struct {
	// 按房间分组：roomID -> *room
	rooms map[uint]*room

	// 全局注册/注销通道
	register   chan *Client
	unregister chan *Client

	// 服务端 → 客户端投递通道
	deliver       chan deliverRequest
	actionHandler GameActionHandler

	mu   sync.RWMutex
	stop chan struct{}
}

// NewHub 创建新的 Hub 实例。
func NewHub() *Hub {
	return &Hub{
		rooms:      make(map[uint]*room),
		register:   make(chan *Client, 256),
		unregister: make(chan *Client, 256),
		deliver:    make(chan deliverRequest, 512),
		stop:       make(chan struct{}),
	}
}

// Run 启动 Hub 主循环。所有房间状态变更都在此 goroutine 内完成。
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.registerClient(client)

		case client := <-h.unregister:
			h.unregisterClient(client)

		case req := <-h.deliver:
			h.deliverTo(req)

		case <-h.stop:
			log.Println("[WS] Hub stopping...")
			h.closeAllClients()
			return
		}
	}
}

// Stop 停止 Hub 并关闭全部连接。
func (h *Hub) Stop() {
	close(h.stop)
}

// BroadcastToRoom 向房间内所有订阅客户端广播消息。
func (h *Hub) BroadcastToRoom(roomID uint, msgType MessageType, data json.RawMessage) {
	h.deliver <- deliverRequest{roomID: roomID, msgType: msgType, data: data}
}

// SendToUser 向房间内指定客户端发送消息。
func (h *Hub) SendToUser(roomID, userID uint, msgType MessageType, data json.RawMessage) {
	h.SendToUserWithRequestID(roomID, userID, msgType, data, "")
}

// SendToUserWithRequestID 向指定客户端发送带行动关联 ID 的事件。
func (h *Hub) SendToUserWithRequestID(
	roomID, userID uint,
	msgType MessageType,
	data json.RawMessage,
	requestID string,
) {
	h.deliver <- deliverRequest{
		roomID: roomID, userID: userID, msgType: msgType, data: data, requestID: requestID,
	}
}

// SendErrorToUser 向房间内指定客户端发送错误事件。
func (h *Hub) SendErrorToUser(roomID, userID uint, code int, message, requestID string) {
	data := mustJSON(ErrorData{Code: code, Message: message, RequestID: requestID})
	h.deliver <- deliverRequest{
		roomID: roomID, userID: userID, msgType: MsgError, data: data, requestID: requestID,
	}
}

// SetGameActionHandler 注入游戏行动处理器。应在 Hub.Run 启动前调用。
func (h *Hub) SetGameActionHandler(handler GameActionHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actionHandler = handler
}

// HandleInbound 处理客户端 → 服务端的非心跳消息，由 Client.readPump 调用。
func (h *Hub) HandleInbound(c *Client, msg *Message) {
	switch msg.Type {
	case MsgSync:
		h.handleSync(c, msg.Data)
	case MsgGameAction:
		h.handleGameAction(c, msg.Data)
	default:
		h.sendErrorTo(c, 1505, "unsupported message type: "+string(msg.Type))
	}
}

func (h *Hub) handleGameAction(c *Client, data json.RawMessage) {
	var request GameActionData
	if err := json.Unmarshal(data, &request); err != nil ||
		request.ExpectedTurn == nil || request.RequestID == "" || request.ActionText == "" {
		h.SendErrorToUser(c.RoomID, c.UserID, 1506, "invalid game action", request.RequestID)
		return
	}

	h.mu.RLock()
	handler := h.actionHandler
	h.mu.RUnlock()
	if handler == nil {
		h.SendErrorToUser(c.RoomID, c.UserID, 1507, "game action handler unavailable", request.RequestID)
		return
	}
	go handler(context.Background(), c, request)
}

func (h *Hub) registerClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if client.closed.Load() {
		// 连接在 register 入队后已结束，跳过注册（unregister 已先行或被延迟）。
		return
	}
	r := h.rooms[client.RoomID]
	if r == nil {
		r = &room{clients: make(map[uint]*Client)}
		h.rooms[client.RoomID] = r
	}
	r.clients[client.UserID] = client
	log.Printf("[WS] User %d subscribed to room %d", client.UserID, client.RoomID)

	// 订阅确认是客户端的首条消息，直接投递（不占用房间序号）。
	payload, err := json.Marshal(Message{
		Type:      MsgSubscribed,
		RoomID:    client.RoomID,
		Data:      mustJSON(SubscribedData{RoomID: client.RoomID, Seq: r.seq}),
		Timestamp: time.Now().UnixMilli(),
	})
	if err != nil {
		log.Printf("[WS] Marshal error: %v", err)
		return
	}
	h.enqueue(client, payload)
}

func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	client.closed.Store(true)
	r, ok := h.rooms[client.RoomID]
	if !ok {
		return
	}
	if _, exists := r.clients[client.UserID]; !exists {
		return
	}
	delete(r.clients, client.UserID)
	close(client.Send)
	if len(r.clients) == 0 {
		delete(h.rooms, client.RoomID)
	}
	log.Printf("[WS] User %d left room %d", client.UserID, client.RoomID)
}

func (h *Hub) deliverTo(req deliverRequest) {
	h.mu.Lock()
	defer h.mu.Unlock()

	r := h.rooms[req.roomID]
	if r == nil {
		return
	}
	r.seq++
	msg := Message{
		Type:      req.msgType,
		RoomID:    req.roomID,
		Seq:       r.seq,
		Data:      req.data,
		Timestamp: time.Now().UnixMilli(),
		RequestID: req.requestID,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[WS] Marshal error: %v", err)
		return
	}
	r.recent = appendRecent(r.recent, msg, recentCapacity)

	if req.userID != 0 {
		if client, ok := r.clients[req.userID]; ok {
			h.enqueue(client, payload)
		}
		return
	}
	for _, client := range r.clients {
		h.enqueue(client, payload)
	}
}

func (h *Hub) handleSync(c *Client, data json.RawMessage) {
	var req SyncRequestData
	if len(data) > 0 {
		if err := json.Unmarshal(data, &req); err != nil {
			h.sendErrorTo(c, 1504, "invalid sync request")
			return
		}
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	r := h.rooms[c.RoomID]
	if r == nil {
		return
	}
	if _, ok := r.clients[c.UserID]; !ok {
		return // 已注销
	}
	messages := recentSince(r.recent, req.SinceSeq)
	payload, err := json.Marshal(Message{
		Type:      MsgSyncBatch,
		RoomID:    c.RoomID,
		Data:      mustJSON(SyncBatchData{Messages: messages, NextSeq: r.seq}),
		Timestamp: time.Now().UnixMilli(),
	})
	if err != nil {
		log.Printf("[WS] Marshal error: %v", err)
		return
	}
	h.enqueue(c, payload)
}

func (h *Hub) sendErrorTo(c *Client, code int, message string) {
	payload, err := json.Marshal(Message{
		Type:      MsgError,
		RoomID:    c.RoomID,
		Data:      mustJSON(ErrorData{Code: code, Message: message}),
		Timestamp: time.Now().UnixMilli(),
	})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if r, ok := h.rooms[c.RoomID]; ok {
		if _, exists := r.clients[c.UserID]; exists {
			h.enqueue(c, payload)
		}
	}
}

// enqueue 非阻塞投递。缓冲区满时丢弃该消息，客户端可依赖 seq 通过 sync 补推。
// 调用方必须持有 h.mu 的读锁或写锁，避免与 unregisterClient 的 close 竞争。
func (h *Hub) enqueue(client *Client, payload []byte) {
	select {
	case client.Send <- payload:
	default:
		log.Printf("[WS] Client %d send buffer full, dropping message", client.UserID)
	}
}

func (h *Hub) closeAllClients() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.rooms {
		for _, client := range r.clients {
			close(client.Send)
		}
	}
	h.rooms = make(map[uint]*room)
}

func appendRecent(recent []Message, msg Message, limit int) []Message {
	recent = append(recent, msg)
	if len(recent) > limit {
		recent = recent[len(recent)-limit:]
	}
	return recent
}

func recentSince(recent []Message, sinceSeq int64) []Message {
	out := make([]Message, 0, len(recent))
	for _, m := range recent {
		if m.Seq > sinceSeq {
			out = append(out, m)
		}
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}
