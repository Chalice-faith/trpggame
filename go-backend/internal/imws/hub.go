package imws

import (
	"context"
	"log"
	"sync"
	"time"

	"trpggame/internal/realtimebus"
)

const (
	hubCommandBuffer = 256
	deliveryBuffer   = 512
	busOperationWait = 2 * time.Second
)

type registerRequest struct {
	client *Client
	result chan bool
}

type deliverRequest struct {
	userID       uint
	connectionID string
	payload      []byte
	result       chan bool
}

type refreshRequest struct {
	client *Client
	result chan bool
}

type userEventBus interface {
	PublishUser(context.Context, realtimebus.UserEvent) error
	ClaimConnection(context.Context, realtimebus.ConnectionScope, uint, uint, string) error
	RefreshConnection(context.Context, realtimebus.ConnectionScope, uint, uint, string) (bool, error)
	ReleaseConnection(context.Context, realtimebus.ConnectionScope, uint, uint, string) (bool, error)
}

// PresenceObserver 接收已经过 Hub 当前连接校验的生命周期事件。
type PresenceObserver interface {
	OnConnected(userID uint, connectionID string)
	OnRefreshed(userID uint, connectionID string)
	OnDisconnected(userID uint, connectionID string)
}

// Hub 管理用户级 IM WebSocket 连接。clients 只由 Run 事件循环修改。
type Hub struct {
	clients map[uint]*Client

	register   chan registerRequest
	unregister chan *Client
	deliver    chan deliverRequest
	refresh    chan refreshRequest
	replace    chan realtimebus.ControlEvent
	observer   PresenceObserver
	inbound    InboundHandler
	bus        userEventBus

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewHub 创建 IM Hub。调用方必须在使用公开方法前启动 Run。
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[uint]*Client),
		register:   make(chan registerRequest, hubCommandBuffer),
		unregister: make(chan *Client, hubCommandBuffer),
		deliver:    make(chan deliverRequest, deliveryBuffer),
		refresh:    make(chan refreshRequest, hubCommandBuffer),
		replace:    make(chan realtimebus.ControlEvent, hubCommandBuffer),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// SetRealtimeBus enables cross-instance IM delivery and distributed connection ownership.
// It must be called before Run.
func (h *Hub) SetRealtimeBus(bus userEventBus) {
	if h != nil {
		h.bus = bus
	}
}

// HandleUserEvent is registered as the process-local Redis user event consumer.
func (h *Hub) HandleUserEvent(event realtimebus.UserEvent) {
	if h == nil || event.UserID == 0 || len(event.Payload) == 0 {
		return
	}
	request := deliverRequest{
		userID: event.UserID, payload: event.Payload, result: make(chan bool, 1),
	}
	select {
	case h.deliver <- request:
	case <-h.done:
	}
}

// HandleControl closes the exact superseded connection without affecting a newer local connection.
func (h *Hub) HandleControl(event realtimebus.ControlEvent) {
	if h == nil || event.Scope != realtimebus.ScopeIM || event.UserID == 0 || event.ConnectionID == "" {
		return
	}
	select {
	case h.replace <- event:
	case <-h.done:
	}
}

// SetPresenceObserver 在 Run 启动前注入生命周期观察者。
func (h *Hub) SetPresenceObserver(observer PresenceObserver) {
	if h != nil {
		h.observer = observer
	}
}

// SetInboundHandler 在 Run 启动前注入业务消息处理器；未注入时业务类型按不支持处理。
func (h *Hub) SetInboundHandler(handler InboundHandler) {
	if h != nil {
		h.inbound = handler
	}
}

// InboundHandler 返回当前业务消息处理器，nil 表示没有可用处理器。
func (h *Hub) InboundHandler() InboundHandler {
	if h == nil {
		return nil
	}
	return h.inbound
}

// Run 启动 Hub 事件循环。
func (h *Hub) Run() {
	defer close(h.done)
	for {
		select {
		case request := <-h.register:
			request.result <- h.registerClient(request.client)
		case client := <-h.unregister:
			h.unregisterClient(client)
		case request := <-h.deliver:
			request.result <- h.deliverTo(request)
		case request := <-h.refresh:
			request.result <- h.refreshClient(request.client)
		case event := <-h.replace:
			h.replaceClient(event)
		case <-h.stop:
			h.closeAllClients()
			return
		}
	}
}

// RefreshPresence 仅为仍是 Hub 当前连接的 Client 发出续租事件。
func (h *Hub) RefreshPresence(client *Client) bool {
	if h == nil || client == nil {
		return false
	}
	request := refreshRequest{client: client, result: make(chan bool, 1)}
	select {
	case h.refresh <- request:
	case <-h.done:
		return false
	}
	select {
	case refreshed := <-request.result:
		return refreshed
	case <-h.done:
		return false
	}
}

// Register 将 Client 注册为用户的最新 IM 连接。
func (h *Hub) Register(client *Client) bool {
	if h == nil || client == nil {
		return false
	}
	request := registerRequest{client: client, result: make(chan bool, 1)}
	select {
	case h.register <- request:
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

// Unregister 注销 Client；旧连接的延迟注销不会删除新连接。
func (h *Hub) Unregister(client *Client) {
	if h == nil || client == nil {
		return
	}
	select {
	case h.unregister <- client:
	case <-h.done:
	}
}

// SendToUser 向用户当前的 IM 连接投递服务端信封。
func (h *Hub) SendToUser(userID uint, message ServerMessage) bool {
	if h == nil || userID == 0 || !ValidMessageType(message.Type) {
		return false
	}
	timestamp := message.Timestamp
	if timestamp == 0 {
		timestamp = time.Now().UnixMilli()
	}
	payload, err := MarshalServerMessage(
		message.Type,
		message.RequestID,
		timestamp,
		message.Data,
	)
	if err != nil {
		return false
	}
	return h.deliverPayload(userID, "", payload)
}

// PublishUserEvent 实现 service.UserEventPublisher，向用户投递业务事件。
func (h *Hub) PublishUserEvent(userID uint, eventType string, data any) bool {
	messageType := MessageType(eventType)
	if !ValidMessageType(messageType) {
		return false
	}
	payload, err := MarshalServerMessage(messageType, "", time.Now().UnixMilli(), data)
	if err != nil {
		return false
	}
	if h.bus == nil {
		return h.deliverPayload(userID, "", payload)
	}
	ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
	defer cancel()
	if err := h.bus.PublishUser(ctx, realtimebus.UserEvent{UserID: userID, Payload: payload}); err != nil {
		log.Printf("[IMWS] Publish distributed user event: %v", err)
		return false
	}
	return true
}

func (h *Hub) sendToClient(client *Client, payload []byte) bool {
	if client == nil {
		return false
	}
	return h.deliverPayload(client.UserID, client.ConnectionID, payload)
}

func (h *Hub) deliverPayload(userID uint, connectionID string, payload []byte) bool {
	request := deliverRequest{
		userID:       userID,
		connectionID: connectionID,
		payload:      payload,
		result:       make(chan bool, 1),
	}
	select {
	case h.deliver <- request:
	case <-h.done:
		return false
	}
	select {
	case delivered := <-request.result:
		return delivered
	case <-h.done:
		return false
	}
}

// Stop 幂等停止 Hub，并等待事件循环退出。
func (h *Hub) Stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() {
		close(h.stop)
	})
	<-h.done
}

func (h *Hub) registerClient(client *Client) bool {
	if client == nil || client.UserID == 0 || client.ConnectionID == "" {
		return false
	}
	if h.bus != nil {
		ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
		err := h.bus.ClaimConnection(ctx, realtimebus.ScopeIM, 0, client.UserID, client.ConnectionID)
		cancel()
		if err != nil {
			log.Printf("[IMWS] Claim distributed connection: %v", err)
			return false
		}
	}

	connected, err := MarshalServerMessage(
		MsgConnected,
		"",
		time.Now().UnixMilli(),
		ConnectedData{UserID: client.UserID, ConnectionID: client.ConnectionID},
	)
	if err != nil {
		h.releaseConnection(client)
		return false
	}

	previous := h.clients[client.UserID]
	h.clients[client.UserID] = client
	if !client.enqueue(connected) {
		if previous == nil {
			delete(h.clients, client.UserID)
		} else {
			h.clients[client.UserID] = previous
		}
		client.closeNow()
		h.releaseConnection(client)
		return false
	}

	if previous != nil && previous != client {
		replacement, marshalErr := MarshalServerMessage(
			MsgConnectionReplaced,
			"",
			time.Now().UnixMilli(),
			ConnectionReplacedData{Reason: CloseReasonConnectionReplaced},
		)
		if marshalErr != nil {
			previous.closeNow()
		} else {
			previous.requestClose(closeCommand{
				payload: replacement,
				code:    CloseCodeConnectionReplaced,
				reason:  CloseReasonConnectionReplaced,
			})
		}
	}
	if h.observer != nil {
		h.observer.OnConnected(client.UserID, client.ConnectionID)
	}

	log.Printf("[IMWS] User %d connected with connection %s", client.UserID, client.ConnectionID)
	return true
}

func (h *Hub) unregisterClient(client *Client) {
	if client == nil {
		return
	}
	current, ok := h.clients[client.UserID]
	if !ok || current != client || current.ConnectionID != client.ConnectionID {
		return
	}
	delete(h.clients, client.UserID)
	client.closeNow()
	if h.observer != nil {
		h.observer.OnDisconnected(client.UserID, client.ConnectionID)
	}
	h.releaseConnection(client)
	log.Printf("[IMWS] User %d disconnected connection %s", client.UserID, client.ConnectionID)
}

func (h *Hub) deliverTo(request deliverRequest) bool {
	client := h.clients[request.userID]
	if client == nil {
		return false
	}
	if request.connectionID != "" && client.ConnectionID != request.connectionID {
		return false
	}
	if client.enqueue(request.payload) {
		return true
	}
	delete(h.clients, request.userID)
	client.closeNow()
	if h.observer != nil {
		h.observer.OnDisconnected(client.UserID, client.ConnectionID)
	}
	h.releaseConnection(client)
	log.Printf("[IMWS] Closing slow connection %s for user %d", client.ConnectionID, client.UserID)
	return false
}

func (h *Hub) closeAllClients() {
	for userID, client := range h.clients {
		client.closeNow()
		delete(h.clients, userID)
		if h.observer != nil {
			h.observer.OnDisconnected(client.UserID, client.ConnectionID)
		}
		h.releaseConnection(client)
	}
}

func (h *Hub) refreshClient(client *Client) bool {
	if client == nil {
		return false
	}
	current := h.clients[client.UserID]
	if current != client || current.ConnectionID != client.ConnectionID {
		return false
	}
	if h.bus != nil {
		ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
		refreshed, err := h.bus.RefreshConnection(
			ctx, realtimebus.ScopeIM, 0, client.UserID, client.ConnectionID,
		)
		cancel()
		if err != nil || !refreshed {
			if err != nil {
				log.Printf("[IMWS] Refresh distributed connection: %v", err)
			}
			return false
		}
	}
	if h.observer != nil {
		h.observer.OnRefreshed(client.UserID, client.ConnectionID)
	}
	return true
}

func (h *Hub) replaceClient(event realtimebus.ControlEvent) {
	client := h.clients[event.UserID]
	if client == nil || client.ConnectionID != event.ConnectionID {
		return
	}
	delete(h.clients, event.UserID)
	replacement, err := MarshalServerMessage(
		MsgConnectionReplaced,
		"",
		time.Now().UnixMilli(),
		ConnectionReplacedData{Reason: CloseReasonConnectionReplaced},
	)
	if err != nil {
		client.closeNow()
	} else {
		client.requestClose(closeCommand{
			payload: replacement, code: CloseCodeConnectionReplaced, reason: CloseReasonConnectionReplaced,
		})
	}
	if h.observer != nil {
		h.observer.OnDisconnected(client.UserID, client.ConnectionID)
	}
}

func (h *Hub) releaseConnection(client *Client) {
	if h.bus == nil || client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), busOperationWait)
	defer cancel()
	if _, err := h.bus.ReleaseConnection(
		ctx, realtimebus.ScopeIM, 0, client.UserID, client.ConnectionID,
	); err != nil {
		log.Printf("[IMWS] Release distributed connection: %v", err)
	}
}
