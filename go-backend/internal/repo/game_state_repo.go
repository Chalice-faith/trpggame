package repo

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/model"
)

const DefaultGameRuntimeTTL = 24 * time.Hour

const maxCachedActionResponseBytes = 256 << 10

const maxActionRuntimeEffects = 8

const (
	maxRuntimeSummaryBytes   = 65535
	maxRuntimeCollectionSize = 512
	maxRuntimeMessages       = 10
)

var (
	ErrInvalidGameRuntimeState   = errors.New("invalid game runtime state")
	ErrGameRuntimeUnavailable    = errors.New("game runtime unavailable")
	ErrGameRuntimeConflict       = errors.New("game runtime turn conflict")
	ErrGameRuntimeNotPlaying     = errors.New("game runtime is not playing")
	ErrActionIdempotencyConflict = errors.New("action request ID was reused")
	ErrInsufficientItemQuantity  = errors.New("insufficient item quantity")
)

var integerRuntimeFields = map[string]struct{}{
	"hp": {}, "max_hp": {}, "mp": {}, "max_mp": {},
	"san": {}, "ac": {}, "level": {},
}

var initializeSoloRuntimeScript = redis.NewScript(`
redis.call("DEL", unpack(KEYS))
redis.call("SET", KEYS[1], ARGV[2])
redis.call("SET", KEYS[2], ARGV[3])
redis.call("RPUSH", KEYS[3], ARGV[4])
redis.call("SET", KEYS[4], ARGV[5])
redis.call("LPUSH", KEYS[5], ARGV[6])
if #ARGV > 6 then
  redis.call("HSET", KEYS[6], unpack(ARGV, 7))
end
for _, key in ipairs(KEYS) do
  redis.call("EXPIRE", key, ARGV[1])
end
return 1
`)

var deleteSoloRuntimeScript = redis.NewScript(`
return redis.call("DEL", unpack(KEYS))
`)

var findActionResultScript = redis.NewScript(`
local cached = redis.call("HGET", KEYS[1], ARGV[1])
if not cached then
  return {0, -1, ""}
end
local current = tonumber(redis.call("GET", KEYS[2]))
if not current or current < 0 or current % 1 ~= 0 then
  return {2, -1, cached}
end
return {1, current, cached}
`)

var captureSoloRuntimeScript = redis.NewScript(`
local status_type = redis.call("TYPE", KEYS[1]).ok
local turn_type = redis.call("TYPE", KEYS[2]).ok
local order_type = redis.call("TYPE", KEYS[3]).ok
local summary_type = redis.call("TYPE", KEYS[4]).ok
local rounds_type = redis.call("TYPE", KEYS[5]).ok
local player_type = redis.call("TYPE", KEYS[6]).ok
local items_type = redis.call("TYPE", KEYS[7]).ok
local buffs_type = redis.call("TYPE", KEYS[8]).ok
if status_type ~= "string" or turn_type ~= "string" or order_type ~= "list" or
   summary_type ~= "string" or rounds_type ~= "list" or player_type ~= "hash" or
   (items_type ~= "none" and items_type ~= "set") or
   (buffs_type ~= "none" and buffs_type ~= "hash") then
  return {0}
end
local status = redis.call("GET", KEYS[1])
local turn = tonumber(redis.call("GET", KEYS[2]))
if (status ~= "playing" and status ~= "paused") or not turn or turn < 0 or turn % 1 ~= 0 then
  return {0}
end
return {
  1,
  status,
  tostring(turn),
  redis.call("LRANGE", KEYS[3], 0, -1),
  redis.call("GET", KEYS[4]),
  redis.call("LRANGE", KEYS[5], 0, -1),
  redis.call("HGETALL", KEYS[6]),
  redis.call("SMEMBERS", KEYS[7]),
  redis.call("HGETALL", KEYS[8])
}
`)

var restoreSoloRuntimeScript = redis.NewScript(`
redis.call("DEL", unpack(KEYS))
redis.call("SET", KEYS[1], ARGV[2])
redis.call("SET", KEYS[2], ARGV[3])
redis.call("SET", KEYS[4], ARGV[4])
local index = 5
local order_count = tonumber(ARGV[index])
index = index + 1
for _ = 1, order_count do
  redis.call("RPUSH", KEYS[3], ARGV[index])
  index = index + 1
end
local message_count = tonumber(ARGV[index])
index = index + 1
for _ = 1, message_count do
  redis.call("RPUSH", KEYS[5], ARGV[index])
  index = index + 1
end
local player_count = tonumber(ARGV[index])
index = index + 1
if player_count > 0 then
  redis.call("HSET", KEYS[6], unpack(ARGV, index, index + player_count * 2 - 1))
  index = index + player_count * 2
end
local item_count = tonumber(ARGV[index])
index = index + 1
for _ = 1, item_count do
  redis.call("SADD", KEYS[8], ARGV[index])
  index = index + 1
end
local buff_count = tonumber(ARGV[index])
index = index + 1
if buff_count > 0 then
  redis.call("HSET", KEYS[9], unpack(ARGV, index, index + buff_count * 2 - 1))
end
for _, key in ipairs(KEYS) do
  if redis.call("EXISTS", key) == 1 then
    redis.call("EXPIRE", key, ARGV[1])
  end
end
return 1
`)

var commitActionRuntimeScript = redis.NewScript(`
local cached = redis.call("HGET", KEYS[7], ARGV[3])
if cached then
  local current = tonumber(redis.call("GET", KEYS[2])) or -1
  return {2, current, cached}
end
local status_type = redis.call("TYPE", KEYS[1]).ok
local turn_type = redis.call("TYPE", KEYS[2]).ok
local rounds_type = redis.call("TYPE", KEYS[5]).ok
local player_type = redis.call("TYPE", KEYS[6]).ok
local actions_type = redis.call("TYPE", KEYS[7]).ok
local items_type = redis.call("TYPE", KEYS[8]).ok
local buffs_type = redis.call("TYPE", KEYS[9]).ok
if status_type ~= "string" or turn_type ~= "string" or rounds_type ~= "list" or
   player_type ~= "hash" or (actions_type ~= "none" and actions_type ~= "hash") or
   (items_type ~= "none" and items_type ~= "set") or
   (buffs_type ~= "none" and buffs_type ~= "hash") then
  return {5, -1, ""}
end
if redis.call("GET", KEYS[1]) ~= "playing" then
  return {3, -1, ""}
end
local current = tonumber(redis.call("GET", KEYS[2]))
if not current or current < 0 or current % 1 ~= 0 or redis.call("EXISTS", KEYS[6]) ~= 1 then
  return {5, -1, ""}
end
local expected = tonumber(ARGV[2])
if current ~= expected then
  return {4, current, ""}
end
local change_count = tonumber(ARGV[7])
local argument_index = 8 + change_count * 2
local item_count = tonumber(ARGV[argument_index])
argument_index = argument_index + 1
local inventory = {}
for _, member in ipairs(redis.call("SMEMBERS", KEYS[8])) do
  local first = string.find(member, "|", 1, true)
  local second = first and string.find(member, "|", first + 1, true)
  if not first or not second then
    return {6, current, ""}
  end
  local name = string.sub(member, 1, first - 1)
  local quantity = tonumber(string.sub(member, first + 1, second - 1))
  if name == "" or not quantity or quantity <= 0 or quantity % 1 ~= 0 or inventory[name] then
    return {6, current, ""}
  end
  inventory[name] = {
    quantity = quantity,
    description = string.sub(member, second + 1)
  }
end
for index = 1, item_count do
  local name = ARGV[argument_index]
  local delta = tonumber(ARGV[argument_index + 1])
  local description = ARGV[argument_index + 2]
  argument_index = argument_index + 3
  local existing = inventory[name]
  local quantity = (existing and existing.quantity or 0) + delta
  if quantity < 0 then
    return {7, current, ""}
  elseif quantity == 0 then
    inventory[name] = nil
  else
    if description == "" and existing then
      description = existing.description
    end
    inventory[name] = {quantity = quantity, description = description}
  end
end
local buff_count = tonumber(ARGV[argument_index])
argument_index = argument_index + 1
local buff_arguments = {}
local existing_buffs = redis.call("HGETALL", KEYS[9])
for index = 1, #existing_buffs, 2 do
  local duration = tonumber(existing_buffs[index + 1])
  if existing_buffs[index] == "" or not duration or duration <= 0 or duration % 1 ~= 0 then
    return {6, current, ""}
  end
end
for index = 1, buff_count do
  table.insert(buff_arguments, ARGV[argument_index])
  table.insert(buff_arguments, ARGV[argument_index + 1])
  argument_index = argument_index + 2
end
if change_count > 0 then
  redis.call("HSET", KEYS[6], unpack(ARGV, 8, 7 + change_count * 2))
end
if item_count > 0 then
  redis.call("DEL", KEYS[8])
  for name, item in pairs(inventory) do
    redis.call("SADD", KEYS[8], name .. "|" .. item.quantity .. "|" .. item.description)
  end
end
if buff_count > 0 then
  redis.call("HSET", KEYS[9], unpack(buff_arguments))
end
redis.call("LPUSH", KEYS[5], ARGV[5], ARGV[6])
redis.call("LTRIM", KEYS[5], 0, 9)
local next_turn = current + 1
redis.call("SET", KEYS[2], next_turn)
redis.call("HSET", KEYS[7], ARGV[3], ARGV[4])
for _, key in ipairs(KEYS) do
  redis.call("EXPIRE", key, ARGV[1])
end
return {1, next_turn, ARGV[4]}
`)

type cachedActionResult struct {
	Fingerprint string          `json:"fingerprint"`
	Response    json.RawMessage `json:"response"`
}

type ActionCommitResult = model.ActionCommitResult

// RedisGameStateRepo 管理文档约定的游戏运行态键。
type RedisGameStateRepo struct {
	client redis.Scripter
	ttl    time.Duration
}

func NewRedisGameStateRepo(client redis.Scripter, ttl time.Duration) (*RedisGameStateRepo, error) {
	if client == nil || ttl <= 0 || ttl%time.Second != 0 {
		return nil, ErrInvalidGameRuntimeState
	}
	return &RedisGameStateRepo{client: client, ttl: ttl}, nil
}

// InitializeSoloRoom 原子替换指定单人房间的运行态，避免留下半初始化键。
func (r *RedisGameStateRepo) InitializeSoloRoom(
	ctx context.Context,
	state *model.SoloRuntimeState,
) error {
	arguments, err := r.initializeArguments(state)
	if err != nil {
		return err
	}

	if err := initializeSoloRuntimeScript.Run(
		ctx,
		r.client,
		soloRuntimeCleanupKeys(state.RoomID, state.UserID),
		arguments...,
	).Err(); err != nil {
		return fmt.Errorf("%w: initialize solo room: %v", ErrGameRuntimeUnavailable, err)
	}
	return nil
}

// CommitAction 以请求 ID 幂等、预期回合 CAS 的方式原子提交一次玩家行动。
func (r *RedisGameStateRepo) CommitAction(
	ctx context.Context,
	mutation *model.ActionRuntimeMutation,
) (*ActionCommitResult, error) {
	keys, arguments, fingerprint, err := r.actionCommitArguments(mutation)
	if err != nil {
		return nil, err
	}

	values, err := commitActionRuntimeScript.Run(ctx, r.client, keys, arguments...).Slice()
	if err != nil {
		return nil, fmt.Errorf("%w: commit action: %v", ErrGameRuntimeUnavailable, err)
	}
	if len(values) != 3 {
		return nil, fmt.Errorf("%w: invalid action commit result", ErrGameRuntimeUnavailable)
	}
	code, ok := redisInt64(values[0])
	if !ok {
		return nil, fmt.Errorf("%w: invalid action commit code", ErrGameRuntimeUnavailable)
	}
	turn, ok := redisInt64(values[1])
	if !ok {
		return nil, fmt.Errorf("%w: invalid action commit turn", ErrGameRuntimeUnavailable)
	}

	switch code {
	case 1, 2:
		cached, err := decodeCachedActionResult(values[2])
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrGameRuntimeUnavailable, err)
		}
		if cached.Fingerprint != fingerprint {
			return nil, ErrActionIdempotencyConflict
		}
		return &ActionCommitResult{
			Duplicate:    code == 2,
			CurrentTurn:  int(turn),
			ResponseJSON: append(json.RawMessage(nil), cached.Response...),
		}, nil
	case 3:
		return nil, ErrGameRuntimeNotPlaying
	case 4:
		return &ActionCommitResult{CurrentTurn: int(turn)}, ErrGameRuntimeConflict
	case 5:
		return nil, ErrGameRuntimeUnavailable
	case 6:
		return nil, ErrGameRuntimeUnavailable
	case 7:
		return nil, ErrInsufficientItemQuantity
	default:
		return nil, fmt.Errorf("%w: unknown action commit code %d", ErrGameRuntimeUnavailable, code)
	}
}

// FindActionResult 在调用 AI 前查询已提交请求，避免幂等重试重复推理。
func (r *RedisGameStateRepo) FindActionResult(
	ctx context.Context,
	roomID uint,
	requestID string,
	fingerprint string,
) (*model.ActionCommitResult, bool, error) {
	if roomID == 0 {
		return nil, false, ErrInvalidGameRuntimeState
	}
	parsedRequestID, err := uuid.Parse(strings.TrimSpace(requestID))
	if err != nil {
		return nil, false, ErrInvalidGameRuntimeState
	}
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	decodedFingerprint, err := hex.DecodeString(fingerprint)
	if err != nil || len(decodedFingerprint) != 32 {
		return nil, false, ErrInvalidGameRuntimeState
	}

	values, err := findActionResultScript.Run(
		ctx,
		r.client,
		[]string{actionResultsKey(roomID), runtimeTurnKey(roomID)},
		parsedRequestID.String(),
	).Slice()
	if err != nil {
		return nil, false, fmt.Errorf("%w: find action result: %v", ErrGameRuntimeUnavailable, err)
	}
	if len(values) != 3 {
		return nil, false, ErrGameRuntimeUnavailable
	}
	code, ok := redisInt64(values[0])
	if !ok {
		return nil, false, ErrGameRuntimeUnavailable
	}
	if code == 0 {
		return nil, false, nil
	}
	if code != 1 {
		return nil, false, ErrGameRuntimeUnavailable
	}
	turn, ok := redisInt64(values[1])
	if !ok {
		return nil, false, ErrGameRuntimeUnavailable
	}
	cached, err := decodeCachedActionResult(values[2])
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrGameRuntimeUnavailable, err)
	}
	if cached.Fingerprint != fingerprint {
		return nil, false, ErrActionIdempotencyConflict
	}
	return &model.ActionCommitResult{
		Duplicate:    true,
		CurrentTurn:  int(turn),
		ResponseJSON: append(json.RawMessage(nil), cached.Response...),
	}, true, nil
}

// CaptureSoloRoom 原子读取单人房间的完整可持久运行态。
func (r *RedisGameStateRepo) CaptureSoloRoom(
	ctx context.Context,
	roomID uint,
	userID uint,
) (*model.SoloRuntimeSnapshot, error) {
	if roomID == 0 || userID == 0 {
		return nil, ErrInvalidGameRuntimeState
	}
	runtime := runtimeKeys(roomID, userID)
	keys := []string{
		runtime[0],
		runtime[1],
		runtime[2],
		runtime[3],
		runtime[4],
		runtime[5],
		itemStateKey(roomID, userID),
		buffStateKey(roomID, userID),
	}
	values, err := captureSoloRuntimeScript.Run(ctx, r.client, keys).Slice()
	if err != nil {
		return nil, fmt.Errorf("%w: capture solo room: %v", ErrGameRuntimeUnavailable, err)
	}
	if len(values) == 1 {
		return nil, ErrGameRuntimeUnavailable
	}
	if len(values) != 9 {
		return nil, fmt.Errorf("%w: malformed snapshot result", ErrGameRuntimeUnavailable)
	}
	code, ok := redisInt64(values[0])
	if !ok || code != 1 {
		return nil, ErrGameRuntimeUnavailable
	}
	snapshot, err := decodeSoloRuntimeSnapshot(roomID, userID, values[1:])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGameRuntimeUnavailable, err)
	}
	return snapshot, nil
}

// RestoreSoloRoom 原子替换单人房间运行态，并清除不属于存档的行动幂等缓存。
func (r *RedisGameStateRepo) RestoreSoloRoom(
	ctx context.Context,
	snapshot *model.SoloRuntimeSnapshot,
) error {
	keys, arguments, err := r.snapshotRestoreArguments(snapshot)
	if err != nil {
		return err
	}
	if err := restoreSoloRuntimeScript.Run(ctx, r.client, keys, arguments...).Err(); err != nil {
		return fmt.Errorf("%w: restore solo room: %v", ErrGameRuntimeUnavailable, err)
	}
	return nil
}

// DeleteSoloRoom 原子删除指定单人房间的全部运行态键，用于失败补偿。
func (r *RedisGameStateRepo) DeleteSoloRoom(ctx context.Context, roomID, userID uint) error {
	if roomID == 0 || userID == 0 {
		return ErrInvalidGameRuntimeState
	}
	if err := deleteSoloRuntimeScript.Run(
		ctx,
		r.client,
		soloRuntimeCleanupKeys(roomID, userID),
	).Err(); err != nil {
		return fmt.Errorf("%w: delete solo room: %v", ErrGameRuntimeUnavailable, err)
	}
	return nil
}

func (r *RedisGameStateRepo) actionCommitArguments(
	mutation *model.ActionRuntimeMutation,
) ([]string, []any, string, error) {
	if mutation == nil || mutation.RoomID == 0 || mutation.UserID == 0 ||
		mutation.ExpectedTurn < 0 || len(mutation.Messages) != 2 {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}
	requestID, err := uuid.Parse(strings.TrimSpace(mutation.RequestID))
	if err != nil {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}
	fingerprint := strings.ToLower(strings.TrimSpace(mutation.RequestFingerprint))
	decodedFingerprint, err := hex.DecodeString(fingerprint)
	if err != nil || len(decodedFingerprint) != 32 {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}
	if len(mutation.ResponseJSON) == 0 || len(mutation.ResponseJSON) > maxCachedActionResponseBytes ||
		!json.Valid(mutation.ResponseJSON) {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}

	encodedMessages := make([]string, 2)
	expectedRoles := []string{"user", "assistant"}
	for index, message := range mutation.Messages {
		message.Role = strings.TrimSpace(message.Role)
		message.Content = strings.TrimSpace(message.Content)
		if message.Role != expectedRoles[index] || message.Content == "" || len(message.Content) > 64<<10 {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		encodedMessages[index] = string(encoded)
	}

	fields, normalizedChanges, err := normalizePlayerState(mutation.PlayerStateChanges, false)
	if err != nil {
		return nil, nil, "", err
	}
	if _, changesIdentity := normalizedChanges["character_id"]; changesIdentity {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}
	if len(mutation.ItemMutations)+len(mutation.BuffMutations) > maxActionRuntimeEffects {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}
	cached, err := json.Marshal(cachedActionResult{
		Fingerprint: fingerprint,
		Response:    append(json.RawMessage(nil), mutation.ResponseJSON...),
	})
	if err != nil {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}

	arguments := []any{
		int64(r.ttl / time.Second),
		mutation.ExpectedTurn,
		requestID.String(),
		string(cached),
		encodedMessages[0],
		encodedMessages[1],
		len(fields),
	}
	for _, field := range fields {
		arguments = append(arguments, field, normalizedChanges[field])
	}
	arguments = append(arguments, len(mutation.ItemMutations))
	for _, item := range mutation.ItemMutations {
		item.Name = strings.TrimSpace(item.Name)
		item.Description = strings.TrimSpace(item.Description)
		if item.Name == "" || strings.Contains(item.Name, "|") || item.QuantityDelta == 0 ||
			utf8.RuneCountInString(item.Name) > 200 ||
			utf8.RuneCountInString(item.Description) > 1000 {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		if item.QuantityDelta < 0 && item.Description != "" {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		arguments = append(arguments, item.Name, item.QuantityDelta, item.Description)
	}
	arguments = append(arguments, len(mutation.BuffMutations))
	for _, buff := range mutation.BuffMutations {
		buff.Name = strings.TrimSpace(buff.Name)
		if buff.Name == "" || buff.Duration <= 0 || utf8.RuneCountInString(buff.Name) > 200 {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		arguments = append(arguments, buff.Name, buff.Duration)
	}
	keys := append(
		runtimeKeys(mutation.RoomID, mutation.UserID),
		actionResultsKey(mutation.RoomID),
		itemStateKey(mutation.RoomID, mutation.UserID),
		buffStateKey(mutation.RoomID, mutation.UserID),
	)
	return keys, arguments, fingerprint, nil
}

func (r *RedisGameStateRepo) initializeArguments(state *model.SoloRuntimeState) ([]any, error) {
	if state == nil || state.RoomID == 0 || state.UserID == 0 || state.Turn < 0 ||
		state.Status != model.RoomStatusPlaying {
		return nil, ErrInvalidGameRuntimeState
	}

	message := model.RuntimeMessage{
		Role:    strings.TrimSpace(state.Opening.Role),
		Content: strings.TrimSpace(state.Opening.Content),
	}
	if message.Role != "assistant" || message.Content == "" || len(state.PlayerState) == 0 {
		return nil, ErrInvalidGameRuntimeState
	}
	encodedMessage, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("%w: encode opening message: %v", ErrInvalidGameRuntimeState, err)
	}

	arguments := []any{
		int64(r.ttl / time.Second),
		string(state.Status),
		state.Turn,
		state.UserID,
		strings.TrimSpace(state.Summary),
		string(encodedMessage),
	}
	fields, normalizedState, err := normalizePlayerState(state.PlayerState, true)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		arguments = append(arguments, field, normalizedState[field])
	}
	return arguments, nil
}

func decodeSoloRuntimeSnapshot(
	roomID uint,
	userID uint,
	values []any,
) (*model.SoloRuntimeSnapshot, error) {
	statusText, ok := redisString(values[0])
	if !ok {
		return nil, errors.New("snapshot status must be text")
	}
	turn, ok := redisInt64(values[1])
	if !ok || turn < 0 || int64(int(turn)) != turn {
		return nil, errors.New("snapshot turn is invalid")
	}
	orderValues, ok := redisStringSlice(values[2])
	if !ok {
		return nil, errors.New("snapshot turn order is invalid")
	}
	turnOrder := make([]uint, len(orderValues))
	for index, value := range orderValues {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 || uint64(uint(parsed)) != parsed {
			return nil, errors.New("snapshot turn order contains an invalid user")
		}
		turnOrder[index] = uint(parsed)
	}
	summary, ok := redisString(values[3])
	if !ok {
		return nil, errors.New("snapshot summary must be text")
	}
	encodedMessages, ok := redisStringSlice(values[4])
	if !ok {
		return nil, errors.New("snapshot messages are invalid")
	}
	playerValues, ok := redisStringSlice(values[5])
	if !ok || len(playerValues)%2 != 0 {
		return nil, errors.New("snapshot player state is invalid")
	}
	playerState := make(map[string]string, len(playerValues)/2)
	for index := 0; index < len(playerValues); index += 2 {
		if _, exists := playerState[playerValues[index]]; exists {
			return nil, errors.New("snapshot player state contains duplicate fields")
		}
		playerState[playerValues[index]] = playerValues[index+1]
	}
	itemValues, ok := redisStringSlice(values[6])
	if !ok {
		return nil, errors.New("snapshot items are invalid")
	}
	buffValues, ok := redisStringSlice(values[7])
	if !ok || len(buffValues)%2 != 0 {
		return nil, errors.New("snapshot buffs are invalid")
	}

	snapshot := &model.SoloRuntimeSnapshot{
		Version:        model.SoloRuntimeSnapshotVersion,
		RoomID:         roomID,
		UserID:         userID,
		Status:         model.RoomStatus(statusText),
		Turn:           int(turn),
		TurnOrder:      turnOrder,
		PlayerState:    playerState,
		Summary:        summary,
		RecentMessages: make([]model.RuntimeMessage, len(encodedMessages)),
	}
	for index, encoded := range encodedMessages {
		if err := decodeRuntimeMessage(encoded, &snapshot.RecentMessages[index]); err != nil {
			return nil, err
		}
	}
	for _, encoded := range itemValues {
		parts := strings.SplitN(encoded, "|", 3)
		if len(parts) != 3 {
			return nil, errors.New("snapshot contains a malformed item")
		}
		quantity, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, errors.New("snapshot contains an invalid item quantity")
		}
		snapshot.Items = append(snapshot.Items, model.RuntimeItem{
			Name: parts[0], Quantity: quantity, Description: parts[2],
		})
	}
	for index := 0; index < len(buffValues); index += 2 {
		duration, err := strconv.Atoi(buffValues[index+1])
		if err != nil {
			return nil, errors.New("snapshot contains an invalid buff duration")
		}
		snapshot.Buffs = append(snapshot.Buffs, model.RuntimeBuff{
			Name: buffValues[index], Duration: duration,
		})
	}
	return normalizeSoloRuntimeSnapshot(snapshot)
}

func (r *RedisGameStateRepo) snapshotRestoreArguments(
	snapshot *model.SoloRuntimeSnapshot,
) ([]string, []any, error) {
	normalized, err := normalizeSoloRuntimeSnapshot(snapshot)
	if err != nil {
		return nil, nil, err
	}
	arguments := []any{
		int64(r.ttl / time.Second),
		string(normalized.Status),
		normalized.Turn,
		normalized.Summary,
		len(normalized.TurnOrder),
	}
	for _, userID := range normalized.TurnOrder {
		arguments = append(arguments, userID)
	}
	arguments = append(arguments, len(normalized.RecentMessages))
	for _, message := range normalized.RecentMessages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return nil, nil, ErrInvalidGameRuntimeState
		}
		arguments = append(arguments, string(encoded))
	}
	fields, playerState, err := normalizePlayerState(normalized.PlayerState, true)
	if err != nil {
		return nil, nil, err
	}
	arguments = append(arguments, len(fields))
	for _, field := range fields {
		arguments = append(arguments, field, playerState[field])
	}
	arguments = append(arguments, len(normalized.Items))
	for _, item := range normalized.Items {
		arguments = append(arguments, fmt.Sprintf("%s|%d|%s", item.Name, item.Quantity, item.Description))
	}
	arguments = append(arguments, len(normalized.Buffs))
	for _, buff := range normalized.Buffs {
		arguments = append(arguments, buff.Name, buff.Duration)
	}
	return soloRuntimeCleanupKeys(normalized.RoomID, normalized.UserID), arguments, nil
}

func normalizeSoloRuntimeSnapshot(
	snapshot *model.SoloRuntimeSnapshot,
) (*model.SoloRuntimeSnapshot, error) {
	if snapshot == nil || snapshot.Version != model.SoloRuntimeSnapshotVersion ||
		snapshot.RoomID == 0 || snapshot.UserID == 0 || snapshot.Turn < 0 ||
		(snapshot.Status != model.RoomStatusPlaying && snapshot.Status != model.RoomStatusPaused) ||
		len(snapshot.TurnOrder) != 1 || snapshot.TurnOrder[0] != snapshot.UserID ||
		len(snapshot.Summary) > maxRuntimeSummaryBytes ||
		len(snapshot.RecentMessages) == 0 || len(snapshot.RecentMessages) > maxRuntimeMessages ||
		len(snapshot.Items) > maxRuntimeCollectionSize || len(snapshot.Buffs) > maxRuntimeCollectionSize {
		return nil, ErrInvalidGameRuntimeState
	}
	fields, playerState, err := normalizePlayerState(snapshot.PlayerState, true)
	if err != nil || len(fields) > maxRuntimeCollectionSize {
		return nil, ErrInvalidGameRuntimeState
	}
	normalized := &model.SoloRuntimeSnapshot{
		Version: model.SoloRuntimeSnapshotVersion, RoomID: snapshot.RoomID, UserID: snapshot.UserID,
		Status: snapshot.Status, Turn: snapshot.Turn, TurnOrder: append([]uint(nil), snapshot.TurnOrder...),
		PlayerState: playerState, Summary: strings.TrimSpace(snapshot.Summary),
		RecentMessages: make([]model.RuntimeMessage, len(snapshot.RecentMessages)),
		Items:          make([]model.RuntimeItem, len(snapshot.Items)),
		Buffs:          make([]model.RuntimeBuff, len(snapshot.Buffs)),
	}
	for index, message := range snapshot.RecentMessages {
		message.Role = strings.TrimSpace(message.Role)
		message.Content = strings.TrimSpace(message.Content)
		expectedRole := "assistant"
		if index%2 == 1 {
			expectedRole = "user"
		}
		if message.Role != expectedRole || message.Content == "" || len(message.Content) > 64<<10 {
			return nil, ErrInvalidGameRuntimeState
		}
		normalized.RecentMessages[index] = message
	}
	itemNames := make(map[string]struct{}, len(snapshot.Items))
	for index, item := range snapshot.Items {
		item.Name = strings.TrimSpace(item.Name)
		item.Description = strings.TrimSpace(item.Description)
		if item.Name == "" || strings.Contains(item.Name, "|") || item.Quantity <= 0 ||
			utf8.RuneCountInString(item.Name) > 200 || utf8.RuneCountInString(item.Description) > 1000 {
			return nil, ErrInvalidGameRuntimeState
		}
		if _, exists := itemNames[item.Name]; exists {
			return nil, ErrInvalidGameRuntimeState
		}
		itemNames[item.Name] = struct{}{}
		normalized.Items[index] = item
	}
	buffNames := make(map[string]struct{}, len(snapshot.Buffs))
	for index, buff := range snapshot.Buffs {
		buff.Name = strings.TrimSpace(buff.Name)
		if buff.Name == "" || buff.Duration <= 0 || utf8.RuneCountInString(buff.Name) > 200 {
			return nil, ErrInvalidGameRuntimeState
		}
		if _, exists := buffNames[buff.Name]; exists {
			return nil, ErrInvalidGameRuntimeState
		}
		buffNames[buff.Name] = struct{}{}
		normalized.Buffs[index] = buff
	}
	sort.Slice(normalized.Items, func(i, j int) bool { return normalized.Items[i].Name < normalized.Items[j].Name })
	sort.Slice(normalized.Buffs, func(i, j int) bool { return normalized.Buffs[i].Name < normalized.Buffs[j].Name })
	return normalized, nil
}

func decodeRuntimeMessage(encoded string, destination *model.RuntimeMessage) error {
	decoder := json.NewDecoder(bytes.NewBufferString(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("snapshot contains a malformed message")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("snapshot contains trailing message data")
	}
	return nil
}

func normalizePlayerState(
	state map[string]string,
	requireValues bool,
) ([]string, map[string]string, error) {
	if requireValues && len(state) == 0 {
		return nil, nil, ErrInvalidGameRuntimeState
	}
	fields := make([]string, 0, len(state))
	normalized := make(map[string]string, len(state))
	for field, value := range state {
		field = strings.TrimSpace(field)
		value = strings.TrimSpace(value)
		if field == "" || len(field) > 64 || len(value) > 8<<10 {
			return nil, nil, ErrInvalidGameRuntimeState
		}
		if _, numeric := integerRuntimeFields[field]; numeric {
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return nil, nil, ErrInvalidGameRuntimeState
			}
		}
		if _, exists := normalized[field]; exists {
			return nil, nil, ErrInvalidGameRuntimeState
		}
		normalized[field] = value
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields, normalized, nil
}

func runtimeKeys(roomID, userID uint) []string {
	room := strconv.FormatUint(uint64(roomID), 10)
	user := strconv.FormatUint(uint64(userID), 10)
	prefix := "room:" + room
	return []string{
		prefix + ":status",
		prefix + ":turn",
		prefix + ":turn_order",
		prefix + ":summary",
		prefix + ":rounds",
		prefix + ":player:" + user,
	}
}

func soloRuntimeCleanupKeys(roomID, userID uint) []string {
	return append(
		runtimeKeys(roomID, userID),
		actionResultsKey(roomID),
		itemStateKey(roomID, userID),
		buffStateKey(roomID, userID),
	)
}

func actionResultsKey(roomID uint) string {
	return "room:" + strconv.FormatUint(uint64(roomID), 10) + ":actions"
}

func runtimeTurnKey(roomID uint) string {
	return fmt.Sprintf("room:%d:turn", roomID)
}

func itemStateKey(roomID, userID uint) string {
	return fmt.Sprintf("room:%d:player:%d:items", roomID, userID)
}

func buffStateKey(roomID, userID uint) string {
	return fmt.Sprintf("room:%d:player:%d:buffs", roomID, userID)
}

func redisInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func redisString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

func redisStringSlice(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		if stringsValue, stringsOK := value.([]string); stringsOK {
			return append([]string(nil), stringsValue...), true
		}
		return nil, false
	}
	result := make([]string, len(values))
	for index, item := range values {
		text, ok := redisString(item)
		if !ok {
			return nil, false
		}
		result[index] = text
	}
	return result, true
}

func decodeCachedActionResult(value any) (*cachedActionResult, error) {
	var encoded []byte
	switch typed := value.(type) {
	case string:
		encoded = []byte(typed)
	case []byte:
		encoded = typed
	default:
		return nil, errors.New("cached action result must be JSON text")
	}
	var cached cachedActionResult
	if err := json.Unmarshal(encoded, &cached); err != nil || cached.Fingerprint == "" ||
		len(cached.Response) == 0 || !json.Valid(cached.Response) {
		return nil, errors.New("cached action result is malformed")
	}
	return &cached, nil
}
