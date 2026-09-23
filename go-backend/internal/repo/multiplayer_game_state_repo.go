package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/model"
)

const DefaultMultiplayerStartLeaseTTL = 2 * time.Minute

var (
	ErrMultiplayerStartInProgress = errors.New("multiplayer start already in progress")
	ErrMultiplayerRuntimeConflict = errors.New("multiplayer runtime conflict")
)

var acquireMultiplayerStartLeaseScript = redis.NewScript(`
if redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then return 1 end
return 0`)

var releaseMultiplayerStartLeaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)

var initializeMultiplayerRuntimeScript = redis.NewScript(`
redis.call('DEL', unpack(KEYS))
redis.call('SET', KEYS[1], '2')
redis.call('SET', KEYS[2], 'provisional')
redis.call('SET', KEYS[3], ARGV[2])
redis.call('SET', KEYS[4], '0')
redis.call('SET', KEYS[6], '')
redis.call('SET', KEYS[7], ARGV[3])
redis.call('LPUSH', KEYS[8], ARGV[4])
local player_count = tonumber(ARGV[5])
local argument_index = 6
for player_index = 1, player_count do
  local user_id = ARGV[argument_index]
  local character_id = ARGV[argument_index + 1]
  local field_count = tonumber(ARGV[argument_index + 2])
  argument_index = argument_index + 3
  redis.call('RPUSH', KEYS[5], user_id)
  redis.call('RPUSH', KEYS[9], user_id)
  local player_key_index = 9 + ((player_index - 1) * 3) + 1
  redis.call('HSET', KEYS[player_key_index], 'character_id', character_id)
  for _ = 1, field_count do
    redis.call('HSET', KEYS[player_key_index], ARGV[argument_index], ARGV[argument_index + 1])
    argument_index = argument_index + 2
  end
end
for _, key in ipairs(KEYS) do
  if redis.call('EXISTS', key) == 1 then redis.call('EXPIRE', key, ARGV[1]) end
end
return 1`)

var activateMultiplayerRuntimeScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= '2' or redis.call('GET', KEYS[3]) ~= ARGV[1] then return 0 end
local status = redis.call('GET', KEYS[2])
if status == 'playing' then
  if redis.call('GET', KEYS[4]) == ARGV[2] then return 2 end
  return 0
end
if status ~= 'provisional' then return 0 end
redis.call('SET', KEYS[2], 'playing')
redis.call('SET', KEYS[4], ARGV[2])
redis.call('ZADD', KEYS[5], ARGV[3], ARGV[4])
for index = 1, 4 do redis.call('EXPIRE', KEYS[index], ARGV[5]) end
return 1`)

var pauseMultiplayerRuntimeScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= '2' or redis.call('GET', KEYS[3]) ~= ARGV[1] then return 0 end
local status = redis.call('GET', KEYS[2])
if status ~= 'provisional' and status ~= 'playing' and status ~= 'paused' then return 0 end
redis.call('SET', KEYS[2], 'paused')
redis.call('SET', KEYS[4], '')
redis.call('ZREM', KEYS[5], ARGV[2])
for index = 1, 4 do redis.call('EXPIRE', KEYS[index], ARGV[3]) end
return 1`)

var deleteProvisionalMultiplayerRuntimeScript = redis.NewScript(`
if redis.call('GET', KEYS[2]) ~= 'provisional' or redis.call('GET', KEYS[3]) ~= ARGV[1] then return 0 end
return redis.call('DEL', unpack(KEYS))`)

var readMultiplayerRuntimeScript = redis.NewScript(`
if redis.call('TYPE', KEYS[1]).ok ~= 'string' or redis.call('TYPE', KEYS[2]).ok ~= 'string' or
   redis.call('TYPE', KEYS[3]).ok ~= 'string' or redis.call('TYPE', KEYS[4]).ok ~= 'string' or
   redis.call('TYPE', KEYS[5]).ok ~= 'list' or redis.call('TYPE', KEYS[6]).ok ~= 'string' or
   redis.call('TYPE', KEYS[7]).ok ~= 'string' or redis.call('TYPE', KEYS[8]).ok ~= 'list' or
   redis.call('TYPE', KEYS[9]).ok ~= 'list' or
   (redis.call('TYPE', KEYS[10]).ok ~= 'none' and redis.call('TYPE', KEYS[10]).ok ~= 'hash') then return {0} end
local players = redis.call('LRANGE', KEYS[9], 0, -1)
local player_data = {}
for _, user_id in ipairs(players) do
  local player_key = ARGV[2] .. user_id
  local items_key = player_key .. ':items'
  local buffs_key = player_key .. ':buffs'
  table.insert(player_data, {user_id, redis.call('HGETALL', player_key), redis.call('SMEMBERS', items_key), redis.call('HGETALL', buffs_key)})
  redis.call('EXPIRE', player_key, ARGV[1])
  if redis.call('EXISTS', items_key) == 1 then redis.call('EXPIRE', items_key, ARGV[1]) end
  if redis.call('EXISTS', buffs_key) == 1 then redis.call('EXPIRE', buffs_key, ARGV[1]) end
end
for _, key in ipairs(KEYS) do redis.call('EXPIRE', key, ARGV[1]) end
return {
  1, redis.call('GET', KEYS[1]), redis.call('GET', KEYS[2]), redis.call('GET', KEYS[3]),
  redis.call('GET', KEYS[4]), redis.call('LRANGE', KEYS[5], 0, -1), redis.call('GET', KEYS[6]),
  redis.call('GET', KEYS[7]), redis.call('LRANGE', KEYS[8], 0, -1), player_data,
  redis.call('HGETALL', KEYS[10])
}`)

func (r *RedisGameStateRepo) AcquireMultiplayerStartLease(
	ctx context.Context,
	roomID uint,
	token string,
	ttl time.Duration,
) error {
	if roomID == 0 || token == "" || ttl < time.Second {
		return ErrInvalidGameRuntimeState
	}
	claimed, err := acquireMultiplayerStartLeaseScript.Run(
		ctx, r.client, []string{multiplayerStartLeaseKey(roomID)}, token, ttl.Milliseconds(),
	).Int64()
	if err != nil {
		return fmt.Errorf("%w: acquire multiplayer start lease: %v", ErrGameRuntimeUnavailable, err)
	}
	if claimed != 1 {
		return ErrMultiplayerStartInProgress
	}
	return nil
}

func (r *RedisGameStateRepo) ReleaseMultiplayerStartLease(ctx context.Context, roomID uint, token string) error {
	if roomID == 0 || token == "" {
		return ErrInvalidGameRuntimeState
	}
	if err := releaseMultiplayerStartLeaseScript.Run(
		ctx, r.client, []string{multiplayerStartLeaseKey(roomID)}, token,
	).Err(); err != nil {
		return fmt.Errorf("%w: release multiplayer start lease: %v", ErrGameRuntimeUnavailable, err)
	}
	return nil
}

func (r *RedisGameStateRepo) InitializeMultiplayerRoom(
	ctx context.Context,
	state *model.MultiplayerRuntimeState,
) error {
	keys, arguments, err := r.multiplayerInitializeArguments(state)
	if err != nil {
		return err
	}
	if err := initializeMultiplayerRuntimeScript.Run(ctx, r.client, keys, arguments...).Err(); err != nil {
		return fmt.Errorf("%w: initialize multiplayer room: %v", ErrGameRuntimeUnavailable, err)
	}
	return nil
}

func (r *RedisGameStateRepo) ActivateMultiplayerRoom(
	ctx context.Context,
	roomID uint,
	generation string,
	deadline time.Time,
) (*model.MultiplayerRuntimeSnapshot, error) {
	if roomID == 0 || uuid.Validate(generation) != nil || deadline.IsZero() {
		return nil, ErrInvalidGameRuntimeState
	}
	deadline = deadline.UTC()
	member := multiplayerDeadlineMember(roomID, generation, 0)
	code, err := activateMultiplayerRuntimeScript.Run(
		ctx,
		r.client,
		[]string{
			multiplayerRuntimeVersionKey(roomID), runtimeStatusKey(roomID), runtimeGenerationKey(roomID),
			multiplayerDeadlineKey(roomID), multiplayerDeadlineQueueKey(),
		},
		generation, deadline.UnixMilli(), deadline.UnixMilli(), member, int64(r.ttl/time.Second),
	).Int64()
	if err != nil {
		return nil, fmt.Errorf("%w: activate multiplayer room: %v", ErrGameRuntimeUnavailable, err)
	}
	if code != 1 && code != 2 {
		return nil, ErrMultiplayerRuntimeConflict
	}
	return r.GetMultiplayerRoom(ctx, roomID)
}

func (r *RedisGameStateRepo) PauseMultiplayerRoom(
	ctx context.Context,
	roomID uint,
	generation string,
) error {
	if roomID == 0 || uuid.Validate(generation) != nil {
		return ErrInvalidGameRuntimeState
	}
	code, err := pauseMultiplayerRuntimeScript.Run(
		ctx,
		r.client,
		[]string{
			multiplayerRuntimeVersionKey(roomID), runtimeStatusKey(roomID), runtimeGenerationKey(roomID),
			multiplayerDeadlineKey(roomID), multiplayerDeadlineQueueKey(),
		},
		generation, multiplayerDeadlineMember(roomID, generation, 0), int64(r.ttl/time.Second),
	).Int64()
	if err != nil {
		return fmt.Errorf("%w: pause multiplayer room: %v", ErrGameRuntimeUnavailable, err)
	}
	if code != 1 {
		return ErrMultiplayerRuntimeConflict
	}
	return nil
}

func (r *RedisGameStateRepo) DeleteProvisionalMultiplayerRoom(
	ctx context.Context,
	state *model.MultiplayerRuntimeState,
) error {
	if err := validateMultiplayerRuntimeState(state); err != nil {
		return err
	}
	keys := multiplayerRuntimeKeys(state.RoomID, state.Players)
	if err := deleteProvisionalMultiplayerRuntimeScript.Run(
		ctx, r.client, keys, state.Generation,
	).Err(); err != nil {
		return fmt.Errorf("%w: delete provisional multiplayer room: %v", ErrGameRuntimeUnavailable, err)
	}
	return nil
}

func (r *RedisGameStateRepo) GetMultiplayerRoom(
	ctx context.Context,
	roomID uint,
) (*model.MultiplayerRuntimeSnapshot, error) {
	if roomID == 0 {
		return nil, ErrInvalidGameRuntimeState
	}
	values, err := readMultiplayerRuntimeScript.Run(
		ctx,
		r.client,
		append(multiplayerCommonRuntimeKeys(roomID), multiplayerActionLeaseKey(roomID)),
		int64(r.ttl/time.Second), fmt.Sprintf("room:%d:player:", roomID),
	).Slice()
	if err != nil {
		return nil, fmt.Errorf("%w: read multiplayer room: %v", ErrGameRuntimeUnavailable, err)
	}
	return decodeMultiplayerRuntimeSnapshot(roomID, values)
}

func (r *RedisGameStateRepo) multiplayerInitializeArguments(
	state *model.MultiplayerRuntimeState,
) ([]string, []any, error) {
	if err := validateMultiplayerRuntimeState(state); err != nil {
		return nil, nil, err
	}
	opening, err := json.Marshal(state.Opening)
	if err != nil {
		return nil, nil, ErrInvalidGameRuntimeState
	}
	arguments := []any{
		int64(r.ttl / time.Second), state.Generation, state.SummaryMemory, opening, len(state.Players),
	}
	for _, player := range state.Players {
		fields := make([]string, 0, len(player.PlayerState))
		for field := range player.PlayerState {
			if strings.TrimSpace(field) == "" || field == "character_id" {
				continue
			}
			fields = append(fields, field)
		}
		sort.Strings(fields)
		arguments = append(arguments, player.UserID, player.CharacterID, len(fields))
		for _, field := range fields {
			arguments = append(arguments, field, player.PlayerState[field])
		}
	}
	return multiplayerRuntimeKeys(state.RoomID, state.Players), arguments, nil
}

func validateMultiplayerRuntimeState(state *model.MultiplayerRuntimeState) error {
	if state == nil || state.RoomID == 0 || uuid.Validate(state.Generation) != nil ||
		len(state.Players) < 2 || len(state.Players) != len(state.TurnOrder) ||
		state.Opening.Role != "assistant" || strings.TrimSpace(state.Opening.Content) == "" ||
		state.TurnTimeout < time.Second {
		return ErrInvalidGameRuntimeState
	}
	players := make(map[uint]model.MultiplayerRuntimePlayer, len(state.Players))
	characters := make(map[uint]struct{}, len(state.Players))
	for _, player := range state.Players {
		if player.UserID == 0 || player.CharacterID == 0 || player.PlayerState == nil {
			return ErrInvalidGameRuntimeState
		}
		if _, exists := players[player.UserID]; exists {
			return ErrInvalidGameRuntimeState
		}
		if _, exists := characters[player.CharacterID]; exists {
			return ErrInvalidGameRuntimeState
		}
		players[player.UserID] = player
		characters[player.CharacterID] = struct{}{}
	}
	for _, userID := range state.TurnOrder {
		if _, exists := players[userID]; !exists {
			return ErrInvalidGameRuntimeState
		}
		delete(players, userID)
	}
	if len(players) != 0 {
		return ErrInvalidGameRuntimeState
	}
	return nil
}

func decodeMultiplayerRuntimeSnapshot(roomID uint, values []any) (*model.MultiplayerRuntimeSnapshot, error) {
	if len(values) != 11 {
		return nil, fmt.Errorf("%w: invalid multiplayer runtime result", ErrGameRuntimeUnavailable)
	}
	code, ok := redisInt64(values[0])
	if !ok || code != 1 {
		return nil, ErrGameRuntimeUnavailable
	}
	version, ok := redisInt64(values[1])
	if !ok || version != model.MultiplayerRuntimeSnapshotVersion {
		return nil, ErrGameRuntimeUnavailable
	}
	statusText, ok := redisString(values[2])
	if !ok || (statusText != string(model.RoomStatusPlaying) && statusText != string(model.RoomStatusPaused)) {
		return nil, ErrGameRuntimeStatusConflict
	}
	generation, ok := redisString(values[3])
	if !ok || uuid.Validate(generation) != nil {
		return nil, ErrGameRuntimeUnavailable
	}
	turn, ok := redisInt64(values[4])
	if !ok || turn < 0 {
		return nil, ErrGameRuntimeUnavailable
	}
	orderStrings, ok := redisStringSlice(values[5])
	if !ok || len(orderStrings) < 2 {
		return nil, ErrGameRuntimeUnavailable
	}
	order := make([]uint, len(orderStrings))
	seen := make(map[uint]struct{}, len(order))
	for index, raw := range orderStrings {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			return nil, ErrGameRuntimeUnavailable
		}
		order[index] = uint(parsed)
		if _, exists := seen[order[index]]; exists {
			return nil, ErrGameRuntimeUnavailable
		}
		seen[order[index]] = struct{}{}
	}
	deadlineText, ok := redisString(values[6])
	if !ok {
		return nil, ErrGameRuntimeUnavailable
	}
	var deadline *time.Time
	if deadlineText != "" {
		millis, err := strconv.ParseInt(deadlineText, 10, 64)
		if err != nil {
			return nil, ErrGameRuntimeUnavailable
		}
		parsed := time.UnixMilli(millis).UTC()
		deadline = &parsed
	}
	lease, err := decodeMultiplayerActionLease(values[10], generation, int(turn), order[int(turn)%len(order)])
	if err != nil {
		return nil, err
	}
	if statusText == string(model.RoomStatusPlaying) && deadline == nil && lease == nil {
		return nil, ErrGameRuntimeUnavailable
	}
	if deadline != nil && lease != nil {
		return nil, ErrGameRuntimeUnavailable
	}
	summary, ok := redisString(values[7])
	if !ok {
		return nil, ErrGameRuntimeUnavailable
	}
	rounds, ok := redisStringSlice(values[8])
	if !ok {
		return nil, ErrGameRuntimeUnavailable
	}
	messages := make([]model.RuntimeMessage, 0, len(rounds))
	for index := len(rounds) - 1; index >= 0; index-- {
		var message model.RuntimeMessage
		if json.Unmarshal([]byte(rounds[index]), &message) != nil || message.Role == "" || message.Content == "" {
			return nil, ErrGameRuntimeUnavailable
		}
		messages = append(messages, message)
	}
	players, err := decodeMultiplayerPlayers(values[9])
	if err != nil || len(players) != len(order) {
		return nil, ErrGameRuntimeUnavailable
	}
	for _, player := range players {
		if _, exists := seen[player.UserID]; !exists {
			return nil, ErrGameRuntimeUnavailable
		}
	}
	currentTurn := int(turn)
	return &model.MultiplayerRuntimeSnapshot{
		Version: model.MultiplayerRuntimeSnapshotVersion, RoomID: roomID,
		Status: model.RoomStatus(statusText), Generation: generation,
		CurrentTurn: currentTurn, RoundNumber: currentTurn / len(order), TurnOrder: order,
		CurrentActorID: order[currentTurn%len(order)], DeadlineAt: deadline,
		Players: players, SummaryMemory: summary, RecentMessages: messages, ActionLease: lease,
	}, nil
}

func decodeMultiplayerActionLease(raw any, generation string, turn int, actor uint) (*model.MultiplayerActionLease, error) {
	fields, ok := redisStringSlice(raw)
	if !ok || len(fields)%2 != 0 {
		return nil, ErrGameRuntimeUnavailable
	}
	if len(fields) == 0 {
		return nil, nil
	}
	values := make(map[string]string, len(fields)/2)
	for index := 0; index < len(fields); index += 2 {
		values[fields[index]] = fields[index+1]
	}
	leaseTurn, turnErr := strconv.Atoi(values["turn"])
	userID, userErr := strconv.ParseUint(values["user_id"], 10, 64)
	claimedAt, claimedErr := strconv.ParseInt(values["claimed_at"], 10, 64)
	if len(values) != 6 || values["generation"] != generation || leaseTurn != turn ||
		userID != uint64(actor) || uuid.Validate(values["request_id"]) != nil ||
		len(values["fingerprint"]) != 64 || turnErr != nil || userErr != nil || claimedErr != nil || claimedAt <= 0 {
		return nil, ErrGameRuntimeUnavailable
	}
	return &model.MultiplayerActionLease{
		Generation: generation, Turn: turn, UserID: actor, RequestID: values["request_id"],
		Fingerprint: values["fingerprint"], ClaimedAt: time.UnixMilli(claimedAt).UTC(),
	}, nil
}

func decodeMultiplayerPlayers(value any) ([]model.MultiplayerRuntimePlayer, error) {
	entries, ok := value.([]any)
	if !ok {
		return nil, ErrGameRuntimeUnavailable
	}
	players := make([]model.MultiplayerRuntimePlayer, 0, len(entries))
	seenUsers := make(map[uint64]struct{}, len(entries))
	for _, rawEntry := range entries {
		entry, ok := rawEntry.([]any)
		if !ok || len(entry) != 4 {
			return nil, ErrGameRuntimeUnavailable
		}
		userText, ok := redisString(entry[0])
		if !ok {
			return nil, ErrGameRuntimeUnavailable
		}
		userID, err := strconv.ParseUint(userText, 10, 64)
		stateValues, stateOK := redisStringSlice(entry[1])
		itemValues, itemsOK := redisStringSlice(entry[2])
		buffValues, buffsOK := redisStringSlice(entry[3])
		if err != nil || userID == 0 || !stateOK || len(stateValues)%2 != 0 || !itemsOK || !buffsOK || len(buffValues)%2 != 0 {
			return nil, ErrGameRuntimeUnavailable
		}
		if _, duplicate := seenUsers[userID]; duplicate {
			return nil, ErrGameRuntimeUnavailable
		}
		seenUsers[userID] = struct{}{}
		state := make(map[string]string, len(stateValues)/2)
		for index := 0; index < len(stateValues); index += 2 {
			state[stateValues[index]] = stateValues[index+1]
		}
		characterID, err := strconv.ParseUint(state["character_id"], 10, 64)
		if err != nil || characterID == 0 {
			return nil, ErrGameRuntimeUnavailable
		}
		delete(state, "character_id")
		items := make([]model.RuntimeItem, 0, len(itemValues))
		for _, encoded := range itemValues {
			var item model.RuntimeItem
			if json.Unmarshal([]byte(encoded), &item) != nil || item.Name == "" || item.Quantity <= 0 {
				return nil, ErrGameRuntimeUnavailable
			}
			items = append(items, item)
		}
		buffs := make([]model.RuntimeBuff, 0, len(buffValues)/2)
		for index := 0; index < len(buffValues); index += 2 {
			duration, err := strconv.Atoi(buffValues[index+1])
			if err != nil || strings.TrimSpace(buffValues[index]) == "" || duration <= 0 {
				return nil, ErrGameRuntimeUnavailable
			}
			buffs = append(buffs, model.RuntimeBuff{Name: buffValues[index], Duration: duration})
		}
		players = append(players, model.MultiplayerRuntimePlayer{
			UserID: uint(userID), CharacterID: uint(characterID), PlayerState: state, Items: items, Buffs: buffs,
		})
	}
	return players, nil
}

func multiplayerCommonRuntimeKeys(roomID uint) []string {
	prefix := fmt.Sprintf("room:%d:", roomID)
	return []string{
		prefix + "runtime_version", prefix + "status", prefix + "generation", prefix + "turn",
		prefix + "turn_order", prefix + "deadline_at", prefix + "summary", prefix + "rounds", prefix + "runtime_players",
	}
}

func multiplayerRuntimeKeys(roomID uint, players []model.MultiplayerRuntimePlayer) []string {
	keys := append([]string(nil), multiplayerCommonRuntimeKeys(roomID)...)
	for _, player := range players {
		keys = append(keys, runtimePlayerKey(roomID, player.UserID), itemStateKey(roomID, player.UserID), buffStateKey(roomID, player.UserID))
	}
	return keys
}

func multiplayerRuntimeVersionKey(roomID uint) string {
	return fmt.Sprintf("room:%d:runtime_version", roomID)
}
func multiplayerDeadlineKey(roomID uint) string   { return fmt.Sprintf("room:%d:deadline_at", roomID) }
func multiplayerStartLeaseKey(roomID uint) string { return fmt.Sprintf("room:%d:start_lease", roomID) }
func multiplayerDeadlineQueueKey() string         { return "game:turn_deadlines" }
func multiplayerDeadlineMember(roomID uint, generation string, turn int) string {
	return fmt.Sprintf("%d:%s:%d", roomID, generation, turn)
}
