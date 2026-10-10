package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"trpggame/internal/model"
)

const (
	presenceEventBuffer  = 1024
	presenceEventTimeout = 5 * time.Second
)

type PresenceLeaseRepository interface {
	Claim(context.Context, uint, string) (bool, error)
	Refresh(context.Context, uint, string) (bool, error)
	Release(context.Context, uint, string) (bool, error)
	Statuses(context.Context, []uint) (map[uint]bool, error)
}

type AcceptedFriendRepository interface {
	ListAcceptedPeerIDs(context.Context, uint) ([]uint, error)
}

type GamePresenceRepository interface {
	ListPlayingMultiplayerUsers(context.Context, []uint) (map[uint]bool, error)
}

type GamePresenceNotifier interface {
	NotifyGameStatusChanged(context.Context, []uint)
}

// UserEventPublisher 由 IM Hub 实现；false 表示用户当前离线或连接不可投递。
type UserEventPublisher interface {
	PublishUserEvent(userID uint, eventType string, data any) bool
}

// RedisPresenceProvider 将租约状态映射为好友 REST 所需的 presence 语义。
type RedisPresenceProvider struct {
	leases PresenceLeaseRepository
	games  GamePresenceRepository
}

func NewRedisPresenceProvider(leases PresenceLeaseRepository) *RedisPresenceProvider {
	return &RedisPresenceProvider{leases: leases}
}

func (p *RedisPresenceProvider) SetGamePresenceRepository(games GamePresenceRepository) {
	if p != nil {
		p.games = games
	}
}

func (p *RedisPresenceProvider) Statuses(ctx context.Context, ids []uint) (map[uint]PresenceStatus, error) {
	values, err := p.leases.Statuses(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[uint]PresenceStatus, len(values))
	for id, online := range values {
		result[id] = PresenceOffline
		if online {
			result[id] = PresenceOnline
		}
	}
	if p.games != nil {
		playing, err := p.games.ListPlayingMultiplayerUsers(ctx, ids)
		if err != nil {
			return nil, err
		}
		for id, active := range playing {
			if active && values[id] {
				result[id] = PresenceGaming
			}
		}
	}
	return result, nil
}

type presenceEventKind uint8

const (
	presenceConnected presenceEventKind = iota + 1
	presenceRefreshed
	presenceDisconnected
	presenceGameStatusChanged
	presenceStop
)

type presenceEvent struct {
	kind         presenceEventKind
	userID       uint
	connectionID string
}

// PresenceCoordinator 按 Hub 产生的顺序串行处理 Redis 租约与好友广播。
type PresenceCoordinator struct {
	leases    PresenceLeaseRepository
	friends   AcceptedFriendRepository
	publisher UserEventPublisher
	games     GamePresenceRepository
	events    chan presenceEvent
	done      chan struct{}
	stopOnce  sync.Once
}

func (c *PresenceCoordinator) SetGamePresenceRepository(games GamePresenceRepository) {
	if c != nil {
		c.games = games
	}
}

func (c *PresenceCoordinator) NotifyGameStatusChanged(_ context.Context, userIDs []uint) {
	for _, userID := range userIDs {
		if userID != 0 {
			c.enqueue(presenceEvent{kind: presenceGameStatusChanged, userID: userID})
		}
	}
}

func NewPresenceCoordinator(leases PresenceLeaseRepository, friends AcceptedFriendRepository, publisher UserEventPublisher) *PresenceCoordinator {
	return &PresenceCoordinator{
		leases: leases, friends: friends, publisher: publisher,
		events: make(chan presenceEvent, presenceEventBuffer), done: make(chan struct{}),
	}
}

// Run 启动有序事件循环。调用方应在 Hub.Run 前启动。
func (c *PresenceCoordinator) Run() {
	defer close(c.done)
	for event := range c.events {
		if event.kind == presenceStop {
			return
		}
		c.process(event)
	}
}

func (c *PresenceCoordinator) OnConnected(userID uint, connectionID string) {
	c.enqueue(presenceEvent{kind: presenceConnected, userID: userID, connectionID: connectionID})
}

func (c *PresenceCoordinator) OnRefreshed(userID uint, connectionID string) {
	c.enqueue(presenceEvent{kind: presenceRefreshed, userID: userID, connectionID: connectionID})
}

func (c *PresenceCoordinator) OnDisconnected(userID uint, connectionID string) {
	c.enqueue(presenceEvent{kind: presenceDisconnected, userID: userID, connectionID: connectionID})
}

func (c *PresenceCoordinator) enqueue(event presenceEvent) {
	select {
	case c.events <- event:
	case <-c.done:
	}
}

// Stop 在同一队列尾部插入停止事件，因此会排空此前事件。
func (c *PresenceCoordinator) Stop() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() { c.enqueue(presenceEvent{kind: presenceStop}) })
	<-c.done
}

func (c *PresenceCoordinator) process(event presenceEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), presenceEventTimeout)
	defer cancel()
	switch event.kind {
	case presenceConnected:
		becameOnline, err := c.leases.Claim(ctx, event.userID, event.connectionID)
		if err != nil {
			log.Printf("claim presence for user %d: %v", event.userID, err)
			return
		}
		if becameOnline {
			c.broadcastCurrent(ctx, event.userID)
		}
	case presenceRefreshed:
		if _, err := c.leases.Refresh(ctx, event.userID, event.connectionID); err != nil {
			log.Printf("refresh presence for user %d: %v", event.userID, err)
		}
	case presenceDisconnected:
		becameOffline, err := c.leases.Release(ctx, event.userID, event.connectionID)
		if err != nil {
			log.Printf("release presence for user %d: %v", event.userID, err)
			return
		}
		if becameOffline {
			c.broadcastCurrent(ctx, event.userID)
		}
	case presenceGameStatusChanged:
		c.broadcastCurrent(ctx, event.userID)
	}
}

func (c *PresenceCoordinator) broadcastCurrent(ctx context.Context, userID uint) {
	statuses, err := c.leases.Statuses(ctx, []uint{userID})
	if err != nil {
		log.Printf("read presence for user %d: %v", userID, err)
		return
	}
	status := PresenceOffline
	if statuses[userID] {
		status = PresenceOnline
		if c.games != nil {
			playing, gameErr := c.games.ListPlayingMultiplayerUsers(ctx, []uint{userID})
			if gameErr != nil {
				log.Printf("read game presence for user %d: %v", userID, gameErr)
				return
			}
			if playing[userID] {
				status = PresenceGaming
			}
		}
	}
	c.broadcast(ctx, userID, status)
}

func (c *PresenceCoordinator) broadcast(ctx context.Context, userID uint, status PresenceStatus) {
	peerIDs, err := c.friends.ListAcceptedPeerIDs(ctx, userID)
	if err != nil {
		log.Printf("list accepted friends for presence user %d: %v", userID, err)
		return
	}
	data := PresenceEventData{UserID: userID, Status: status}
	for _, peerID := range peerIDs {
		c.publisher.PublishUserEvent(peerID, "presence", data)
	}
}

type PresenceEventData struct {
	UserID uint           `json:"user_id"`
	Status PresenceStatus `json:"status"`
}

type FriendshipEventData struct {
	FriendshipID uint                   `json:"friendship_id"`
	Status       model.FriendshipStatus `json:"status"`
	RequestedBy  uint                   `json:"requested_by"`
	Peer         PublicUser             `json:"peer"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// RealtimeFriendshipPublisher 在事务提交后构造双方视角并尽力投递。
type RealtimeFriendshipPublisher struct {
	users     FriendUserRepository
	publisher UserEventPublisher
}

func NewRealtimeFriendshipPublisher(users FriendUserRepository, publisher UserEventPublisher) *RealtimeFriendshipPublisher {
	return &RealtimeFriendshipPublisher{users: users, publisher: publisher}
}

func (p *RealtimeFriendshipPublisher) PublishFriendshipUpdated(ctx context.Context, event FriendshipUpdatedEvent) error {
	users, err := p.users.FindActiveByIDs(ctx, []uint{event.UserLowID, event.UserHighID})
	if err != nil {
		return fmt.Errorf("load friendship event users: %w", err)
	}
	byID := make(map[uint]model.User, len(users))
	for _, user := range users {
		byID[user.ID] = user
	}
	for _, recipientID := range []uint{event.UserLowID, event.UserHighID} {
		peerID := event.UserHighID
		if recipientID == event.UserHighID {
			peerID = event.UserLowID
		}
		peer, ok := byID[peerID]
		if !ok {
			continue
		}
		p.publisher.PublishUserEvent(recipientID, "friendship_updated", FriendshipEventData{
			FriendshipID: event.FriendshipID, Status: event.Status, RequestedBy: event.RequestedBy,
			Peer: publicUser(peer), UpdatedAt: event.UpdatedAt,
		})
	}
	return nil
}
