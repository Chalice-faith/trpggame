package ws

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"trpggame/internal/realtime"
)

// recentCapacity 每个房间保留的近期服务端消息数，超出后丢弃最旧的，用于重连补推。
const recentCapacity = 200

const (
	hubCommandBuffer = 256
	deliveryBuffer   = 512
)

// room 一个房间的订阅状态。内部字段只在持有 Hub.mu 时访问。
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

type registerRequest struct {
	client *Client
	result chan bool
}

// GameActionHandler 处理已通过 JWT 与房间订阅鉴权的客户端行动。
type GameActionHandler func(context.Context, *Client, GameActionData)

// Hub 管理所有 WebSocket 连接。
type Hub struct {
	// 按房间分组：roomID -> *room
	rooms map[uint]*room

	// 全局注册/注销通道
	register   chan registerRequest
	unregister chan *Client

	// 服务端 → 客户端投递通道
	deliver       chan deliverRequest
	actionHandler GameActionHandler

	mu       sync.RWMutex
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewHub 创建新的 Hub 实例。
func NewHub() *Hub {
	return &Hub{
		rooms:      make(map[uint]*room),
		register:   make(chan registerRequest, hubCommandBuffer),
		unregister: make(chan *Client, hubCommandBuffer),
		deliver:    make(chan deliverRequest, deliveryBuffer),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Run 启动 Hub 主循环。所有房间状态变更都在此 goroutine 内完成。
func (h *Hub) Run() {
	defer close(h.done)
	for {
		select {
		case request := <-h.register:
			request.result <- h.registerClient(request.client)

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
	if h == nil {
		return
	}
	h.stopOnce.Do(func() {
		close(h.stop)
	})
	<-h.done
}

// Register 确认式注册 Client；Hub 停止后立即返回 false。
func (h *Hub) Register(client *Client) bool {
	if h == nil || client == nil {
		return false
	}
	request := registerRequest{client: client, result: make(chan bool, 1)}
	select {
	case <-h.stop:
		return false
	case <-h.done:
		return false
	default:
	}
	select {
	case h.register <- request:
	case <-h.stop:
		return false
	case <-h.done:
		return false
	}
	select {
	case registered := <-request.result:
		return registered
	case <-h.done:
		return false
	}
}

// Unregister 注销 Client；被替换连接的延迟注销不会删除当前连接。
func (h *Hub) Unregister(client *Client) {
	if h == nil || client == nil {
		return
	}
	select {
	case h.unregister <- client:
	case <-h.stop:
	case <-h.done:
	}
}

// BroadcastToRoom 向房间内所有订阅客户端广播消息。
func (h *Hub) BroadcastToRoom(roomID uint, msgType MessageType, data json.RawMessage) {
	h.queueDelivery(deliverRequest{roomID: roomID, msgType: msgType, data: data})
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
	h.queueDelivery(deliverRequest{
		roomID: roomID, userID: userID, msgType: msgType, data: data, requestID: requestID,
	})
}

// SendErrorToUser 向房间内指定客户端发送错误事件。
func (h *Hub) SendErrorToUser(roomID, userID uint, code int, message, requestID string) {
	data := mustJSON(ErrorData{Code: code, Message: message, RequestID: requestID})
	h.queueDelivery(deliverRequest{
		roomID: roomID, userID: userID, msgType: MsgError, data: data, requestID: requestID,
	})
}

func (h *Hub) queueDelivery(request deliverRequest) {
	if h == nil {
		return
	}
	select {
	case <-h.stop:
		return
	case <-h.done:
		return
	default:
	}
	select {
	case h.deliver <- request:
	case <-h.stop:
	case <-h.done:
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
	if msg == nil || !h.isCurrentClient(c) {
		return
	}
	switch msg.Type {
	case MsgSync:
		h.handleSync(c, msg.Data)
	case MsgGameAction:
		h.handleGameAction(c, msg.Data)
	default:
		h.sendErrorTo(c, 1505, "unsupported message type: "+string(msg.Type), "")
	}
}

func (h *Hub) handleGameAction(c *Client, data json.RawMessage) {
	var request GameActionData
	if err := json.Unmarshal(data, &request); err != nil ||
		request.ExpectedTurn == nil || request.RequestID == "" || request.ActionText == "" {
		h.sendErrorTo(c, 1506, "invalid game action", request.RequestID)
		return
	}

	h.mu.RLock()
	handler := h.actionHandler
	h.mu.RUnlock()
	if handler == nil {
		h.sendErrorTo(c, 1507, "game action handler unavailable", request.RequestID)
		return
	}
	go func() {
		if h.isCurrentClient(c) {
			handler(context.Background(), c, request)
		}
	}()
}

func (h *Hub) registerClient(client *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if client == nil || client.UserID == 0 || client.RoomID == 0 ||
		client.ConnectionID == "" || client.isClosed() {
		return false
	}
	r := h.rooms[client.RoomID]
	if r == nil {
		r = &room{clients: make(map[uint]*Client)}
		h.rooms[client.RoomID] = r
	}

	// 订阅确认是客户端的首条消息，直接投递（不占用房间序号）。
	payload, err := json.Marshal(Message{
		Type:      MsgSubscribed,
		RoomID:    client.RoomID,
		Data:      mustJSON(SubscribedData{RoomID: client.RoomID, Seq: r.seq}),
		Timestamp: time.Now().UnixMilli(),
	})
	if err != nil {
		log.Printf("[WS] Marshal error: %v", err)
		return false
	}

	previous := r.clients[client.UserID]
	r.clients[client.UserID] = client
	if !client.enqueue(payload) {
		if previous == nil {
			delete(r.clients, client.UserID)
			if len(r.clients) == 0 {
				delete(h.rooms, client.RoomID)
			}
		} else {
			r.clients[client.UserID] = previous
		}
		client.closeNow()
		return false
	}

	if previous != nil && previous != client {
		previous.requestClose(clientCloseCommand{
			code:   realtime.CloseCodeConnectionReplaced,
			reason: realtime.CloseReasonConnectionReplaced,
		})
	}
	log.Printf(
		"[WS] User %d subscribed to room %d with connection %s",
		client.UserID,
		client.RoomID,
		client.ConnectionID,
	)
	return true
}

func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	client.closeNow()
	r, ok := h.rooms[client.RoomID]
	if !ok {
		return
	}
	current, exists := r.clients[client.UserID]
	if !exists || current != client || current.ConnectionID != client.ConnectionID {
		return
	}
	delete(r.clients, client.UserID)
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
			h.sendErrorTo(c, 1504, "invalid sync request", "")
			return
		}
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	r := h.rooms[c.RoomID]
	if r == nil {
		return
	}
	current, ok := r.clients[c.UserID]
	if !ok || current != c || current.ConnectionID != c.ConnectionID {
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

func (h *Hub) sendErrorTo(c *Client, code int, message, requestID string) {
	if c == nil {
		return
	}
	payload, err := json.Marshal(Message{
		Type:      MsgError,
		RoomID:    c.RoomID,
		Data:      mustJSON(ErrorData{Code: code, Message: message, RequestID: requestID}),
		Timestamp: time.Now().UnixMilli(),
		RequestID: requestID,
	})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if r, ok := h.rooms[c.RoomID]; ok {
		if current, exists := r.clients[c.UserID]; exists &&
			current == c && current.ConnectionID == c.ConnectionID {
			h.enqueue(c, payload)
		}
	}
}

func (h *Hub) sendToClient(client *Client, payload []byte) bool {
	if h == nil || client == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	r := h.rooms[client.RoomID]
	if r == nil {
		return false
	}
	current := r.clients[client.UserID]
	if current != client || current.ConnectionID != client.ConnectionID {
		return false
	}
	return h.enqueue(client, payload)
}

func (h *Hub) isCurrentClient(client *Client) bool {
	if h == nil || client == nil || client.isClosed() {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	r := h.rooms[client.RoomID]
	if r == nil {
		return false
	}
	current := r.clients[client.UserID]
	return current == client && current.ConnectionID == client.ConnectionID
}

// enqueue 非阻塞投递。缓冲区满时丢弃该消息，客户端可依赖 seq 通过 sync 补推。
func (h *Hub) enqueue(client *Client, payload []byte) bool {
	if client.enqueue(payload) {
		return true
	}
	log.Printf("[WS] Client %d send buffer full or closed, dropping message", client.UserID)
	return false
}

func (h *Hub) closeAllClients() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.rooms {
		for _, client := range r.clients {
			client.closeNow()
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
