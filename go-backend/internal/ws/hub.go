package ws

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"trpggame/internal/realtime"
	"trpggame/internal/realtimebus"
)

// recentCapacity 每个房间保留的近期服务端消息数，超出后丢弃最旧的，用于重连补推。
const recentCapacity = 200

const (
	hubCommandBuffer = 256
	deliveryBuffer   = 512
	busOperationWait = 2 * time.Second
)

// room 一个房间的订阅状态。内部字段只在持有 Hub.mu 时访问。
type room struct {
	clients      map[uint]*Client
	seq          int64 // 房间全局消息水位
	deliveredSeq int64 // 本实例已消费的 Redis Pub/Sub 水位，用于抑制重复投递
	recent       []Message
}

// deliverRequest 一次服务端 → 客户端投递请求。userID 为 0 表示广播给整个房间。
type deliverRequest struct {
	roomID          uint
	userID          uint
	revokeUserID    uint
	currentSnapshot bool
	unsequenced     bool
	msgType         MessageType
	data            json.RawMessage
	requestID       string
}

type registerRequest struct {
	client *Client
	result chan bool
}

type disconnectRequest struct {
	roomID       uint
	userID       uint
	connectionID string
	code         int
	reason       string
}

type roomEventBus interface {
	PublishRoom(context.Context, realtimebus.RoomEvent) (int64, error)
	CurrentRoomSequence(context.Context, uint) (int64, error)
	ReplayRoom(context.Context, uint, uint, int64) (realtimebus.ReplayResult, error)
	ClaimConnection(context.Context, realtimebus.ConnectionScope, uint, uint, string) error
	RefreshConnection(context.Context, realtimebus.ConnectionScope, uint, uint, string) (bool, error)
	ReleaseConnection(context.Context, realtimebus.ConnectionScope, uint, uint, string) (bool, error)
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
	disconnect chan disconnectRequest

	// 服务端 → 客户端投递通道
	deliver       chan deliverRequest
	distributed   chan realtimebus.RoomEvent
	actionHandler GameActionHandler
	bus           roomEventBus

	mu       sync.RWMutex
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewHub 创建新的 Hub 实例。
func NewHub() *Hub {
	return &Hub{
		rooms:       make(map[uint]*room),
		register:    make(chan registerRequest, hubCommandBuffer),
		unregister:  make(chan *Client, hubCommandBuffer),
		disconnect:  make(chan disconnectRequest, hubCommandBuffer),
		deliver:     make(chan deliverRequest, deliveryBuffer),
		distributed: make(chan realtimebus.RoomEvent, deliveryBuffer),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
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

		case request := <-h.disconnect:
			h.disconnectUser(request)

		case req := <-h.deliver:
			h.deliverTo(req)

		case event := <-h.distributed:
			h.deliverDistributed(event)

		case <-h.stop:
			log.Println("[WS] Hub stopping...")
			h.closeAllClients()
			return
		}
	}
}

// SetRealtimeBus enables Redis-backed sequencing, replay and distributed connection ownership.
// It must be called before Run.
func (h *Hub) SetRealtimeBus(bus roomEventBus) {
	if h != nil {
		h.bus = bus
	}
}

// HandleRoomEvent is registered as the process-local Redis room event consumer.
func (h *Hub) HandleRoomEvent(event realtimebus.RoomEvent) {
	if h == nil || event.RoomID == 0 || event.Seq <= 0 || len(event.Message) == 0 {
		return
	}
	select {
	case h.distributed <- event:
	case <-h.stop:
	case <-h.done:
	}
}

// HandleControl closes a superseded connection only when its immutable connection ID still matches.
func (h *Hub) HandleControl(event realtimebus.ControlEvent) {
	if h == nil || event.Scope != realtimebus.ScopeGame || event.RoomID == 0 ||
		event.UserID == 0 || event.ConnectionID == "" {
		return
	}
	select {
	case h.disconnect <- disconnectRequest{
		roomID: event.RoomID, userID: event.UserID, connectionID: event.ConnectionID,
		code: realtime.CloseCodeConnectionReplaced, reason: realtime.CloseReasonConnectionReplaced,
	}:
	case <-h.stop:
	case <-h.done:
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

// DisconnectUser immediately removes the current room connection and closes it with a stable reason.
func (h *Hub) DisconnectUser(roomID, userID uint, code int, reason string) {
	if h == nil || roomID == 0 || userID == 0 {
		return
	}
	select {
	case h.disconnect <- disconnectRequest{roomID: roomID, userID: userID, code: code, reason: reason}:
	case <-h.stop:
	case <-h.done:
	}
}

// RefreshConnection renews distributed ownership for a still-current game connection.
func (h *Hub) RefreshConnection(client *Client) bool {
	if !h.isCurrentClient(client) {
		return false
	}
	if h.bus == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
	defer cancel()
	refreshed, err := h.bus.RefreshConnection(
		ctx, realtimebus.ScopeGame, client.RoomID, client.UserID, client.ConnectionID,
	)
	if err != nil {
		log.Printf("[WS] Refresh distributed connection: %v", err)
		return false
	}
	return refreshed
}

// BroadcastToRoom 向房间内所有订阅客户端广播消息。
func (h *Hub) BroadcastToRoom(roomID uint, msgType MessageType, data json.RawMessage) {
	h.queueDelivery(deliverRequest{roomID: roomID, msgType: msgType, data: data})
}

func (h *Hub) BroadcastToRoomWithRequestID(roomID uint, msgType MessageType, data json.RawMessage, requestID string) {
	h.queueDelivery(deliverRequest{roomID: roomID, msgType: msgType, data: data, requestID: requestID})
}

// BroadcastAndRevoke gives the affected connection the committed final event, removes it
// from the room immediately, and then closes it. Later room events cannot reach that user.
func (h *Hub) BroadcastAndRevoke(roomID, userID uint, msgType MessageType, data json.RawMessage) {
	h.queueDelivery(deliverRequest{roomID: roomID, revokeUserID: userID, msgType: msgType, data: data})
}

// SendToUser 向房间内指定客户端发送消息。
func (h *Hub) SendToUser(roomID, userID uint, msgType MessageType, data json.RawMessage) {
	h.SendToUserWithRequestID(roomID, userID, msgType, data, "")
}

// SendRoomSnapshot sends a baseline snapshot at the current room sequence without
// advancing or retaining the transient per-connection delivery in replay history.
func (h *Hub) SendRoomSnapshot(roomID, userID uint, data json.RawMessage) {
	h.queueDelivery(deliverRequest{roomID: roomID, userID: userID, msgType: MsgRoomSnapshot, data: data, currentSnapshot: true})
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
		roomID: roomID, userID: userID, msgType: MsgError, data: data, requestID: requestID, unsequenced: true,
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
	if client == nil || client.UserID == 0 || client.RoomID == 0 ||
		client.ConnectionID == "" || client.isClosed() {
		return false
	}
	globalSeq := int64(0)
	if h.bus != nil {
		ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
		err := h.bus.ClaimConnection(
			ctx, realtimebus.ScopeGame, client.RoomID, client.UserID, client.ConnectionID,
		)
		if err == nil {
			globalSeq, err = h.bus.CurrentRoomSequence(ctx, client.RoomID)
		}
		cancel()
		if err != nil {
			log.Printf("[WS] Claim distributed connection: %v", err)
			return false
		}
	}

	h.mu.Lock()
	r := h.rooms[client.RoomID]
	if r == nil {
		r = &room{clients: make(map[uint]*Client)}
		h.rooms[client.RoomID] = r
	}
	if globalSeq > r.seq {
		r.seq = globalSeq
	}

	// 订阅确认是客户端的首条消息，直接投递（不占用房间序号）。
	payload, err := json.Marshal(Message{
		Type:      MsgSubscribed,
		RoomID:    client.RoomID,
		Data:      mustJSON(SubscribedData{RoomID: client.RoomID, Seq: r.seq}),
		Timestamp: time.Now().UnixMilli(),
	})
	if err != nil {
		h.mu.Unlock()
		h.releaseConnection(client)
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
		h.mu.Unlock()
		h.releaseConnection(client)
		return false
	}

	if previous != nil && previous != client {
		previous.requestClose(clientCloseCommand{
			code:   realtime.CloseCodeConnectionReplaced,
			reason: realtime.CloseReasonConnectionReplaced,
		})
	}
	h.mu.Unlock()
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
	client.closeNow()
	r, ok := h.rooms[client.RoomID]
	if !ok {
		h.mu.Unlock()
		return
	}
	current, exists := r.clients[client.UserID]
	if !exists || current != client || current.ConnectionID != client.ConnectionID {
		h.mu.Unlock()
		return
	}
	delete(r.clients, client.UserID)
	if len(r.clients) == 0 {
		delete(h.rooms, client.RoomID)
	}
	h.mu.Unlock()
	h.releaseConnection(client)
	log.Printf("[WS] User %d left room %d", client.UserID, client.RoomID)
}

func (h *Hub) disconnectUser(request disconnectRequest) {
	h.mu.Lock()
	r := h.rooms[request.roomID]
	if r == nil {
		h.mu.Unlock()
		return
	}
	client := r.clients[request.userID]
	if client == nil {
		h.mu.Unlock()
		return
	}
	if request.connectionID != "" && client.ConnectionID != request.connectionID {
		h.mu.Unlock()
		return
	}
	delete(r.clients, request.userID)
	if len(r.clients) == 0 {
		delete(h.rooms, request.roomID)
	}
	client.requestClose(clientCloseCommand{code: request.code, reason: request.reason})
	h.mu.Unlock()
	h.releaseConnection(client)
}

func (h *Hub) deliverTo(req deliverRequest) {
	if req.currentSnapshot || req.unsequenced {
		h.deliverUnsequenced(req)
		return
	}
	if h.bus != nil {
		msg := Message{
			Type: req.msgType, RoomID: req.roomID, UserID: req.userID,
			Data: req.data, Timestamp: time.Now().UnixMilli(), RequestID: req.requestID,
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			log.Printf("[WS] Marshal error: %v", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
		_, err = h.bus.PublishRoom(ctx, realtimebus.RoomEvent{
			RoomID: req.roomID, UserID: req.userID, RevokeUserID: req.revokeUserID, Message: payload,
		})
		cancel()
		if err != nil {
			log.Printf("[WS] Publish distributed room event: %v", err)
		}
		return
	}

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
	if req.revokeUserID != 0 {
		for userID, client := range r.clients {
			if userID == req.revokeUserID {
				delete(r.clients, userID)
				client.requestClose(clientCloseCommand{
					code: realtime.CloseCodeRoomAccessRevoked, reason: realtime.CloseReasonRoomAccessRevoked,
					finalPayload: payload,
				})
				continue
			}
			h.enqueue(client, payload)
		}
		if len(r.clients) == 0 {
			delete(h.rooms, req.roomID)
		}
		return
	}

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

func (h *Hub) deliverUnsequenced(req deliverRequest) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	r := h.rooms[req.roomID]
	if r == nil {
		return
	}
	client := r.clients[req.userID]
	if client == nil {
		return
	}
	seq := int64(0)
	if req.currentSnapshot {
		seq = r.seq
	}
	payload, err := json.Marshal(Message{
		Type: req.msgType, RoomID: req.roomID, Seq: seq, Data: req.data,
		Timestamp: time.Now().UnixMilli(), RequestID: req.requestID,
	})
	if err != nil {
		log.Printf("[WS] Marshal error: %v", err)
		return
	}
	h.enqueue(client, payload)
}

func (h *Hub) deliverDistributed(event realtimebus.RoomEvent) {
	var msg Message
	if err := json.Unmarshal(event.Message, &msg); err != nil {
		log.Printf("[WS] Decode distributed room event: %v", err)
		return
	}
	msg.RoomID = event.RoomID
	msg.UserID = event.UserID
	msg.Seq = event.Seq
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.Lock()
	r := h.rooms[event.RoomID]
	if r == nil || event.Seq <= r.deliveredSeq {
		h.mu.Unlock()
		return
	}
	r.deliveredSeq = event.Seq
	if event.Seq > r.seq {
		r.seq = event.Seq
	}
	if event.RevokeUserID != 0 {
		var revoked *Client
		for userID, client := range r.clients {
			if userID == event.RevokeUserID {
				delete(r.clients, userID)
				revoked = client
				client.requestClose(clientCloseCommand{
					code: realtime.CloseCodeRoomAccessRevoked, reason: realtime.CloseReasonRoomAccessRevoked,
					finalPayload: payload,
				})
				continue
			}
			h.enqueue(client, payload)
		}
		if len(r.clients) == 0 {
			delete(h.rooms, event.RoomID)
		}
		h.mu.Unlock()
		h.releaseConnection(revoked)
		return
	}
	if event.UserID != 0 {
		if client := r.clients[event.UserID]; client != nil {
			h.enqueue(client, payload)
		}
		h.mu.Unlock()
		return
	}
	for _, client := range r.clients {
		h.enqueue(client, payload)
	}
	h.mu.Unlock()
}

func (h *Hub) handleSync(c *Client, data json.RawMessage) {
	var req SyncRequestData
	if len(data) > 0 {
		if err := json.Unmarshal(data, &req); err != nil {
			h.sendErrorTo(c, 1504, "invalid sync request", "")
			return
		}
	}

	if h.bus != nil {
		if !h.isCurrentClient(c) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
		replay, err := h.bus.ReplayRoom(ctx, c.RoomID, c.UserID, req.SinceSeq)
		cancel()
		if err != nil {
			h.sendErrorTo(c, 1508, "room sync unavailable", "")
			return
		}
		if !h.isCurrentClient(c) {
			return
		}
		messageType := MsgSyncBatch
		var responseData json.RawMessage
		if replay.SnapshotRequired {
			messageType = MsgSnapshotRequired
			responseData = mustJSON(SnapshotRequiredData{NextSeq: replay.NextSeq})
		} else {
			messages := make([]Message, 0, len(replay.Events))
			for _, event := range replay.Events {
				var message Message
				if json.Unmarshal(event.Message, &message) != nil {
					continue
				}
				message.RoomID, message.UserID, message.Seq = event.RoomID, event.UserID, event.Seq
				messages = append(messages, message)
			}
			responseData = mustJSON(SyncBatchData{Messages: messages, NextSeq: replay.NextSeq})
		}
		payload, err := json.Marshal(Message{
			Type: messageType, RoomID: c.RoomID, Data: responseData, Timestamp: time.Now().UnixMilli(),
		})
		if err == nil {
			h.sendToClient(c, payload)
		}
		return
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
	clients := make([]*Client, 0)
	for _, r := range h.rooms {
		for _, client := range r.clients {
			client.closeNow()
			clients = append(clients, client)
		}
	}
	h.rooms = make(map[uint]*room)
	h.mu.Unlock()
	for _, client := range clients {
		h.releaseConnection(client)
	}
}

func (h *Hub) releaseConnection(client *Client) {
	if h.bus == nil || client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
	defer cancel()
	if _, err := h.bus.ReleaseConnection(
		ctx, realtimebus.ScopeGame, client.RoomID, client.UserID, client.ConnectionID,
	); err != nil {
		log.Printf("[WS] Release distributed connection: %v", err)
	}
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
