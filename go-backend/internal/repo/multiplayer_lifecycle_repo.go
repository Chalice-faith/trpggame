package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/model"
)

var transitionMultiplayerRuntimeScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= '2' then return 0 end
if redis.call('GET', KEYS[2]) ~= ARGV[1] or redis.call('GET', KEYS[3]) ~= ARGV[2] then return 0 end
local turn = tonumber(redis.call('GET', KEYS[4]))
local count = redis.call('LLEN', KEYS[6])
if not turn or turn < 0 or turn % 1 ~= 0 or count < 2 then return -1 end
local lease_type = redis.call('TYPE', KEYS[7]).ok
if lease_type ~= 'none' and lease_type ~= 'hash' then return -1 end
local old_member = ARGV[6] .. ':' .. ARGV[2] .. ':' .. tostring(turn)
if ARGV[3] == 'playing' then
  if not tonumber(ARGV[4]) or tonumber(ARGV[4]) <= 0 or redis.call('HLEN', KEYS[7]) > 0 then return -1 end
  redis.call('SET', KEYS[5], ARGV[4])
  redis.call('ZREM', KEYS[8], old_member)
  redis.call('ZADD', KEYS[8], ARGV[4], ARGV[6] .. ':' .. ARGV[5] .. ':' .. tostring(turn))
else
  redis.call('SET', KEYS[5], '')
  redis.call('ZREM', KEYS[8], old_member)
  redis.call('DEL', KEYS[7])
end
redis.call('SET', KEYS[2], ARGV[3])
redis.call('SET', KEYS[3], ARGV[5])
for index = 1, 7 do
  if redis.call('EXISTS', KEYS[index]) == 1 then redis.call('EXPIRE', KEYS[index], ARGV[7]) end
end
return turn + 1
`)

var restoreMultiplayerRuntimeScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= '2' or redis.call('GET', KEYS[2]) ~= 'paused' or
   redis.call('GET', KEYS[3]) ~= ARGV[2] then return 0 end
local current = tonumber(redis.call('GET', KEYS[4]))
local old_generation = redis.call('GET', KEYS[3])
if not current then return -1 end
local player_count = tonumber(ARGV[6])
if not player_count or player_count < 2 or player_count * 3 + 13 ~= #KEYS then return -1 end
local turn_count = tonumber(ARGV[7])
if turn_count ~= player_count then return -1 end
local message_count = tonumber(ARGV[8 + turn_count])
if not message_count or message_count < 1 or message_count > 10 then return -1 end
local room_id = ARGV[#ARGV]
if tonumber(room_id) == nil or tonumber(room_id) <= 0 then return -1 end
local old_member = room_id .. ':' .. old_generation .. ':' .. tostring(current)
redis.call('ZREM', KEYS[#KEYS - 1], old_member)
redis.call('DEL', unpack(KEYS, 1, #KEYS - 2))
redis.call('SET', KEYS[1], '2')
redis.call('SET', KEYS[2], 'paused')
redis.call('SET', KEYS[3], ARGV[3])
redis.call('SET', KEYS[4], ARGV[4])
redis.call('SET', KEYS[6], '')
redis.call('SET', KEYS[7], ARGV[5])
local index = 7
index = index + 1
for player_index = 1, turn_count do
  local user_id = ARGV[index]
  index = index + 1
  redis.call('RPUSH', KEYS[5], user_id)
  redis.call('RPUSH', KEYS[9], user_id)
end
index = index + 1
for _ = 1, message_count do
  redis.call('LPUSH', KEYS[8], ARGV[index])
  index = index + 1
end
for player_index = 1, player_count do
  local key_index = 9 + ((player_index - 1) * 3) + 1
  local user_id = ARGV[index]
  local character_id = ARGV[index + 1]
  local state_count = tonumber(ARGV[index + 2])
  index = index + 3
  redis.call('HSET', KEYS[key_index], 'character_id', character_id)
  for _ = 1, state_count do
    redis.call('HSET', KEYS[key_index], ARGV[index], ARGV[index + 1])
    index = index + 2
  end
  local item_count = tonumber(ARGV[index])
  index = index + 1
  for _ = 1, item_count do
    redis.call('SADD', KEYS[key_index + 1], ARGV[index])
    index = index + 1
  end
  local buff_count = tonumber(ARGV[index])
  index = index + 1
  for _ = 1, buff_count do
    redis.call('HSET', KEYS[key_index + 2], ARGV[index], ARGV[index + 1])
    index = index + 2
  end
end
for key_index = 1, #KEYS - 2 do
  if redis.call('EXISTS', KEYS[key_index]) == 1 then redis.call('EXPIRE', KEYS[key_index], ARGV[1]) end
end
redis.call('ZREM', KEYS[#KEYS], room_id)
return 1
`)

// TransitionMultiplayerRoom fences in-flight requests whenever status changes.
func (r *RedisGameStateRepo) TransitionMultiplayerRoom(
	ctx context.Context,
	roomID uint,
	expectedGeneration, nextGeneration string,
	from, to model.RoomStatus,
	deadline time.Time,
) (int, error) {
	if roomID == 0 || uuid.Validate(expectedGeneration) != nil || uuid.Validate(nextGeneration) != nil ||
		(from != model.RoomStatusPlaying && from != model.RoomStatusPaused) ||
		(to != model.RoomStatusPlaying && to != model.RoomStatusPaused && to != model.RoomStatusEnded) {
		return 0, ErrInvalidGameRuntimeState
	}
	if to == model.RoomStatusPlaying && deadline.IsZero() {
		return 0, ErrInvalidGameRuntimeState
	}
	deadlineMillis := int64(0)
	if !deadline.IsZero() {
		deadlineMillis = deadline.UTC().UnixMilli()
	}
	code, err := transitionMultiplayerRuntimeScript.Run(ctx, r.client, []string{
		multiplayerRuntimeVersionKey(roomID), runtimeStatusKey(roomID), runtimeGenerationKey(roomID), runtimeTurnKey(roomID),
		multiplayerDeadlineKey(roomID), multiplayerTurnOrderKey(roomID), multiplayerActionLeaseKey(roomID), multiplayerDeadlineQueueKey(),
	}, string(from), expectedGeneration, string(to), deadlineMillis, nextGeneration, roomID,
		int64(r.ttl/time.Second)).Int64()
	if err != nil {
		return 0, fmt.Errorf("%w: transition multiplayer runtime: %v", ErrGameRuntimeUnavailable, err)
	}
	if code < 0 {
		return 0, ErrGameRuntimeUnavailable
	}
	if code == 0 {
		return 0, ErrMultiplayerRuntimeConflict
	}
	return int(code) - 1, nil
}

// RestoreMultiplayerRoom replaces the paused runtime from a strictly validated V2 snapshot.
func (r *RedisGameStateRepo) RestoreMultiplayerRoom(
	ctx context.Context,
	expectedGeneration string,
	snapshot *model.MultiplayerRuntimeSnapshot,
) error {
	if uuid.Validate(expectedGeneration) != nil {
		return ErrInvalidGameRuntimeState
	}
	if err := validatePausedMultiplayerSnapshot(snapshot); err != nil {
		return err
	}
	playersByID := make(map[uint]model.MultiplayerRuntimePlayer, len(snapshot.Players))
	for _, player := range snapshot.Players {
		playersByID[player.UserID] = player
	}
	orderedPlayers := make([]model.MultiplayerRuntimePlayer, 0, len(snapshot.TurnOrder))
	for _, userID := range snapshot.TurnOrder {
		orderedPlayers = append(orderedPlayers, playersByID[userID])
	}
	keys := append(multiplayerRuntimeKeys(snapshot.RoomID, orderedPlayers), actionResultsKey(snapshot.RoomID),
		pendingMultiplayerAutoSavesKey(snapshot.RoomID), multiplayerDeadlineQueueKey(), pendingMultiplayerAutoSaveRoomsKey())
	arguments := []any{int64(r.ttl / time.Second), expectedGeneration, snapshot.Generation, snapshot.CurrentTurn,
		snapshot.SummaryMemory, len(snapshot.Players), len(snapshot.TurnOrder)}
	for _, userID := range snapshot.TurnOrder {
		arguments = append(arguments, userID)
	}
	arguments = append(arguments, len(snapshot.RecentMessages))
	for _, message := range snapshot.RecentMessages {
		encoded, _ := json.Marshal(message)
		arguments = append(arguments, string(encoded))
	}
	for _, userID := range snapshot.TurnOrder {
		player := playersByID[userID]
		fields := make([]string, 0, len(player.PlayerState))
		for field := range player.PlayerState {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		arguments = append(arguments, player.UserID, player.CharacterID, len(fields))
		for _, field := range fields {
			arguments = append(arguments, field, player.PlayerState[field])
		}
		arguments = append(arguments, len(player.Items))
		for _, item := range player.Items {
			encoded, _ := json.Marshal(item)
			arguments = append(arguments, string(encoded))
		}
		arguments = append(arguments, len(player.Buffs))
		for _, buff := range player.Buffs {
			arguments = append(arguments, buff.Name, buff.Duration)
		}
	}
	arguments = append(arguments, snapshot.RoomID)
	code, err := restoreMultiplayerRuntimeScript.Run(ctx, r.client, keys, arguments...).Int64()
	if err != nil {
		return fmt.Errorf("%w: restore multiplayer runtime: %v", ErrGameRuntimeUnavailable, err)
	}
	if code < 0 {
		return ErrInvalidGameRuntimeState
	}
	if code != 1 {
		return ErrMultiplayerRuntimeConflict
	}
	return nil
}

func validatePausedMultiplayerSnapshot(snapshot *model.MultiplayerRuntimeSnapshot) error {
	return validateMultiplayerSnapshot(snapshot, model.RoomStatusPaused)
}

func validateMultiplayerSnapshot(snapshot *model.MultiplayerRuntimeSnapshot, expectedStatus model.RoomStatus) error {
	deadlineValid := snapshot != nil && ((expectedStatus == model.RoomStatusPaused && snapshot.DeadlineAt == nil) ||
		(expectedStatus == model.RoomStatusPlaying && snapshot.DeadlineAt != nil && !snapshot.DeadlineAt.IsZero()))
	if snapshot == nil || snapshot.Version != model.MultiplayerRuntimeSnapshotVersion || snapshot.RoomID == 0 ||
		uuid.Validate(snapshot.Generation) != nil || snapshot.Status != expectedStatus || !deadlineValid ||
		snapshot.ActionLease != nil || snapshot.CurrentTurn < 0 || len(snapshot.TurnOrder) < 2 ||
		snapshot.RoundNumber != snapshot.CurrentTurn/len(snapshot.TurnOrder) || len(snapshot.Players) != len(snapshot.TurnOrder) ||
		len(snapshot.RecentMessages) == 0 || len(snapshot.RecentMessages) > 10 || len(snapshot.SummaryMemory) > 65535 {
		return ErrInvalidGameRuntimeState
	}
	seen := make(map[uint]struct{}, len(snapshot.TurnOrder))
	players := make(map[uint]model.MultiplayerRuntimePlayer, len(snapshot.Players))
	for _, player := range snapshot.Players {
		if player.UserID == 0 || player.CharacterID == 0 || player.PlayerState == nil ||
			player.Items == nil || player.Buffs == nil {
			return ErrInvalidGameRuntimeState
		}
		if _, duplicate := players[player.UserID]; duplicate {
			return ErrInvalidGameRuntimeState
		}
		players[player.UserID] = player
	}
	for _, userID := range snapshot.TurnOrder {
		if userID == 0 {
			return ErrInvalidGameRuntimeState
		}
		if _, duplicate := seen[userID]; duplicate {
			return ErrInvalidGameRuntimeState
		}
		seen[userID] = struct{}{}
		if _, exists := players[userID]; !exists {
			return ErrInvalidGameRuntimeState
		}
	}
	if snapshot.CurrentActorID != snapshot.TurnOrder[snapshot.CurrentTurn%len(snapshot.TurnOrder)] {
		return ErrInvalidGameRuntimeState
	}
	for _, message := range snapshot.RecentMessages {
		if (message.Role != "user" && message.Role != "assistant" && message.Role != "system") || strings.TrimSpace(message.Content) == "" {
			return ErrInvalidGameRuntimeState
		}
	}
	for _, player := range snapshot.Players {
		for _, item := range player.Items {
			if strings.TrimSpace(item.Name) == "" || item.Quantity <= 0 {
				return ErrInvalidGameRuntimeState
			}
		}
		for _, buff := range player.Buffs {
			if strings.TrimSpace(buff.Name) == "" || buff.Duration <= 0 {
				return ErrInvalidGameRuntimeState
			}
		}
	}
	return nil
}

func pendingMultiplayerAutoSavesKey(roomID uint) string {
	return fmt.Sprintf("room:%d:pending_multiplayer_auto_saves", roomID)
}

func pendingMultiplayerAutoSaveRoomsKey() string { return "game:pending_multiplayer_auto_save_rooms" }

func multiplayerRuntimePlayersKey(roomID uint) string {
	return fmt.Sprintf("room:%d:runtime_players", roomID)
}

func multiplayerSummaryKey(roomID uint) string { return fmt.Sprintf("room:%d:summary", roomID) }
