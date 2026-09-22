package realtimebus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	DefaultRoomLogCapacity = 200
	DefaultRoomLogTTL      = 24 * time.Hour
	DefaultConnectionTTL   = 90 * time.Second

	roomChannel    = "realtime:v1:room"
	userChannel    = "realtime:v1:user"
	controlChannel = "realtime:v1:control"
	sequenceToken  = "__ROOM_SEQ__"
)

var (
	ErrInvalidEvent      = errors.New("invalid realtime event")
	ErrBusAlreadyStarted = errors.New("realtime bus already started")

	publishRoomScript = redis.NewScript(`
local seq = redis.call('INCR', KEYS[1])
local encoded = string.gsub(ARGV[1], '"seq":"__ROOM_SEQ__"', '"seq":' .. seq, 1)
redis.call('RPUSH', KEYS[2], encoded)
redis.call('LTRIM', KEYS[2], -tonumber(ARGV[2]), -1)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
redis.call('PEXPIRE', KEYS[2], ARGV[3])
redis.call('PUBLISH', ARGV[4], encoded)
return seq`)

	replayRoomScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then current = '0' end
return {current, redis.call('LRANGE', KEYS[2], 0, -1)}`)

	claimConnectionScript = redis.NewScript(`
local previous = redis.call('GET', KEYS[1])
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
if previous and previous ~= ARGV[1] then
  redis.call('PUBLISH', ARGV[3], ARGV[4] .. previous)
end
if previous then return previous else return '' end`)

	refreshConnectionScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0`)

	releaseConnectionScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)
)

// RoomEvent is the Redis wire envelope for a sequenced room event.
type RoomEvent struct {
	RoomID       uint            `json:"room_id"`
	UserID       uint            `json:"user_id,omitempty"`
	RevokeUserID uint            `json:"revoke_user_id,omitempty"`
	Seq          int64           `json:"seq"`
	Message      json.RawMessage `json:"message"`
}

type roomPublishEnvelope struct {
	RoomID       uint            `json:"room_id"`
	UserID       uint            `json:"user_id,omitempty"`
	RevokeUserID uint            `json:"revoke_user_id,omitempty"`
	Seq          string          `json:"seq"`
	Message      json.RawMessage `json:"message"`
}

// UserEvent is intentionally not persisted: IM recovery remains database-backed.
type UserEvent struct {
	UserID  uint            `json:"user_id"`
	Payload json.RawMessage `json:"payload"`
}

type ConnectionScope string

const (
	ScopeGame ConnectionScope = "game"
	ScopeIM   ConnectionScope = "im"
)

// ControlEvent asks the previous owning instance to close a superseded connection.
type ControlEvent struct {
	TargetInstanceID string          `json:"target_instance_id"`
	Scope            ConnectionScope `json:"scope"`
	RoomID           uint            `json:"room_id,omitempty"`
	UserID           uint            `json:"user_id"`
	ConnectionID     string          `json:"connection_id"`
}

type ReplayResult struct {
	Events           []RoomEvent
	NextSeq          int64
	SnapshotRequired bool
}

type Options struct {
	InstanceID      string
	RoomLogCapacity int
	RoomLogTTL      time.Duration
	ConnectionTTL   time.Duration
}

type Bus struct {
	client          redis.UniversalClient
	instanceID      string
	roomLogCapacity int
	roomLogTTL      time.Duration
	connectionTTL   time.Duration

	mu             sync.RWMutex
	roomHandler    func(RoomEvent)
	userHandler    func(UserEvent)
	controlHandler func(ControlEvent)
	pubsub         *redis.PubSub
	cancel         context.CancelFunc
	done           chan struct{}
	started        bool
	stopOnce       sync.Once
}

func New(client redis.UniversalClient, options Options) (*Bus, error) {
	if client == nil {
		return nil, ErrInvalidEvent
	}
	if strings.TrimSpace(options.InstanceID) == "" {
		options.InstanceID = uuid.NewString()
	}
	if options.RoomLogCapacity <= 0 {
		options.RoomLogCapacity = DefaultRoomLogCapacity
	}
	if options.RoomLogTTL <= 0 {
		options.RoomLogTTL = DefaultRoomLogTTL
	}
	if options.ConnectionTTL <= 0 {
		options.ConnectionTTL = DefaultConnectionTTL
	}
	return &Bus{
		client:          client,
		instanceID:      options.InstanceID,
		roomLogCapacity: options.RoomLogCapacity,
		roomLogTTL:      options.RoomLogTTL,
		connectionTTL:   options.ConnectionTTL,
	}, nil
}

func (b *Bus) InstanceID() string { return b.instanceID }

func (b *Bus) SetRoomHandler(handler func(RoomEvent)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.roomHandler = handler
}

func (b *Bus) SetUserHandler(handler func(UserEvent)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.userHandler = handler
}

func (b *Bus) SetControlHandler(handler func(ControlEvent)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.controlHandler = handler
}

// Start establishes all subscriptions before returning, so events published afterwards are observable.
func (b *Bus) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return ErrBusAlreadyStarted
	}
	subscriptionContext, cancel := context.WithCancel(context.Background())
	pubsub := b.client.Subscribe(subscriptionContext, roomChannel, userChannel, controlChannel)
	b.mu.Unlock()

	if _, err := pubsub.Receive(ctx); err != nil {
		cancel()
		_ = pubsub.Close()
		return fmt.Errorf("subscribe realtime bus: %w", err)
	}

	b.mu.Lock()
	b.pubsub = pubsub
	b.cancel = cancel
	b.done = make(chan struct{})
	b.started = true
	done := b.done
	b.mu.Unlock()

	go b.consume(subscriptionContext, pubsub, done)
	return nil
}

func (b *Bus) Stop() {
	if b == nil {
		return
	}
	b.stopOnce.Do(func() {
		b.mu.RLock()
		started, cancel, pubsub, done := b.started, b.cancel, b.pubsub, b.done
		b.mu.RUnlock()
		if !started {
			return
		}
		cancel()
		_ = pubsub.Close()
		<-done
	})
}

func (b *Bus) consume(ctx context.Context, pubsub *redis.PubSub, done chan struct{}) {
	defer close(done)
	channel := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-channel:
			if !ok {
				return
			}
			b.dispatch(message.Channel, []byte(message.Payload))
		}
	}
}

func (b *Bus) dispatch(channel string, payload []byte) {
	b.mu.RLock()
	roomHandler, userHandler, controlHandler := b.roomHandler, b.userHandler, b.controlHandler
	b.mu.RUnlock()

	switch channel {
	case roomChannel:
		var event RoomEvent
		if json.Unmarshal(payload, &event) == nil && roomHandler != nil {
			invokeHandler("room", func() { roomHandler(event) })
		}
	case userChannel:
		var event UserEvent
		if json.Unmarshal(payload, &event) == nil && userHandler != nil {
			invokeHandler("user", func() { userHandler(event) })
		}
	case controlChannel:
		var event ControlEvent
		if decodeControlEvent(payload, &event) == nil && event.TargetInstanceID == b.instanceID && controlHandler != nil {
			invokeHandler("control", func() { controlHandler(event) })
		}
	}
}

func (b *Bus) PublishRoom(ctx context.Context, event RoomEvent) (int64, error) {
	if event.RoomID == 0 || len(event.Message) == 0 || !json.Valid(event.Message) {
		return 0, ErrInvalidEvent
	}
	wire, err := json.Marshal(roomPublishEnvelope{
		RoomID: event.RoomID, UserID: event.UserID, RevokeUserID: event.RevokeUserID,
		Seq: sequenceToken, Message: event.Message,
	})
	if err != nil {
		return 0, fmt.Errorf("marshal room event: %w", err)
	}
	seq, err := publishRoomScript.Run(
		ctx,
		b.client,
		[]string{roomSequenceKey(event.RoomID), roomLogKey(event.RoomID)},
		wire,
		b.roomLogCapacity,
		b.roomLogTTL.Milliseconds(),
		roomChannel,
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("publish room event: %w", err)
	}
	return seq, nil
}

func (b *Bus) CurrentRoomSequence(ctx context.Context, roomID uint) (int64, error) {
	if roomID == 0 {
		return 0, ErrInvalidEvent
	}
	value, err := b.client.Get(ctx, roomSequenceKey(roomID)).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read room sequence: %w", err)
	}
	seq, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse room sequence: %w", err)
	}
	return seq, nil
}

func (b *Bus) ReplayRoom(ctx context.Context, roomID, userID uint, sinceSeq int64) (ReplayResult, error) {
	if roomID == 0 || userID == 0 || sinceSeq < 0 {
		return ReplayResult{}, ErrInvalidEvent
	}
	result, err := replayRoomScript.Run(
		ctx,
		b.client,
		[]string{roomSequenceKey(roomID), roomLogKey(roomID)},
	).Slice()
	if err != nil {
		return ReplayResult{}, fmt.Errorf("replay room events: %w", err)
	}
	if len(result) != 2 {
		return ReplayResult{}, errors.New("invalid room replay result")
	}
	current, err := parseRedisInt(result[0])
	if err != nil {
		return ReplayResult{}, fmt.Errorf("parse room replay watermark: %w", err)
	}
	rows, ok := result[1].([]interface{})
	if !ok {
		return ReplayResult{}, errors.New("invalid room replay entries")
	}
	events := make([]RoomEvent, 0, len(rows))
	var firstSeq int64
	for _, row := range rows {
		encoded, ok := row.(string)
		if !ok {
			if bytes, bytesOK := row.([]byte); bytesOK {
				encoded = string(bytes)
			} else {
				continue
			}
		}
		var event RoomEvent
		if err := json.Unmarshal([]byte(encoded), &event); err != nil || event.Seq <= 0 {
			continue
		}
		if firstSeq == 0 {
			firstSeq = event.Seq
		}
		if event.Seq > sinceSeq && (event.UserID == 0 || event.UserID == userID) {
			events = append(events, event)
		}
	}
	snapshotRequired := sinceSeq > current || (sinceSeq < current && (firstSeq == 0 || sinceSeq < firstSeq-1))
	if snapshotRequired {
		events = nil
	}
	return ReplayResult{Events: events, NextSeq: current, SnapshotRequired: snapshotRequired}, nil
}

func (b *Bus) PublishUser(ctx context.Context, event UserEvent) error {
	if event.UserID == 0 || len(event.Payload) == 0 || !json.Valid(event.Payload) {
		return ErrInvalidEvent
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal user event: %w", err)
	}
	if err := b.client.Publish(ctx, userChannel, payload).Err(); err != nil {
		return fmt.Errorf("publish user event: %w", err)
	}
	return nil
}

func (b *Bus) ClaimConnection(
	ctx context.Context,
	scope ConnectionScope,
	roomID, userID uint,
	connectionID string,
) error {
	key, err := connectionKey(scope, roomID, userID)
	if err != nil || connectionID == "" || strings.Contains(connectionID, "|") || strings.Contains(b.instanceID, "|") {
		return ErrInvalidEvent
	}
	owner := b.instanceID + "|" + connectionID
	controlPrefix := string(scope) + "|" + strconv.FormatUint(uint64(roomID), 10) + "|" +
		strconv.FormatUint(uint64(userID), 10) + "|"
	_, err = claimConnectionScript.Run(
		ctx,
		b.client,
		[]string{key},
		owner,
		b.connectionTTL.Milliseconds(),
		controlChannel,
		controlPrefix,
	).Text()
	if err != nil {
		return fmt.Errorf("claim realtime connection: %w", err)
	}
	return nil
}

func (b *Bus) RefreshConnection(
	ctx context.Context,
	scope ConnectionScope,
	roomID, userID uint,
	connectionID string,
) (bool, error) {
	key, err := connectionKey(scope, roomID, userID)
	if err != nil || connectionID == "" {
		return false, ErrInvalidEvent
	}
	owner := b.instanceID + "|" + connectionID
	result, err := refreshConnectionScript.Run(
		ctx, b.client, []string{key}, owner, b.connectionTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return false, fmt.Errorf("refresh realtime connection: %w", err)
	}
	return result == 1, nil
}

func (b *Bus) ReleaseConnection(
	ctx context.Context,
	scope ConnectionScope,
	roomID, userID uint,
	connectionID string,
) (bool, error) {
	key, err := connectionKey(scope, roomID, userID)
	if err != nil || connectionID == "" {
		return false, ErrInvalidEvent
	}
	owner := b.instanceID + "|" + connectionID
	result, err := releaseConnectionScript.Run(ctx, b.client, []string{key}, owner).Int64()
	if err != nil {
		return false, fmt.Errorf("release realtime connection: %w", err)
	}
	return result == 1, nil
}

func connectionKey(scope ConnectionScope, roomID, userID uint) (string, error) {
	if userID == 0 {
		return "", ErrInvalidEvent
	}
	switch scope {
	case ScopeIM:
		return "realtime:v1:connection:im:" + strconv.FormatUint(uint64(userID), 10), nil
	case ScopeGame:
		if roomID == 0 {
			return "", ErrInvalidEvent
		}
		return "realtime:v1:connection:game:" + strconv.FormatUint(uint64(roomID), 10) + ":" + strconv.FormatUint(uint64(userID), 10), nil
	default:
		return "", ErrInvalidEvent
	}
}

func roomSequenceKey(roomID uint) string {
	return "realtime:v1:room:" + strconv.FormatUint(uint64(roomID), 10) + ":seq"
}

func roomLogKey(roomID uint) string {
	return "realtime:v1:room:" + strconv.FormatUint(uint64(roomID), 10) + ":log"
}

func parseRedisInt(value any) (int64, error) {
	switch typed := value.(type) {
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	case int64:
		return typed, nil
	default:
		return 0, fmt.Errorf("unsupported Redis integer %T", value)
	}
}

func decodeControlEvent(payload []byte, event *ControlEvent) error {
	parts := strings.Split(string(payload), "|")
	if len(parts) != 5 {
		return ErrInvalidEvent
	}
	roomID, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return err
	}
	userID, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil {
		return err
	}
	*event = ControlEvent{
		Scope: ConnectionScope(parts[0]), RoomID: uint(roomID), UserID: uint(userID),
		TargetInstanceID: parts[3], ConnectionID: parts[4],
	}
	if event.TargetInstanceID == "" || event.ConnectionID == "" ||
		(event.Scope != ScopeGame && event.Scope != ScopeIM) {
		return ErrInvalidEvent
	}
	return nil
}

func invokeHandler(kind string, handler func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("[RealtimeBus] %s handler panic: %v", kind, recovered)
		}
	}()
	handler()
}
