package repo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/model"
)

var (
	ErrMultiplayerNotCurrentActor    = errors.New("multiplayer user is not current actor")
	ErrMultiplayerActionInProgress   = errors.New("multiplayer action already in progress")
	ErrMultiplayerDeadlineNotReached = errors.New("multiplayer deadline not reached")
)

// An interrupted AI request must eventually relinquish its turn even when the
// process that acquired it never gets to run its failure cleanup.
const MultiplayerActionRecoveryTimeout = 5 * time.Minute

var acquireMultiplayerActionScript = redis.NewScript(gameArchiveLua + `
local meta, gate_code = archive_gate(KEYS[10], KEYS[11], ARGV[10], 'multiplayer', true)
if gate_code ~= 0 then return {gate_code, '', ''} end
if not archive_type(KEYS[8], 'hash') then return {92, '', ''} end
local cached = redis.call('HGET', KEYS[8], ARGV[3])
if cached then
  if meta then
    local ok, value = pcall(cjson.decode, cached)
    if not ok or value.archive_timeline_id ~= meta.timeline then return {91, '', ''} end
  end
  local current_generation = redis.call('GET', KEYS[3]) or ''
  if current_generation ~= ARGV[6] then return {4, current_generation, ''} end
  return {2, current_generation, cached}
end
local _, code = archive_gate(KEYS[10], KEYS[11], ARGV[10], 'multiplayer', false)
if code ~= 0 then return {code, '', ''} end
for index, kind in ipairs({'string','string','string','string','list','string','hash','hash','zset'}) do
  if not archive_type(KEYS[index], kind) then return {92, '', ''} end
end
if redis.call('GET', KEYS[1]) ~= '2' then return {8, '', ''} end
if redis.call('GET', KEYS[2]) ~= 'playing' then return {3, '', ''} end
local generation = redis.call('GET', KEYS[3])
local current = tonumber(redis.call('GET', KEYS[4]))
local order_count = redis.call('LLEN', KEYS[5])
local deadline = tonumber(redis.call('GET', KEYS[6]))
if not generation or not current or current < 0 or current % 1 ~= 0 or order_count < 2 then
  return {8, '', ''}
end
if generation ~= ARGV[6] then return {4, generation, ''} end
if current ~= tonumber(ARGV[1]) then return {4, generation, tostring(current)} end
local actor = redis.call('LINDEX', KEYS[5], current % order_count)
if actor ~= ARGV[2] then return {5, generation, actor or ''} end
local lease_type = redis.call('TYPE', KEYS[7]).ok
if lease_type ~= 'none' and lease_type ~= 'hash' then return {8, '', ''} end
if redis.call('HLEN', KEYS[7]) > 0 then return {6, generation, ''} end
if not deadline then return {8, '', ''} end
if tonumber(ARGV[5]) > deadline then return {7, generation, tostring(deadline)} end
redis.call('HSET', KEYS[7],
  'generation', generation, 'turn', tostring(current), 'user_id', ARGV[2],
  'request_id', ARGV[3], 'fingerprint', ARGV[4], 'claimed_at', ARGV[5])
redis.call('SET', KEYS[6], '')
redis.call('ZADD', KEYS[9], tonumber(ARGV[5]) + tonumber(ARGV[9]), ARGV[7])
for index = 1, 8 do
  if redis.call('EXISTS', KEYS[index]) == 1 then redis.call('EXPIRE', KEYS[index], ARGV[8]) end
end
return {1, generation, ''}
`)

var releaseMultiplayerActionScript = redis.NewScript(gameArchiveLua + `
local meta, code = archive_meta(KEYS[8], KEYS[9])
if code ~= 0 then return code end
if meta and (meta.state ~= 'ready' or meta.head ~= meta.durable or meta.control ~= '' or redis.call('EXISTS', KEYS[9]) ~= 0) then return 90 end
if redis.call('GET', KEYS[1]) ~= '2' or redis.call('GET', KEYS[2]) ~= 'playing' or
   redis.call('GET', KEYS[3]) ~= ARGV[1] or redis.call('GET', KEYS[4]) ~= ARGV[2] then return 0 end
if redis.call('HGET', KEYS[5], 'generation') ~= ARGV[1] or
   redis.call('HGET', KEYS[5], 'turn') ~= ARGV[2] or
   redis.call('HGET', KEYS[5], 'user_id') ~= ARGV[3] or
   redis.call('HGET', KEYS[5], 'request_id') ~= ARGV[4] or
   redis.call('HGET', KEYS[5], 'fingerprint') ~= ARGV[5] then return 0 end
redis.call('DEL', KEYS[5])
redis.call('SET', KEYS[6], ARGV[6])
redis.call('ZADD', KEYS[7], ARGV[6], ARGV[7])
for index = 1, 6 do
  if redis.call('EXISTS', KEYS[index]) == 1 then redis.call('EXPIRE', KEYS[index], ARGV[8]) end
end
return 1
`)

const multiplayerAutoSaveSnapshotLua = `
local function saveMultiplayerRoundSnapshot(room_id, generation, response, order_key, players_key, rounds_key, summary_key, pending_key, pending_rooms_key)
  local current_turn = tonumber(response.current_turn)
  local round_number = tonumber(response.round_number)
  local order_ids = redis.call('LRANGE', order_key, 0, -1)
  if not current_turn or not round_number or round_number <= 0 or round_number % 5 ~= 0 or
     #order_ids < 2 or current_turn % #order_ids ~= 0 or current_turn / #order_ids ~= round_number then
    return false
  end
  local players = {}
  for _, user_text in ipairs(redis.call('LRANGE', players_key, 0, -1)) do
    local player_key = 'room:' .. tostring(room_id) .. ':player:' .. user_text
    local raw_state = redis.call('HGETALL', player_key)
    local state = {}
    local character_id = nil
    for index = 1, #raw_state, 2 do
      if raw_state[index] == 'character_id' then
        character_id = tonumber(raw_state[index + 1])
      else
        state[raw_state[index]] = raw_state[index + 1]
      end
    end
    local items = {}
    for _, encoded in ipairs(redis.call('SMEMBERS', player_key .. ':items')) do
      local ok, item = pcall(cjson.decode, encoded)
      if not ok or type(item) ~= 'table' then return false end
      table.insert(items, item)
    end
    if #items == 0 then items = {cjson.null} end
    local buffs = {}
    local raw_buffs = redis.call('HGETALL', player_key .. ':buffs')
    for index = 1, #raw_buffs, 2 do
      table.insert(buffs, {name = raw_buffs[index], duration = tonumber(raw_buffs[index + 1])})
    end
    if #buffs == 0 then buffs = {cjson.null} end
    table.insert(players, {user_id = tonumber(user_text), character_id = character_id, player_state = state, items = items, buffs = buffs})
  end
  if #players ~= #order_ids then return false end
  local turn_order = {}
  for _, user_text in ipairs(order_ids) do table.insert(turn_order, tonumber(user_text)) end
  local rounds = redis.call('LRANGE', rounds_key, 0, -1)
  local messages = {}
  for index = #rounds, 1, -1 do
    local ok, message = pcall(cjson.decode, rounds[index])
    if not ok or type(message) ~= 'table' then return false end
    table.insert(messages, message)
  end
  local snapshot = {
    version = 2, room_id = tonumber(room_id), status = 'playing', generation = generation,
    current_turn = current_turn, round_number = round_number, turn_order = turn_order,
    current_actor_id = tonumber(response.current_actor_id), deadline_at = response.deadline_at,
    players = players, summary_memory = redis.call('GET', summary_key) or '', recent_messages = messages,
  }
  local encoded = cjson.encode(snapshot)
  encoded = string.gsub(encoded, '"items":%[null%]', '"items":[]')
  encoded = string.gsub(encoded, '"buffs":%[null%]', '"buffs":[]')
  redis.call('HSET', pending_key, tostring(round_number), encoded)
  redis.call('ZADD', pending_rooms_key, 0, tostring(room_id))
  return true
end
`

var commitMultiplayerActionScript = redis.NewScript(gameArchiveLua + multiplayerAutoSaveSnapshotLua + `
local archive_keys = {unpack(KEYS, #KEYS - 5, #KEYS)}
if not archive_type(KEYS[9], 'hash') then return {7, -1, ''} end
local cached = redis.call('HGET', KEYS[9], ARGV[4])
if cached then
  local code = archive_replay(archive_keys, ARGV[#ARGV], 'multiplayer', cached)
  if code ~= 0 then return {code, -1, ''} end
  if redis.call('GET', KEYS[3]) ~= ARGV[1] then return {4, -1, ''} end
  return {2, tonumber(redis.call('GET', KEYS[4])) or -1, cached}
end
if redis.call('GET', KEYS[1]) ~= '2' then return {7, -1, ''} end
if redis.call('GET', KEYS[2]) ~= 'playing' then return {3, -1, ''} end
if redis.call('GET', KEYS[3]) ~= ARGV[1] then return {4, -1, ''} end
local current = tonumber(redis.call('GET', KEYS[4]))
local order_count = redis.call('LLEN', KEYS[5])
if not current or current < 0 or current % 1 ~= 0 or order_count < 2 then return {7, -1, ''} end
if current ~= tonumber(ARGV[2]) then return {5, current, ''} end
if redis.call('LINDEX', KEYS[5], current % order_count) ~= ARGV[3] then return {6, current, ''} end
if redis.call('HGET', KEYS[8], 'generation') ~= ARGV[1] or
   redis.call('HGET', KEYS[8], 'turn') ~= ARGV[2] or
   redis.call('HGET', KEYS[8], 'user_id') ~= ARGV[3] or
   redis.call('HGET', KEYS[8], 'request_id') ~= ARGV[4] or
   redis.call('HGET', KEYS[8], 'fingerprint') ~= ARGV[5] then return {6, current, ''} end

local mutation_count = tonumber(ARGV[13])
if not mutation_count or mutation_count < 0 or #KEYS ~= 20 + mutation_count * 3 then return {7, current, ''} end
local archive_plan, archive_code = archive_prepare(archive_keys, ARGV[#ARGV], 'multiplayer', ARGV[1],
  ARGV[14 + mutation_count], ARGV[3], current, math.floor(current/order_count), math.floor((current+1)/order_count), ARGV[4], ARGV[5], 'action')
if archive_code ~= 0 then return {archive_code, current, ''} end
for index, kind in ipairs({'string','string','string','string','list','string','list','hash','hash','zset','list','string','hash','zset'}) do
  if not archive_type(KEYS[index], kind) then return {7, current, ''} end
end
local decoded_cache = cjson.decode(ARGV[6])
local plans = {}
for index = 1, mutation_count do
  local ok, mutation = pcall(cjson.decode, ARGV[13 + index])
  if not ok or type(mutation) ~= 'table' then return {7, current, ''} end
  local player_key = KEYS[14 + (index - 1) * 3 + 1]
  local items_key = KEYS[14 + (index - 1) * 3 + 2]
  local buffs_key = KEYS[14 + (index - 1) * 3 + 3]
  local member = false
  for order_index = 0, order_count - 1 do
    if redis.call('LINDEX', KEYS[5], order_index) == tostring(mutation.user_id) then member = true break end
  end
  if not member then return {7, current, ''} end
  if redis.call('TYPE', player_key).ok ~= 'hash' then return {7, current, ''} end
  local items_type = redis.call('TYPE', items_key).ok
  local buffs_type = redis.call('TYPE', buffs_key).ok
  if (items_type ~= 'none' and items_type ~= 'set') or (buffs_type ~= 'none' and buffs_type ~= 'hash') then
    return {7, current, ''}
  end
  local inventory = {}
  for _, encoded in ipairs(redis.call('SMEMBERS', items_key)) do
    local item_ok, item = pcall(cjson.decode, encoded)
    if not item_ok or type(item) ~= 'table' or type(item.name) ~= 'string' or item.name == '' or
       type(item.quantity) ~= 'number' or item.quantity <= 0 or item.quantity % 1 ~= 0 or inventory[item.name] then
      return {7, current, ''}
    end
    inventory[item.name] = {quantity = item.quantity, description = item.description or ''}
  end
  for _, item in ipairs(mutation.items or {}) do
    local existing = inventory[item.name]
    local quantity = (existing and existing.quantity or 0) + item.quantity_delta
    if quantity < 0 then return {8, current, ''} end
    if quantity == 0 then
      inventory[item.name] = nil
    else
      local description = item.description or ''
      if description == '' and existing then description = existing.description end
      inventory[item.name] = {quantity = quantity, description = description}
    end
  end
  local existing_buffs = redis.call('HGETALL', buffs_key)
  for buff_index = 1, #existing_buffs, 2 do
    local duration = tonumber(existing_buffs[buff_index + 1])
    if existing_buffs[buff_index] == '' or not duration or duration <= 0 or duration % 1 ~= 0 then
      return {7, current, ''}
    end
  end
  plans[index] = {mutation = mutation, player = player_key, items = items_key, buffs = buffs_key, inventory = inventory}
end

local boundary = ''
if archive_plan then
  local code
  boundary, code = archive_multiplayer_boundary(ARGV[14 + mutation_count], ARGV[1], current+1, KEYS[5], KEYS[11], KEYS[7], KEYS[12], plans, ARGV[7], ARGV[8], archive_plan)
  if code ~= 0 then return {code, current, ''} end
end
for _, plan in ipairs(plans) do
  for field, value in pairs(plan.mutation.player_state_changes or {}) do
    redis.call('HSET', plan.player, field, value)
  end
  if #(plan.mutation.items or {}) > 0 then
    redis.call('DEL', plan.items)
    for name, item in pairs(plan.inventory) do
      redis.call('SADD', plan.items, cjson.encode({name = name, quantity = item.quantity, description = item.description}))
    end
  end
  for _, buff in ipairs(plan.mutation.buffs or {}) do
    redis.call('HSET', plan.buffs, buff.name, tostring(buff.duration))
  end
end
redis.call('LPUSH', KEYS[7], ARGV[7], ARGV[8])
redis.call('LTRIM', KEYS[7], 0, 9)
local next_turn = current + 1
redis.call('SET', KEYS[4], tostring(next_turn))
redis.call('HSET', KEYS[9], ARGV[4], ARGV[6])
redis.call('DEL', KEYS[8])
redis.call('SET', KEYS[6], archive_plan and '' or ARGV[9])
redis.call('ZREM', KEYS[10], ARGV[10])
if not archive_plan then redis.call('ZADD', KEYS[10], ARGV[9], ARGV[11]) end
local response = decoded_cache.response
if not archive_plan and response and tonumber(response.current_turn) and tonumber(response.round_number) and
   tonumber(response.round_number) % 5 == 0 and tonumber(response.current_turn) % order_count == 0 then
  saveMultiplayerRoundSnapshot(ARGV[14 + mutation_count], ARGV[1], response, KEYS[5], KEYS[11], KEYS[7], KEYS[12], KEYS[13], KEYS[14])
end
for index = 1, 9 do
  if redis.call('EXISTS', KEYS[index]) == 1 then redis.call('EXPIRE', KEYS[index], ARGV[12]) end
end
for index = 11, 12 do
  if redis.call('EXISTS', KEYS[index]) == 1 then redis.call('EXPIRE', KEYS[index], ARGV[12]) end
end
archive_commit(archive_plan, ARGV[6], boundary)
for index = 15, #KEYS - 6 do
  if redis.call('EXISTS', KEYS[index]) == 1 then redis.call('EXPIRE', KEYS[index], ARGV[12]) end
end
return {1, next_turn, ''}
`)

var skipMultiplayerTurnScript = redis.NewScript(gameArchiveLua + multiplayerAutoSaveSnapshotLua + `
local archive_keys = {unpack(KEYS, 15, 20)}
if not archive_type(KEYS[8], 'hash') then return {92, ''} end
local cached = redis.call('HGET', KEYS[8], ARGV[4])
if cached then
  local code = archive_replay(archive_keys, ARGV[14], 'multiplayer', cached)
  if code ~= 0 then return {code, ''} end
  if redis.call('GET', KEYS[3]) ~= ARGV[1] then return {4, ''} end
  return {2, cached}
end
local timeline = ''
if ARGV[14] ~= '' then
  local ok, envelope = pcall(cjson.decode, ARGV[14]); if not ok then return {92, ''} end
  local valid, record = pcall(cjson.decode, envelope.record_json); if not valid then return {92, ''} end
  timeline = record.TimelineID
end
local _, gate_code = archive_gate(archive_keys[1], archive_keys[2], timeline, 'multiplayer', false)
if gate_code ~= 0 then return {gate_code, ''} end
if redis.call('GET', KEYS[1]) ~= '2' or redis.call('GET', KEYS[2]) ~= 'playing' then
  redis.call('ZREM', KEYS[9], ARGV[9])
  return {3, ''}
end
if redis.call('GET', KEYS[3]) ~= ARGV[1] then
  redis.call('ZREM', KEYS[9], ARGV[9])
  return {4, ''}
end
local current = tonumber(redis.call('GET', KEYS[4]))
local count = redis.call('LLEN', KEYS[5])
local deadline = tonumber(redis.call('GET', KEYS[6]))
if redis.call('HLEN', KEYS[7]) > 0 then return {8, ''} end
if not current or current ~= tonumber(ARGV[2]) or count < 2 or not deadline then
  redis.call('ZREM', KEYS[9], ARGV[9])
  return {5, ''}
end
local actor = redis.call('LINDEX', KEYS[5], current % count)
if ARGV[8] == 'timeout' then
  if tonumber(ARGV[7]) < deadline then return {7, ''} end
elseif actor ~= ARGV[3] then
  return {6, ''}
end
local next_turn = current + 1
local decoded_cache = cjson.decode(ARGV[6])
local archive_plan, archive_code = archive_prepare(archive_keys, ARGV[14], 'multiplayer', ARGV[1], ARGV[13], actor,
  current, math.floor(current/count), math.floor(next_turn/count), ARGV[4], decoded_cache.fingerprint, 'skip_' .. ARGV[8])
if archive_code ~= 0 then return {archive_code, ''} end
for index, kind in ipairs({'string','string','string','string','list','string','hash','hash','zset','list','string','hash','zset','list'}) do
  if not archive_type(KEYS[index], kind) then return {92, ''} end
end
local response = cjson.decode(ARGV[5])
local boundary = ''
if archive_plan then
  local code
  boundary, code = archive_multiplayer_boundary(ARGV[13], ARGV[1], next_turn, KEYS[5], KEYS[10], KEYS[14], KEYS[11], {}, nil, nil, archive_plan)
  if code ~= 0 then return {code, ''} end
end
redis.call('SET', KEYS[4], tostring(next_turn))
redis.call('SET', KEYS[6], archive_plan and '' or ARGV[10])
redis.call('HSET', KEYS[8], ARGV[4], ARGV[6])
redis.call('ZREM', KEYS[9], ARGV[9])
if not archive_plan then redis.call('ZADD', KEYS[9], ARGV[10], ARGV[11]) end
if not archive_plan and tonumber(response.current_turn) and tonumber(response.round_number) and
   tonumber(response.round_number) % 5 == 0 and tonumber(response.current_turn) % count == 0 then
  saveMultiplayerRoundSnapshot(ARGV[13], ARGV[1], response, KEYS[5], KEYS[10], KEYS[14], KEYS[11], KEYS[12], KEYS[13])
end
for index = 1, 8 do
  if redis.call('EXISTS', KEYS[index]) == 1 then redis.call('EXPIRE', KEYS[index], ARGV[12]) end
end
for index = 10, 11 do
  if redis.call('EXISTS', KEYS[index]) == 1 then redis.call('EXPIRE', KEYS[index], ARGV[12]) end
end
archive_commit(archive_plan, ARGV[6], boundary)
if redis.call('EXISTS', KEYS[14]) == 1 then redis.call('EXPIRE', KEYS[14], ARGV[12]) end
return {1, ARGV[5]}
`)

var listDueMultiplayerDeadlinesScript = redis.NewScript(`
return redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
`)

var discardMultiplayerDeadlineScript = redis.NewScript(`
if redis.call('GET', KEYS[2]) == '2' and redis.call('GET', KEYS[3]) == 'playing' and
   redis.call('GET', KEYS[4]) == ARGV[2] and redis.call('GET', KEYS[5]) == ARGV[3] then return 0 end
return redis.call('ZREM', KEYS[1], ARGV[1])`)

type encodedMultiplayerMutation struct {
	UserID             uint                        `json:"user_id"`
	PlayerStateChanges map[string]string           `json:"player_state_changes"`
	Items              []model.RuntimeItemMutation `json:"items"`
	Buffs              []model.RuntimeBuffMutation `json:"buffs"`
}

func (r *RedisGameStateRepo) AcquireMultiplayerAction(ctx context.Context, roomID, userID uint, generation string, expectedTurn int, requestID, fingerprint string, now time.Time) (*model.MultiplayerActionAcquireResult, error) {
	return r.acquireMultiplayerAction(ctx, roomID, userID, generation, expectedTurn, requestID, fingerprint, now, "")
}

func (r *RedisGameStateRepo) AcquireMemoryMultiplayerAction(ctx context.Context, roomID, userID uint, expectedTurn int, requestID, fingerprint string, now time.Time, expected model.GameArchiveExpectation) (*model.MultiplayerActionAcquireResult, error) {
	if !model.ValidMemoryUUID(expected.TimelineID) || !model.ValidMemoryUUID(expected.Generation) {
		return nil, model.ErrInvalidMemoryData
	}
	return r.acquireMultiplayerAction(ctx, roomID, userID, expected.Generation, expectedTurn, requestID, fingerprint, now, expected.TimelineID)
}

func (r *RedisGameStateRepo) acquireMultiplayerAction(ctx context.Context, roomID, userID uint, generation string, expectedTurn int, requestID, fingerprint string, now time.Time, timeline string) (*model.MultiplayerActionAcquireResult, error) {
	requestID, fingerprint, err := validateMultiplayerActionIdentity(roomID, userID, expectedTurn, requestID, fingerprint)
	if err != nil || uuid.Validate(generation) != nil || now.IsZero() {
		return nil, ErrInvalidGameRuntimeState
	}
	values, err := acquireMultiplayerActionScript.Run(ctx, r.client, []string{
		multiplayerRuntimeVersionKey(roomID), runtimeStatusKey(roomID), runtimeGenerationKey(roomID), runtimeTurnKey(roomID),
		multiplayerTurnOrderKey(roomID), multiplayerDeadlineKey(roomID), multiplayerActionLeaseKey(roomID), actionResultsKey(roomID), multiplayerDeadlineQueueKey(),
		gameArchiveKeys(roomID)[0], gameArchiveKeys(roomID)[1],
	}, expectedTurn, userID, requestID, fingerprint, now.UTC().UnixMilli(), generation,
		multiplayerDeadlineMember(roomID, generation, expectedTurn), int64(r.ttl/time.Second), MultiplayerActionRecoveryTimeout.Milliseconds(), timeline).Slice()
	if err != nil {
		return nil, fmt.Errorf("%w: acquire multiplayer action: %v", ErrGameRuntimeUnavailable, err)
	}
	if len(values) != 3 {
		return nil, ErrGameRuntimeUnavailable
	}
	code, ok := redisInt64(values[0])
	if issue := archiveError(code); issue != nil {
		return nil, issue
	}
	if !ok {
		return nil, ErrGameRuntimeUnavailable
	}
	actualGeneration, _ := redisString(values[1])
	switch code {
	case 1:
		return &model.MultiplayerActionAcquireResult{Generation: actualGeneration}, nil
	case 2:
		cached, decodeErr := decodeCachedActionResult(values[2])
		if decodeErr != nil {
			return nil, ErrGameRuntimeUnavailable
		}
		if cached.Fingerprint != fingerprint {
			return nil, ErrActionIdempotencyConflict
		}
		response := append(json.RawMessage(nil), cached.Response...)
		if timeline != "" {
			memory, _ := r.archiveDelivery(ctx, roomID, timeline)
			response = archiveResponse(response, memory, nil)
		}
		return &model.MultiplayerActionAcquireResult{Generation: actualGeneration, Duplicate: true, ResponseJSON: response}, nil
	case 3:
		return nil, ErrGameRuntimeNotPlaying
	case 4, 7:
		return nil, ErrGameRuntimeConflict
	case 5:
		return nil, ErrMultiplayerNotCurrentActor
	case 6:
		return nil, ErrMultiplayerActionInProgress
	default:
		return nil, ErrGameRuntimeUnavailable
	}
}

func (r *RedisGameStateRepo) ReleaseMultiplayerAction(ctx context.Context, mutation *model.MultiplayerActionMutation, deadline time.Time) error {
	if mutation == nil {
		return ErrInvalidGameRuntimeState
	}
	requestID, fingerprint, err := validateMultiplayerActionIdentity(mutation.RoomID, mutation.UserID, mutation.ExpectedTurn, mutation.RequestID, mutation.RequestFingerprint)
	if err != nil || uuid.Validate(mutation.Generation) != nil || deadline.IsZero() {
		return ErrInvalidGameRuntimeState
	}
	deadline = deadline.UTC()
	code, err := releaseMultiplayerActionScript.Run(ctx, r.client, []string{
		multiplayerRuntimeVersionKey(mutation.RoomID), runtimeStatusKey(mutation.RoomID), runtimeGenerationKey(mutation.RoomID), runtimeTurnKey(mutation.RoomID),
		multiplayerActionLeaseKey(mutation.RoomID), multiplayerDeadlineKey(mutation.RoomID), multiplayerDeadlineQueueKey(),
		gameArchiveKeys(mutation.RoomID)[0], gameArchiveKeys(mutation.RoomID)[1],
	}, mutation.Generation, mutation.ExpectedTurn, mutation.UserID, requestID, fingerprint, deadline.UnixMilli(),
		multiplayerDeadlineMember(mutation.RoomID, mutation.Generation, mutation.ExpectedTurn), int64(r.ttl/time.Second)).Int64()
	if err != nil {
		return fmt.Errorf("%w: release multiplayer action: %v", ErrGameRuntimeUnavailable, err)
	}
	if code != 1 {
		if issue := archiveError(code); issue != nil {
			return issue
		}
		return ErrGameRuntimeGenerationConflict
	}
	return nil
}

func (r *RedisGameStateRepo) CommitMultiplayerAction(ctx context.Context, mutation *model.MultiplayerActionMutation) (*model.MultiplayerActionCommitResult, error) {
	keys, arguments, fingerprint, err := r.multiplayerCommitArguments(mutation)
	if err != nil {
		return nil, err
	}
	values, err := commitMultiplayerActionScript.Run(ctx, r.client, keys, arguments...).Slice()
	if err != nil {
		if mutation.Archive != nil {
			if _, probeErr := r.reconcileArchiveCommit(ctx, archiveRecordEnvelope(arguments[len(arguments)-1])); probeErr != nil {
				return nil, fmt.Errorf("%w: commit multiplayer action: %v", probeErr, err)
			}
			values = []any{int64(1), int64(mutation.ExpectedTurn + 1), ""}
		} else {
			return nil, fmt.Errorf("%w: commit multiplayer action: %v", ErrGameRuntimeUnavailable, err)
		}
	}
	if len(values) != 3 {
		return nil, ErrGameRuntimeUnavailable
	}
	code, ok := redisInt64(values[0])
	if issue := archiveError(code); issue != nil {
		return nil, issue
	}
	turn, turnOK := redisInt64(values[1])
	if !ok || !turnOK {
		return nil, ErrGameRuntimeUnavailable
	}
	if code == 2 {
		cached, decodeErr := decodeCachedActionResult(values[2])
		if decodeErr != nil {
			return nil, ErrGameRuntimeUnavailable
		}
		if cached.Fingerprint != fingerprint {
			return nil, ErrActionIdempotencyConflict
		}
		result := &model.MultiplayerActionCommitResult{Duplicate: true, CurrentTurn: int(turn), ResponseJSON: append(json.RawMessage(nil), cached.Response...)}
		if mutation.Archive != nil {
			result.Memory, _ = r.archiveDelivery(ctx, mutation.RoomID, mutation.Archive.TimelineID)
			result.ResponseJSON = archiveResponse(result.ResponseJSON, result.Memory, nil)
		}
		return result, nil
	}
	switch code {
	case 1:
		// The write is already committed. A failed follow-up read must not turn a
		// successful CAS into a reported failure that triggers lease cleanup.
		snapshot, _ := r.GetMultiplayerRoom(ctx, mutation.RoomID)
		result := &model.MultiplayerActionCommitResult{CurrentTurn: int(turn), ResponseJSON: append(json.RawMessage(nil), mutation.ResponseJSON...), Snapshot: snapshot}
		if mutation.Archive != nil {
			result.Memory, _ = r.archiveDelivery(ctx, mutation.RoomID, mutation.Archive.TimelineID)
			result.ResponseJSON = archiveResponse(result.ResponseJSON, result.Memory, nil)
		}
		return result, nil
	case 3:
		return nil, ErrGameRuntimeNotPlaying
	case 4:
		return nil, ErrGameRuntimeGenerationConflict
	case 5:
		return nil, ErrGameRuntimeConflict
	case 6:
		return nil, ErrMultiplayerNotCurrentActor
	case 8:
		return nil, ErrInsufficientItemQuantity
	default:
		return nil, ErrGameRuntimeUnavailable
	}
}

func (r *RedisGameStateRepo) SkipMultiplayerTurn(ctx context.Context, request *model.MultiplayerSkipRequest) (*model.MultiplayerSkipResult, error) {
	if request == nil || uuid.Validate(request.Generation) != nil || request.Now.IsZero() || request.NextDeadline.IsZero() || !json.Valid(request.ResponseJSON) {
		return nil, ErrInvalidGameRuntimeState
	}
	validationUserID := request.UserID
	if request.Timeout {
		validationUserID = 1
	}
	requestID, fingerprint, err := validateMultiplayerActionIdentity(request.RoomID, validationUserID, request.ExpectedTurn, request.RequestID, request.RequestFingerprint)
	if err != nil {
		return nil, err
	}
	cached, err := json.Marshal(cachedActionResult{Fingerprint: fingerprint, Response: request.ResponseJSON})
	if err != nil {
		return nil, ErrInvalidGameRuntimeState
	}
	mode := "manual"
	if request.Timeout {
		mode = "timeout"
	}
	envelope, record, err := encodeArchiveRecord(request.Archive, request.RoomID, request.UserID, request.Generation, requestID, fingerprint, "skip_"+mode, request.ExpectedTurn,
		map[string]any{"mode": "multiplayer", "reason": mode, "response": archiveFactResponse(request.ResponseJSON)})
	if err != nil {
		return nil, err
	}
	cacheText := withArchiveCache(string(cached), record)
	currentMember := multiplayerDeadlineMember(request.RoomID, request.Generation, request.ExpectedTurn)
	nextMember := multiplayerDeadlineMember(request.RoomID, request.Generation, request.ExpectedTurn+1)
	keys := append([]string{
		multiplayerRuntimeVersionKey(request.RoomID), runtimeStatusKey(request.RoomID), runtimeGenerationKey(request.RoomID), runtimeTurnKey(request.RoomID),
		multiplayerTurnOrderKey(request.RoomID), multiplayerDeadlineKey(request.RoomID), multiplayerActionLeaseKey(request.RoomID), actionResultsKey(request.RoomID), multiplayerDeadlineQueueKey(),
		multiplayerRuntimePlayersKey(request.RoomID), multiplayerSummaryKey(request.RoomID), pendingMultiplayerAutoSavesKey(request.RoomID),
		pendingMultiplayerAutoSaveRoomsKey(), multiplayerRoundsKey(request.RoomID),
	}, gameArchiveKeys(request.RoomID)...)
	values, err := skipMultiplayerTurnScript.Run(ctx, r.client, keys, request.Generation, request.ExpectedTurn, request.UserID, requestID, string(request.ResponseJSON), cacheText, request.Now.UTC().UnixMilli(), mode,
		currentMember, request.NextDeadline.UTC().UnixMilli(), nextMember, int64(r.ttl/time.Second), request.RoomID, envelope).Slice()
	if err != nil {
		if record != nil {
			if _, probeErr := r.reconcileArchiveCommit(ctx, record); probeErr != nil {
				return nil, probeErr
			}
			values = []any{int64(1), string(request.ResponseJSON)}
		} else {
			return nil, fmt.Errorf("%w: skip multiplayer turn: %v", ErrGameRuntimeUnavailable, err)
		}
	}
	if len(values) != 2 {
		return nil, ErrGameRuntimeUnavailable
	}
	code, ok := redisInt64(values[0])
	if issue := archiveError(code); issue != nil {
		return nil, issue
	}
	if !ok {
		return nil, ErrGameRuntimeUnavailable
	}
	if code == 1 || code == 2 {
		encoded, ok := redisString(values[1])
		if !ok {
			return nil, ErrGameRuntimeUnavailable
		}
		var result model.MultiplayerSkipResult
		if code == 2 {
			cachedResult, decodeErr := decodeCachedActionResult(encoded)
			if decodeErr != nil || cachedResult.Fingerprint != fingerprint {
				if decodeErr == nil {
					return nil, ErrActionIdempotencyConflict
				}
				return nil, ErrGameRuntimeUnavailable
			}
			encoded = string(cachedResult.Response)
			result.Duplicate = true
		}
		if json.Unmarshal([]byte(encoded), &result) != nil || result.Generation != request.Generation || result.CurrentTurn != request.ExpectedTurn+1 {
			return nil, ErrGameRuntimeUnavailable
		}
		if record != nil {
			memory, deadline := r.archiveDelivery(ctx, request.RoomID, record.TimelineID)
			result.Memory = memory
			result.DeadlineAt = time.Time{}
			if deadline != nil {
				result.DeadlineAt = *deadline
			}
		}
		return &result, nil
	}
	switch code {
	case 3:
		return nil, ErrGameRuntimeNotPlaying
	case 4:
		return nil, ErrGameRuntimeGenerationConflict
	case 5:
		return nil, ErrGameRuntimeConflict
	case 6:
		return nil, ErrMultiplayerNotCurrentActor
	case 7:
		return nil, ErrMultiplayerDeadlineNotReached
	case 8:
		return nil, ErrMultiplayerActionInProgress
	default:
		return nil, ErrGameRuntimeUnavailable
	}
}

func (r *RedisGameStateRepo) ListDueMultiplayerDeadlines(ctx context.Context, now time.Time, limit int) ([]model.MultiplayerDeadlineTask, error) {
	if now.IsZero() || limit <= 0 || limit > 256 {
		return nil, ErrInvalidGameRuntimeState
	}
	values, err := listDueMultiplayerDeadlinesScript.Run(ctx, r.client, []string{multiplayerDeadlineQueueKey()}, now.UTC().UnixMilli(), limit).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("%w: list multiplayer deadlines: %v", ErrGameRuntimeUnavailable, err)
	}
	result := make([]model.MultiplayerDeadlineTask, 0, len(values))
	for _, member := range values {
		task, parseErr := parseMultiplayerDeadlineMember(member)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: malformed multiplayer deadline", ErrGameRuntimeUnavailable)
		}
		result = append(result, task)
	}
	return result, nil
}

func (r *RedisGameStateRepo) DiscardMultiplayerDeadline(ctx context.Context, task model.MultiplayerDeadlineTask) error {
	if task.RoomID == 0 || uuid.Validate(task.Generation) != nil || task.Turn < 0 ||
		task.Member != multiplayerDeadlineMember(task.RoomID, task.Generation, task.Turn) {
		return ErrInvalidGameRuntimeState
	}
	if err := discardMultiplayerDeadlineScript.Run(ctx, r.client, []string{
		multiplayerDeadlineQueueKey(), multiplayerRuntimeVersionKey(task.RoomID), runtimeStatusKey(task.RoomID),
		runtimeGenerationKey(task.RoomID), runtimeTurnKey(task.RoomID),
	}, task.Member, task.Generation, task.Turn).Err(); err != nil {
		return fmt.Errorf("%w: discard multiplayer deadline: %v", ErrGameRuntimeUnavailable, err)
	}
	return nil
}

func (r *RedisGameStateRepo) multiplayerCommitArguments(mutation *model.MultiplayerActionMutation) ([]string, []any, string, error) {
	if mutation == nil || uuid.Validate(mutation.Generation) != nil || mutation.NextDeadline.IsZero() || len(mutation.Messages) != 2 || !json.Valid(mutation.ResponseJSON) || len(mutation.ResponseJSON) > maxCachedActionResponseBytes {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}
	requestID, fingerprint, err := validateMultiplayerActionIdentity(mutation.RoomID, mutation.UserID, mutation.ExpectedTurn, mutation.RequestID, mutation.RequestFingerprint)
	if err != nil {
		return nil, nil, "", err
	}
	cached, err := json.Marshal(cachedActionResult{Fingerprint: fingerprint, Response: mutation.ResponseJSON})
	if err != nil {
		return nil, nil, "", ErrInvalidGameRuntimeState
	}
	encodedMessages := make([]string, 2)
	for index, expectedRole := range []string{"user", "assistant"} {
		message := mutation.Messages[index]
		message.Role, message.Content = strings.TrimSpace(message.Role), strings.TrimSpace(message.Content)
		if message.Role != expectedRole || message.Content == "" || len(message.Content) > 64<<10 {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		encoded, marshalErr := json.Marshal(message)
		if marshalErr != nil {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		encodedMessages[index] = string(encoded)
	}
	keys := []string{
		multiplayerRuntimeVersionKey(mutation.RoomID), runtimeStatusKey(mutation.RoomID), runtimeGenerationKey(mutation.RoomID), runtimeTurnKey(mutation.RoomID),
		multiplayerTurnOrderKey(mutation.RoomID), multiplayerDeadlineKey(mutation.RoomID), multiplayerRoundsKey(mutation.RoomID), multiplayerActionLeaseKey(mutation.RoomID),
		actionResultsKey(mutation.RoomID), multiplayerDeadlineQueueKey(), multiplayerRuntimePlayersKey(mutation.RoomID),
		multiplayerSummaryKey(mutation.RoomID), pendingMultiplayerAutoSavesKey(mutation.RoomID), pendingMultiplayerAutoSaveRoomsKey(),
	}
	arguments := []any{mutation.Generation, mutation.ExpectedTurn, mutation.UserID, requestID, fingerprint, string(cached), encodedMessages[0], encodedMessages[1],
		mutation.NextDeadline.UTC().UnixMilli(), multiplayerDeadlineMember(mutation.RoomID, mutation.Generation, mutation.ExpectedTurn),
		multiplayerDeadlineMember(mutation.RoomID, mutation.Generation, mutation.ExpectedTurn+1), int64(r.ttl / time.Second), len(mutation.PlayerMutations)}
	seen := make(map[uint]struct{}, len(mutation.PlayerMutations))
	for _, player := range mutation.PlayerMutations {
		if player.UserID == 0 {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		if _, duplicate := seen[player.UserID]; duplicate {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		seen[player.UserID] = struct{}{}
		_, normalized, normalizeErr := normalizePlayerState(player.PlayerStateChanges, false)
		if normalizeErr != nil {
			return nil, nil, "", normalizeErr
		}
		if _, changesIdentity := normalized["character_id"]; changesIdentity {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		if len(player.ItemMutations)+len(player.BuffMutations) > maxActionRuntimeEffects {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		for index := range player.ItemMutations {
			item := &player.ItemMutations[index]
			item.Name, item.Description = strings.TrimSpace(item.Name), strings.TrimSpace(item.Description)
			if item.Name == "" || item.QuantityDelta == 0 || utf8.RuneCountInString(item.Name) > 200 || utf8.RuneCountInString(item.Description) > 1000 {
				return nil, nil, "", ErrInvalidGameRuntimeState
			}
		}
		for index := range player.BuffMutations {
			buff := &player.BuffMutations[index]
			buff.Name = strings.TrimSpace(buff.Name)
			if buff.Name == "" || buff.Duration <= 0 || utf8.RuneCountInString(buff.Name) > 200 {
				return nil, nil, "", ErrInvalidGameRuntimeState
			}
		}
		if normalized == nil {
			normalized = map[string]string{}
		}
		encoded, marshalErr := json.Marshal(encodedMultiplayerMutation{UserID: player.UserID, PlayerStateChanges: normalized,
			Items: append([]model.RuntimeItemMutation{}, player.ItemMutations...), Buffs: append([]model.RuntimeBuffMutation{}, player.BuffMutations...)})
		if marshalErr != nil {
			return nil, nil, "", ErrInvalidGameRuntimeState
		}
		arguments = append(arguments, string(encoded))
		keys = append(keys, runtimePlayerKey(mutation.RoomID, player.UserID), itemStateKey(mutation.RoomID, player.UserID), buffStateKey(mutation.RoomID, player.UserID))
	}
	arguments = append(arguments, mutation.RoomID)
	players := make([]json.RawMessage, 0, len(mutation.PlayerMutations))
	for i := range mutation.PlayerMutations {
		players = append(players, json.RawMessage(arguments[13+i].(string)))
	}
	envelope, record, err := encodeArchiveRecord(mutation.Archive, mutation.RoomID, mutation.UserID, mutation.Generation, requestID, fingerprint, "action", mutation.ExpectedTurn,
		map[string]any{"mode": "multiplayer", "messages": []json.RawMessage{json.RawMessage(encodedMessages[0]), json.RawMessage(encodedMessages[1])}, "players": players, "response": archiveFactResponse(mutation.ResponseJSON)})
	if err != nil {
		return nil, nil, "", err
	}
	arguments[5] = withArchiveCache(arguments[5].(string), record)
	arguments = append(arguments, envelope)
	keys = append(keys, gameArchiveKeys(mutation.RoomID)...)
	return keys, arguments, fingerprint, nil
}

func validateMultiplayerActionIdentity(roomID, userID uint, expectedTurn int, requestID, fingerprint string) (string, string, error) {
	if roomID == 0 || userID == 0 || expectedTurn < 0 {
		return "", "", ErrInvalidGameRuntimeState
	}
	parsed, err := uuid.Parse(strings.TrimSpace(requestID))
	if err != nil {
		return "", "", ErrInvalidGameRuntimeState
	}
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	decoded, err := hex.DecodeString(fingerprint)
	if err != nil || len(decoded) != 32 {
		return "", "", ErrInvalidGameRuntimeState
	}
	return parsed.String(), fingerprint, nil
}

func parseMultiplayerDeadlineMember(member string) (model.MultiplayerDeadlineTask, error) {
	parts := strings.Split(member, ":")
	if len(parts) != 3 {
		return model.MultiplayerDeadlineTask{}, ErrInvalidGameRuntimeState
	}
	roomID, roomErr := strconv.ParseUint(parts[0], 10, 64)
	turn, turnErr := strconv.Atoi(parts[2])
	if roomErr != nil || roomID == 0 || uuid.Validate(parts[1]) != nil || turnErr != nil || turn < 0 {
		return model.MultiplayerDeadlineTask{}, ErrInvalidGameRuntimeState
	}
	return model.MultiplayerDeadlineTask{RoomID: uint(roomID), Generation: parts[1], Turn: turn, Member: member}, nil
}

func multiplayerActionLeaseKey(roomID uint) string {
	return fmt.Sprintf("room:%d:action_lease", roomID)
}

func multiplayerTurnOrderKey(roomID uint) string { return fmt.Sprintf("room:%d:turn_order", roomID) }
func multiplayerRoundsKey(roomID uint) string    { return fmt.Sprintf("room:%d:rounds", roomID) }
