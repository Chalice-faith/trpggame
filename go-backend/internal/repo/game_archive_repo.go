package repo

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"trpggame/internal/model"
)

//go:embed lua/game_archive.lua
var gameArchiveLua string

const maxArchiveRedisPosition = uint64(9007199254740990)

// Remove the persistent capability key before running a legacy cleanup script.
// A4 will introduce token-checked replacements for memory-enabled rooms.
const legacyArchiveGuardLua = `
if redis.call('EXISTS', KEYS[#KEYS]) ~= 0 or redis.call('EXISTS', (string.gsub(KEYS[#KEYS], 'meta$', 'pending'))) ~= 0 then return redis.error_reply('GAME_ARCHIVE_GATE') end
table.remove(KEYS)
`

func archiveRuntimeError(err error) error {
	if err != nil && strings.Contains(err.Error(), "GAME_ARCHIVE_GATE") {
		return ErrGameArchiveNotReady
	}
	return err
}

func archiveWrappedError(err error) error {
	if issue := archiveRuntimeError(err); issue == ErrGameArchiveNotReady {
		return issue
	}
	return ErrGameRuntimeUnavailable
}

var (
	ErrGameArchiveNotReady      = errors.New("game archive is not ready")
	ErrGameArchiveBranchChanged = errors.New("game archive branch changed")
	ErrGameArchiveCorrupt       = errors.New("game archive ledger is corrupt")
	ErrGameArchiveLeaseConflict = errors.New("game archive lease conflict")
	ErrGameArchiveCommitUnknown = errors.New("game archive commit outcome is unknown")
)

func archiveError(code int64) error {
	switch code {
	case 90:
		return ErrGameArchiveNotReady
	case 91:
		return ErrGameArchiveBranchChanged
	case 92:
		return ErrGameArchiveCorrupt
	case 93:
		return ErrActionIdempotencyConflict
	}
	return nil
}

func gameArchiveKeys(roomID uint) []string {
	prefix := fmt.Sprintf("game:archive:room:%d:", roomID)
	return []string{prefix + "meta", prefix + "pending", prefix + "lease", "game:archive:pending_rooms", prefix + "autosaves", "game:archive:auto_save_rooms"}
}

func encodeArchiveRecord(record *model.GameActionRecord, roomID, actorID uint, generation, requestID, fingerprint, kind string, turn int, payload any) (string, *model.GameActionRecord, error) {
	if record == nil {
		return "", nil, nil
	}
	copy := *record
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return "", nil, model.ErrInvalidMemoryData
	}
	canonical, err := model.CanonicalMemoryJSON(encodedPayload)
	if err != nil {
		return "", nil, err
	}
	if len(copy.Payload) != 0 {
		supplied, err := model.CanonicalMemoryJSON(copy.Payload)
		if err != nil || !bytes.Equal(supplied, canonical) {
			return "", nil, model.ErrInvalidMemoryData
		}
	}
	copy.Payload = canonical
	if err := copy.Seal(); err != nil {
		return "", nil, err
	}
	if copy.Position > maxArchiveRedisPosition || copy.SourceRevision > maxArchiveRedisPosition || uint64(roomID) > maxArchiveRedisPosition || turn < 0 || uint64(turn) >= maxArchiveRedisPosition || copy.RoomID != roomID ||
		copy.ActorID == nil || uint64(*copy.ActorID) > maxArchiveRedisPosition || (actorID != 0 && *copy.ActorID != actorID) || copy.SourceGeneration != generation || copy.RequestID != requestID ||
		copy.Fingerprint != fingerprint || copy.Kind != kind || copy.TurnBefore != turn || copy.TurnAfter != turn+1 {
		return "", nil, model.ErrInvalidMemoryData
	}
	body, err := json.Marshal(copy)
	if err != nil || len(body) > model.GameMemoryMaxPayloadBytes+8192 {
		return "", nil, model.ErrInvalidMemoryData
	}
	envelope, err := json.Marshal(struct {
		RecordJSON string `json:"record_json"`
	}{string(body)})
	return string(envelope), &copy, err
}

func archiveFactResponse(raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw
	}
	delete(fields, "deadline_at")
	delete(fields, "memory")
	encoded, _ := json.Marshal(fields)
	return encoded
}

func archiveRecordEnvelope(value any) *model.GameActionRecord {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	var envelope struct {
		RecordJSON string `json:"record_json"`
	}
	var record model.GameActionRecord
	if json.Unmarshal([]byte(text), &envelope) != nil || json.Unmarshal([]byte(envelope.RecordJSON), &record) != nil {
		return nil
	}
	return &record
}

// The capability is only bound by a future verified start/load control operation.
var bindGameArchiveScript = redis.NewScript(gameArchiveLua + `
for index, kind in ipairs({'hash','hash','hash','zset','hash','zset'}) do
  if not archive_type(KEYS[index], kind) then return 92 end
end
if redis.call('TYPE', KEYS[7]).ok ~= 'string' or redis.call('TYPE', KEYS[8]).ok ~= 'string' or
   redis.call('TYPE', KEYS[9]).ok ~= 'string' then return 92 end
if redis.call('GET', KEYS[8]) ~= ARGV[2] or redis.call('GET', KEYS[9]) ~= ARGV[6] then return 91 end
local status = redis.call('GET', KEYS[7])
if status ~= 'playing' and status ~= 'paused' and status ~= 'provisional' then return 90 end
local meta, code = archive_meta(KEYS[1], KEYS[2])
if code ~= 0 then return code end
if meta then
  if meta.timeline == ARGV[1] and meta.head == tonumber(ARGV[3]) and meta.durable == tonumber(ARGV[3]) and
     meta.revision == tonumber(ARGV[4]) and meta.mode == ARGV[5] and meta.timeout == tonumber(ARGV[7]) and
     meta.control == '' and meta.state == 'ready' and redis.call('EXISTS', KEYS[2]) == 0 then return 2 end
  return 91
end
if redis.call('EXISTS', KEYS[3]) ~= 0 or redis.call('HLEN', KEYS[5]) ~= 0 then return 92 end
redis.call('HSET', KEYS[1], 'timeline_id', ARGV[1], 'head_position', ARGV[3], 'durable_position', ARGV[3],
  'revision', ARGV[4], 'mode', ARGV[5], 'timeout_ms', ARGV[7], 'archive_state', 'ready', 'control_operation_id', '')
return 1
`)

func (r *RedisGameStateRepo) BindGameArchive(ctx context.Context, binding model.GameArchiveBinding) error {
	if binding.RoomID == 0 || uint64(binding.RoomID) > maxArchiveRedisPosition || !model.ValidMemoryUUID(binding.TimelineID) || !model.ValidMemoryUUID(binding.Generation) ||
		binding.Position > maxArchiveRedisPosition || binding.Revision == 0 || binding.Revision > maxArchiveRedisPosition || binding.ExpectedTurn < 0 ||
		(binding.Mode != "solo" && binding.Mode != "multiplayer") ||
		(binding.Mode == "multiplayer" && (binding.TurnTimeout < time.Second || binding.TurnTimeout > time.Hour)) ||
		(binding.Mode == "solo" && binding.TurnTimeout != 0) {
		return model.ErrInvalidMemoryData
	}
	keys := append(gameArchiveKeys(binding.RoomID), runtimeStatusKey(binding.RoomID), runtimeGenerationKey(binding.RoomID), runtimeTurnKey(binding.RoomID))
	code, err := bindGameArchiveScript.Run(ctx, r.client, keys, binding.TimelineID, binding.Generation, binding.Position,
		binding.Revision, binding.Mode, binding.ExpectedTurn, binding.TurnTimeout.Milliseconds()).Int64()
	if err != nil {
		return fmt.Errorf("%w: bind archive", ErrGameRuntimeUnavailable)
	}
	if issue := archiveError(code); issue != nil {
		return issue
	}
	if code != 1 && code != 2 {
		return ErrGameArchiveCorrupt
	}
	return nil
}

var getGameArchiveScript = redis.NewScript(gameArchiveLua + `
local meta, code = archive_meta(KEYS[1], KEYS[2])
if code ~= 0 then return {code} end
if not meta then return {0} end
if meta.state == 'ready' and (meta.head ~= meta.durable or redis.call('EXISTS', KEYS[2]) ~= 0) then return {92} end
if meta.state == 'pending' and (meta.head ~= meta.durable + 1 or redis.call('EXISTS', KEYS[2]) == 0) then return {92} end
return {1, redis.call('HGETALL', KEYS[1])}
`)

func decodeArchiveMetadata(raw any) (*model.GameArchiveRuntime, error) {
	values, ok := redisStringSlice(raw)
	if !ok || len(values)%2 != 0 {
		return nil, ErrGameArchiveCorrupt
	}
	fields := make(map[string]string)
	for i := 0; i < len(values); i += 2 {
		fields[values[i]] = values[i+1]
	}
	head, err1 := strconv.ParseUint(fields["head_position"], 10, 64)
	durable, err2 := strconv.ParseUint(fields["durable_position"], 10, 64)
	revision, err3 := strconv.ParseUint(fields["revision"], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || !model.ValidMemoryUUID(fields["timeline_id"]) || head < durable || head-durable > 1 ||
		head > maxArchiveRedisPosition || revision == 0 || revision > maxArchiveRedisPosition ||
		(fields["control_operation_id"] != "" && !model.ValidMemoryUUID(fields["control_operation_id"])) {
		return nil, ErrGameArchiveCorrupt
	}
	switch fields["archive_state"] {
	case "ready", "pending", "recovering", "blocked":
	default:
		return nil, ErrGameArchiveCorrupt
	}
	return &model.GameArchiveRuntime{TimelineID: fields["timeline_id"], HeadPosition: head, DurablePosition: durable,
		ArchiveState: fields["archive_state"], ControlOperationID: fields["control_operation_id"], Revision: revision}, nil
}

func (r *RedisGameStateRepo) GetGameArchive(ctx context.Context, roomID uint) (*model.GameArchiveRuntime, error) {
	if roomID == 0 {
		return nil, model.ErrInvalidMemoryData
	}
	values, err := getGameArchiveScript.Run(ctx, r.client, gameArchiveKeys(roomID)[:2]).Slice()
	if err != nil {
		return nil, fmt.Errorf("%w: read archive", ErrGameRuntimeUnavailable)
	}
	if len(values) == 1 {
		code, ok := redisInt64(values[0])
		if ok && code == 0 {
			return nil, nil
		}
		if issue := archiveError(code); issue != nil {
			return nil, issue
		}
	}
	if len(values) != 2 {
		return nil, ErrGameArchiveCorrupt
	}
	return decodeArchiveMetadata(values[1])
}

var listArchiveRoomsScript = redis.NewScript(`
if redis.call('TYPE', KEYS[1]).ok ~= 'none' and redis.call('TYPE', KEYS[1]).ok ~= 'zset' then return redis.error_reply('invalid archive queue type') end
return redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
`)

func (r *RedisGameStateRepo) ListPendingArchiveRooms(ctx context.Context, now time.Time, limit int) ([]uint, error) {
	if now.IsZero() || limit < 1 || limit > 256 {
		return nil, model.ErrInvalidMemoryData
	}
	values, err := listArchiveRoomsScript.Run(ctx, r.client, []string{gameArchiveKeys(1)[3]}, now.UTC().UnixMilli(), limit).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("%w: list archive rooms", ErrGameRuntimeUnavailable)
	}
	rooms := make([]uint, 0, len(values))
	for _, text := range values {
		id, err := strconv.ParseUint(text, 10, 64)
		if err != nil || id == 0 || uint64(uint(id)) != id {
			return nil, ErrGameArchiveCorrupt
		}
		rooms = append(rooms, uint(id))
	}
	return rooms, nil
}

var claimArchiveScript = redis.NewScript(gameArchiveLua + `
local function damaged()
  if redis.call('TYPE', KEYS[1]).ok == 'hash' then redis.call('HSET', KEYS[1], 'archive_state', 'blocked') end
  if redis.call('TYPE', KEYS[4]).ok == 'zset' then redis.call('ZREM', KEYS[4], ARGV[3]) end
  return {92}
end
local meta, code = archive_meta(KEYS[1], KEYS[2])
if code == 92 then return damaged() end
if code ~= 0 then return {code} end
if not meta then
  if archive_type(KEYS[4], 'zset') then redis.call('ZREM', KEYS[4], ARGV[3]) end
  return {0}
end
if meta.state == 'blocked' then
  if archive_type(KEYS[4], 'zset') then redis.call('ZREM', KEYS[4], ARGV[3]) end
  return {90}
end
if redis.call('EXISTS', KEYS[2]) == 0 then
  if meta.head ~= meta.durable then return damaged() end
  if archive_type(KEYS[4], 'zset') then redis.call('ZREM', KEYS[4], ARGV[3]) end
  return {0}
end
if meta.state ~= 'pending' and meta.state ~= 'recovering' then return damaged() end
if not archive_type(KEYS[3], 'hash') then return damaged() end
if not archive_type(KEYS[4], 'zset') then return damaged() end
local commit = redis.call('HGET', KEYS[2], 'commit_id')
local hash = redis.call('HGET', KEYS[2], 'payload_hash')
if not commit or not hash or redis.call('HGET', KEYS[2], 'timeline_id') ~= meta.timeline or
   tonumber(redis.call('HGET', KEYS[2], 'position')) ~= meta.head or meta.head ~= meta.durable + 1 then return damaged() end
local valid, record = pcall(cjson.decode, redis.call('HGET', KEYS[2], 'record_json') or '')
if not valid or type(record) ~= 'table' or record.CommitID ~= commit or record.PayloadHash ~= hash or
   record.TimelineID ~= meta.timeline or record.Position ~= meta.head or record.SourceRevision ~= meta.revision then return damaged() end
if redis.call('EXISTS', KEYS[3]) ~= 0 and (redis.call('HGET', KEYS[3], 'owner_token') ~= ARGV[1] or
   redis.call('HGET', KEYS[3], 'commit_id') ~= commit or redis.call('HGET', KEYS[3], 'payload_hash') ~= hash) then return {3} end
redis.call('HSET', KEYS[3], 'owner_token', ARGV[1], 'commit_id', commit, 'payload_hash', hash)
redis.call('PEXPIRE', KEYS[3], ARGV[2])
local clock = redis.call('TIME')
redis.call('ZADD', KEYS[4], tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000) + tonumber(ARGV[2]), ARGV[3])
return {1, redis.call('HGETALL', KEYS[2])}
`)

func decodePendingArchive(raw any) (*model.PendingGameArchive, error) {
	values, ok := redisStringSlice(raw)
	if !ok || len(values)%2 != 0 {
		return nil, ErrGameArchiveCorrupt
	}
	fields := make(map[string]string)
	for i := 0; i < len(values); i += 2 {
		fields[values[i]] = values[i+1]
	}
	var record model.GameActionRecord
	if json.Unmarshal([]byte(fields["record_json"]), &record) != nil || !model.ValidMemoryHash(record.PayloadHash) || record.Seal() != nil ||
		record.CommitID != fields["commit_id"] || record.PayloadHash != fields["payload_hash"] || record.TimelineID != fields["timeline_id"] ||
		strconv.FormatUint(record.Position, 10) != fields["position"] || !json.Valid([]byte(fields["response_json"])) {
		return nil, ErrGameArchiveCorrupt
	}
	cached, err := decodeCachedActionResult(fields["response_json"])
	if err != nil || cached.Fingerprint != record.Fingerprint {
		return nil, ErrGameArchiveCorrupt
	}
	var attempts uint64
	if fields["attempts"] != "" {
		attempts, err = strconv.ParseUint(fields["attempts"], 10, 32)
		if err != nil || attempts > 30 {
			return nil, ErrGameArchiveCorrupt
		}
	}
	return &model.PendingGameArchive{Record: &record, ResponseJSON: cached.Response, BoundarySnapshot: []byte(fields["boundary_snapshot"]), Attempts: uint(attempts)}, nil
}

// Reusing the same owner token renews its lease; expiry never deletes the outbox.
func (r *RedisGameStateRepo) ClaimGameArchive(ctx context.Context, roomID uint, ownerToken string, ttl time.Duration) (*model.PendingGameArchive, error) {
	if roomID == 0 || !model.ValidMemoryUUID(ownerToken) || ttl < time.Second || ttl > 5*time.Minute {
		return nil, model.ErrInvalidMemoryData
	}
	values, err := claimArchiveScript.Run(ctx, r.client, gameArchiveKeys(roomID)[:4], ownerToken, ttl.Milliseconds(), roomID).Slice()
	if err != nil {
		return nil, fmt.Errorf("%w: claim archive", ErrGameRuntimeUnavailable)
	}
	if len(values) == 1 {
		code, _ := redisInt64(values[0])
		if code == 0 {
			return nil, nil
		}
		if code == 3 {
			return nil, ErrGameArchiveLeaseConflict
		}
		if issue := archiveError(code); issue != nil {
			return nil, issue
		}
	}
	if len(values) != 2 {
		return nil, ErrGameArchiveCorrupt
	}
	pending, err := decodePendingArchive(values[1])
	if err != nil {
		r.quarantineArchiveClaim(ctx, roomID, ownerToken)
		return nil, err
	}
	if pending.Record.RoomID != roomID {
		r.quarantineArchiveClaim(ctx, roomID, ownerToken)
		return nil, ErrGameArchiveCorrupt
	}
	pending.OwnerToken = ownerToken
	return pending, nil
}

var quarantineArchiveClaimScript = redis.NewScript(`
if redis.call('TYPE', KEYS[1]).ok ~= 'hash' or redis.call('TYPE', KEYS[3]).ok ~= 'hash' or
   redis.call('HGET', KEYS[3], 'owner_token') ~= ARGV[1] or not (redis.call('TYPE', KEYS[4]).ok == 'zset') then return 0 end
redis.call('HSET', KEYS[1], 'archive_state', 'blocked')
redis.call('ZREM', KEYS[4], ARGV[2])
redis.call('DEL', KEYS[3])
return 1
`)

func (r *RedisGameStateRepo) quarantineArchiveClaim(ctx context.Context, roomID uint, owner string) {
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_ = quarantineArchiveClaimScript.Run(probe, r.client, gameArchiveKeys(roomID)[:4], owner, roomID).Err()
}

var releaseArchiveScript = redis.NewScript(gameArchiveLua + `
local meta, code = archive_meta(KEYS[1], KEYS[2]); if code ~= 0 then return code end
if not meta or meta.timeline ~= ARGV[4] or redis.call('HGET', KEYS[2], 'position') ~= ARGV[5] or
   redis.call('HGET', KEYS[2], 'commit_id') ~= ARGV[2] or redis.call('HGET', KEYS[2], 'payload_hash') ~= ARGV[3] then return 0 end
if redis.call('TYPE', KEYS[3]).ok ~= 'hash' or redis.call('HGET', KEYS[3], 'owner_token') ~= ARGV[1] or
   redis.call('HGET', KEYS[3], 'commit_id') ~= ARGV[2] or redis.call('HGET', KEYS[3], 'payload_hash') ~= ARGV[3] then return 0 end
redis.call('DEL', KEYS[3])
return 1
`)

func (r *RedisGameStateRepo) ReleaseGameArchive(ctx context.Context, ack model.GameArchiveACK) error {
	if err := validateArchiveACK(ack); err != nil {
		return err
	}
	code, err := releaseArchiveScript.Run(ctx, r.client, gameArchiveKeys(ack.RoomID)[:3], ack.OwnerToken, ack.CommitID, ack.PayloadHash, ack.TimelineID, ack.Position).Int64()
	if err != nil {
		return fmt.Errorf("%w: release archive", ErrGameRuntimeUnavailable)
	}
	if code != 1 {
		if issue := archiveError(code); issue != nil {
			return issue
		}
		return ErrGameArchiveLeaseConflict
	}
	return nil
}

func validateArchiveACK(ack model.GameArchiveACK) error {
	if ack.RoomID == 0 || !model.ValidMemoryUUID(ack.TimelineID) || !model.ValidMemoryUUID(ack.CommitID) || !model.ValidMemoryUUID(ack.OwnerToken) ||
		!model.ValidMemoryHash(ack.PayloadHash) || ack.Position == 0 || ack.Position > maxArchiveRedisPosition {
		return model.ErrInvalidMemoryData
	}
	return nil
}

var acknowledgeArchiveScript = redis.NewScript(gameArchiveLua + `
local meta, code = archive_meta(KEYS[1], KEYS[2])
if code ~= 0 then return {code} end
if not meta or meta.timeline ~= ARGV[1] then return {91} end
for index, kind in ipairs({'hash','hash','hash','zset','hash','zset','string','string','string','string','zset'}) do
  if not archive_type(KEYS[index], kind) then return {92} end
end
if redis.call('EXISTS', KEYS[2]) == 0 then
  if redis.call('HGET', KEYS[1], 'last_commit_id') == ARGV[2] and redis.call('HGET', KEYS[1], 'last_hash') == ARGV[3] and
     redis.call('HGET', KEYS[1], 'last_owner_token') == ARGV[4] and meta.durable == tonumber(ARGV[5]) then
    return {2, redis.call('GET', KEYS[10]) or '', redis.call('HGETALL', KEYS[1]), redis.call('EXISTS', KEYS[7])}
  end
  return {3}
end
if redis.call('TYPE', KEYS[3]).ok ~= 'hash' or redis.call('HGET', KEYS[3], 'owner_token') ~= ARGV[4] or
   redis.call('HGET', KEYS[3], 'commit_id') ~= ARGV[2] or redis.call('HGET', KEYS[3], 'payload_hash') ~= ARGV[3] then return {3} end
if redis.call('HGET', KEYS[2], 'commit_id') ~= ARGV[2] or redis.call('HGET', KEYS[2], 'payload_hash') ~= ARGV[3] or
   redis.call('HGET', KEYS[2], 'timeline_id') ~= ARGV[1] or redis.call('HGET', KEYS[2], 'position') ~= ARGV[5] or
   meta.head ~= tonumber(ARGV[5]) or meta.durable + 1 ~= meta.head or (meta.state ~= 'pending' and meta.state ~= 'recovering') then return {92} end
local ok, record = pcall(cjson.decode, redis.call('HGET', KEYS[2], 'record_json') or '')
if not ok then return {92} end
local available = redis.call('EXISTS', KEYS[7]) == 1 and redis.call('EXISTS', KEYS[8]) == 1 and redis.call('EXISTS', KEYS[9]) == 1
if type(record) ~= 'table' or record.CommitID ~= ARGV[2] or record.PayloadHash ~= ARGV[3] or
   record.TimelineID ~= ARGV[1] or record.Position ~= meta.head then return {92} end
local can_resume = available and redis.call('GET', KEYS[7]) == 'playing' and redis.call('GET', KEYS[8]) == record.SourceGeneration and
  redis.call('GET', KEYS[9]) == tostring(record.TurnAfter) and meta.control == ''
if available and redis.call('GET', KEYS[7]) == 'playing' and meta.control == '' and not can_resume then return {92} end
local boundary = redis.call('HGET', KEYS[2], 'boundary_snapshot') or ''
if boundary ~= '' then
  local valid, snapshot = pcall(cjson.decode, boundary)
  if not valid or type(snapshot) ~= 'table' or type(snapshot.memory) ~= 'table' or snapshot.memory.timeline_id ~= meta.timeline or
     snapshot.memory.head_position ~= meta.head then return {92} end
end
if boundary ~= '' then
  redis.call('HSET', KEYS[5], meta.timeline .. ':' .. ARGV[5] .. ':' .. archive_number(record.RoundAfter), boundary)
  redis.call('ZADD', KEYS[6], 0, ARGV[6])
end
local deadline = ''
if can_resume and meta.mode == 'multiplayer' then
  local clock = redis.call('TIME')
  deadline = tostring(tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000) + meta.timeout)
  redis.call('SET', KEYS[10], deadline, 'PX', ARGV[7])
  redis.call('ZADD', KEYS[11], deadline, ARGV[6] .. ':' .. redis.call('GET', KEYS[8]) .. ':' .. tostring(record.TurnAfter))
end
redis.call('HSET', KEYS[1], 'durable_position', ARGV[5], 'archive_state', meta.control == '' and 'ready' or 'recovering',
  'last_commit_id', ARGV[2], 'last_hash', ARGV[3], 'last_owner_token', ARGV[4])
redis.call('DEL', KEYS[2], KEYS[3])
redis.call('ZREM', KEYS[4], ARGV[6])
return {1, deadline, redis.call('HGETALL', KEYS[1]), available and 1 or 0}
`)

// Caller confirms a successful MySQL Archive transaction before invoking ACK.
func (r *RedisGameStateRepo) AcknowledgeGameArchive(ctx context.Context, ack model.GameArchiveACK) (*model.GameArchiveACKResult, error) {
	if err := validateArchiveACK(ack); err != nil {
		return nil, err
	}
	keys := append(gameArchiveKeys(ack.RoomID), runtimeStatusKey(ack.RoomID), runtimeGenerationKey(ack.RoomID), runtimeTurnKey(ack.RoomID), multiplayerDeadlineKey(ack.RoomID), multiplayerDeadlineQueueKey())
	values, err := acknowledgeArchiveScript.Run(ctx, r.client, keys, ack.TimelineID, ack.CommitID, ack.PayloadHash, ack.OwnerToken,
		ack.Position, ack.RoomID, r.ttl.Milliseconds()).Slice()
	if err != nil {
		return nil, fmt.Errorf("%w: archive ACK outcome unknown", ErrGameArchiveCommitUnknown)
	}
	if len(values) == 0 {
		return nil, ErrGameArchiveCorrupt
	}
	code, _ := redisInt64(values[0])
	if issue := archiveError(code); issue != nil {
		return nil, issue
	}
	if code == 3 {
		return nil, ErrGameArchiveLeaseConflict
	}
	if len(values) != 4 || (code != 1 && code != 2) {
		return nil, ErrGameArchiveCorrupt
	}
	memory, err := decodeArchiveMetadata(values[2])
	if err != nil {
		return nil, err
	}
	result := &model.GameArchiveACKResult{Duplicate: code == 2, Memory: memory}
	available, _ := redisInt64(values[3])
	result.RuntimeAvailable = available == 1
	text, _ := redisString(values[1])
	if text != "" {
		millis, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, ErrGameArchiveCorrupt
		}
		deadline := time.UnixMilli(millis).UTC()
		result.DeadlineAt = &deadline
	}
	return result, nil
}

var reconcileArchiveScript = redis.NewScript(gameArchiveLua + `
local meta, code = archive_meta(KEYS[1], KEYS[2])
if code ~= 0 or not meta then return {92} end
if meta.timeline ~= ARGV[1] then return {91} end
if not archive_type(KEYS[3], 'hash') then return {92} end
local value = redis.call('HGET', KEYS[3], ARGV[5])
local ok, cached = pcall(cjson.decode, value or '')
local matching = ok and type(cached) == 'table' and cached.archive_commit_id == ARGV[2] and cached.archive_hash == ARGV[3] and cached.archive_timeline_id == meta.timeline
if matching and redis.call('HGET', KEYS[2], 'commit_id') == ARGV[2] and redis.call('HGET', KEYS[2], 'payload_hash') == ARGV[3] and
   meta.state == 'pending' and meta.head == tonumber(ARGV[4]) and meta.durable + 1 == meta.head and
   redis.call('HGET', KEYS[2], 'position') == ARGV[4] and redis.call('HGET', KEYS[2], 'record_json') and
   redis.call('HGET', KEYS[2], 'response_json') == value then return {1} end
if meta.durable >= tonumber(ARGV[4]) then
  if archive_type(KEYS[3], 'hash') then
    local value = redis.call('HGET', KEYS[3], ARGV[5])
    if value then
      local ok, cached = pcall(cjson.decode, value)
      if ok and type(cached) == 'table' and cached.archive_commit_id == ARGV[2] and cached.archive_hash == ARGV[3] and cached.archive_timeline_id == meta.timeline then return {2} end
    end
  end
end
if meta.head < tonumber(ARGV[4]) or meta.head == tonumber(ARGV[4]) then
  redis.call('HSET', KEYS[1], 'archive_state', 'blocked')
end
return {3}
`)

// Transport/script errors are not evidence of rollback. Reconcile under a fresh
// bounded context; a complete outbox proves commit, otherwise close the gate.
func (r *RedisGameStateRepo) reconcileArchiveCommit(ctx context.Context, record *model.GameActionRecord) (*model.GameArchiveRuntime, error) {
	if record == nil {
		return nil, ErrGameRuntimeUnavailable
	}
	sealed := *record
	if sealed.Seal() != nil {
		return nil, ErrGameArchiveCommitUnknown
	}
	record = &sealed
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	keys := append(gameArchiveKeys(record.RoomID)[:2], actionResultsKey(record.RoomID))
	values, err := reconcileArchiveScript.Run(probe, r.client, keys, record.TimelineID, record.CommitID, record.PayloadHash, record.Position, record.RequestID).Slice()
	if err != nil || len(values) != 1 {
		return nil, ErrGameArchiveCommitUnknown
	}
	code, _ := redisInt64(values[0])
	if code != 1 && code != 2 {
		return nil, ErrGameArchiveCommitUnknown
	}
	memory, err := r.GetGameArchive(probe, record.RoomID)
	if err != nil {
		return nil, ErrGameArchiveCommitUnknown
	}
	return memory, nil
}

func archiveExpectation(value []model.GameArchiveExpectation) (string, string, error) {
	if len(value) == 0 {
		return "", "", nil
	}
	if len(value) != 1 || !model.ValidMemoryUUID(value[0].TimelineID) || !model.ValidMemoryUUID(value[0].Generation) {
		return "", "", model.ErrInvalidMemoryData
	}
	return value[0].TimelineID, value[0].Generation, nil
}

// Response delivery metadata follows current state and never reopens old deadlines.
func archiveResponse(raw json.RawMessage, memory *model.GameArchiveRuntime, deadline *time.Time) json.RawMessage {
	if memory == nil {
		return raw
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil {
		return raw
	}
	response["memory"], _ = json.Marshal(memory)
	if _, exists := response["deadline_at"]; exists {
		response["deadline_at"], _ = json.Marshal(deadline)
	}
	body, err := json.Marshal(response)
	if err != nil {
		return raw
	}
	return body
}

// Internal JSON cache fields keep commit identity separate from mutable delivery metadata.
func withArchiveCache(raw string, record *model.GameActionRecord) string {
	if record == nil {
		return raw
	}
	var cache map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &cache) != nil {
		return raw
	}
	cache["archive_commit_id"], _ = json.Marshal(record.CommitID)
	cache["archive_hash"], _ = json.Marshal(record.PayloadHash)
	cache["archive_timeline_id"], _ = json.Marshal(record.TimelineID)
	cache["archive_position"], _ = json.Marshal(record.Position)
	encoded, _ := json.Marshal(cache)
	return string(encoded)
}

var archiveDeliveryScript = redis.NewScript(gameArchiveLua + `
local meta, code = archive_meta(KEYS[1], KEYS[2])
if code ~= 0 or not meta or meta.timeline ~= ARGV[1] then return {0} end
local deadline = ''
if meta.state == 'ready' and meta.control == '' and redis.call('GET', KEYS[4]) == 'playing' then deadline = redis.call('GET', KEYS[3]) or '' end
return {1, redis.call('HGETALL', KEYS[1]), deadline}
`)

func (r *RedisGameStateRepo) archiveDelivery(ctx context.Context, roomID uint, timeline string) (*model.GameArchiveRuntime, *time.Time) {
	// The commit is known even if the post-commit read fails; never return an old timer.
	fallback := &model.GameArchiveRuntime{TimelineID: timeline, ArchiveState: "recovering"}
	keys := append(gameArchiveKeys(roomID)[:2], multiplayerDeadlineKey(roomID), runtimeStatusKey(roomID))
	values, err := archiveDeliveryScript.Run(ctx, r.client, keys, timeline).Slice()
	if err != nil || len(values) != 3 {
		return fallback, nil
	}
	memory, err := decodeArchiveMetadata(values[1])
	if err != nil {
		return fallback, nil
	}
	text, _ := redisString(values[2])
	if text == "" {
		return memory, nil
	}
	millis, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return memory, nil
	}
	deadline := time.UnixMilli(millis).UTC()
	return memory, &deadline
}

var retryArchiveScript = redis.NewScript(gameArchiveLua + `
local meta, code = archive_meta(KEYS[1], KEYS[2]); if code ~= 0 then return code end
if not meta or meta.timeline ~= ARGV[1] then return 91 end
if not archive_type(KEYS[4], 'zset') then return 92 end
if redis.call('HGET', KEYS[3], 'owner_token') ~= ARGV[4] or redis.call('HGET', KEYS[3], 'commit_id') ~= ARGV[2] or
   redis.call('HGET', KEYS[3], 'payload_hash') ~= ARGV[3] then return 3 end
if redis.call('HGET', KEYS[2], 'commit_id') ~= ARGV[2] or redis.call('HGET', KEYS[2], 'payload_hash') ~= ARGV[3] or
   redis.call('HGET', KEYS[2], 'position') ~= ARGV[5] then return 92 end
local attempts = tonumber(redis.call('HGET', KEYS[2], 'attempts') or '0')
if not attempts or attempts < 0 or attempts > 30 then return 92 end
redis.call('ZADD', KEYS[4], ARGV[7], ARGV[6])
redis.call('HSET', KEYS[2], 'attempts', math.min(attempts + 1, 30))
redis.call('DEL', KEYS[3])
return 1
`)

// Retry scheduling and lease release share the same ownership fence.
func (r *RedisGameStateRepo) RetryGameArchive(ctx context.Context, ack model.GameArchiveACK, nextRetry time.Time) error {
	if err := validateArchiveACK(ack); err != nil {
		return err
	}
	if nextRetry.IsZero() {
		return model.ErrInvalidMemoryData
	}
	code, err := retryArchiveScript.Run(ctx, r.client, gameArchiveKeys(ack.RoomID)[:4], ack.TimelineID, ack.CommitID, ack.PayloadHash,
		ack.OwnerToken, ack.Position, ack.RoomID, nextRetry.UTC().UnixMilli()).Int64()
	if err != nil {
		return fmt.Errorf("%w: schedule archive retry", ErrGameRuntimeUnavailable)
	}
	if issue := archiveError(code); issue != nil {
		return issue
	}
	if code != 1 {
		return ErrGameArchiveLeaseConflict
	}
	return nil
}

var blockArchiveScript = redis.NewScript(gameArchiveLua + `
local meta, code = archive_meta(KEYS[1], KEYS[2]); if code ~= 0 then return code end
if not meta or meta.timeline ~= ARGV[1] then return 91 end
if not archive_type(KEYS[4], 'zset') then return 92 end
if redis.call('HGET', KEYS[3], 'owner_token') ~= ARGV[4] or redis.call('HGET', KEYS[3], 'commit_id') ~= ARGV[2] or
   redis.call('HGET', KEYS[3], 'payload_hash') ~= ARGV[3] then return 3 end
if redis.call('HGET', KEYS[2], 'commit_id') ~= ARGV[2] or redis.call('HGET', KEYS[2], 'payload_hash') ~= ARGV[3] or
   redis.call('HGET', KEYS[2], 'position') ~= ARGV[5] then return 92 end
redis.call('HSET', KEYS[1], 'archive_state', 'blocked')
redis.call('ZREM', KEYS[4], ARGV[6])
redis.call('DEL', KEYS[3])
return 1
`)

// BlockGameArchive retains the exact pending body for investigation. A stale
// owner cannot quarantine a newer record, and blocked rooms do not starve polls.
func (r *RedisGameStateRepo) BlockGameArchive(ctx context.Context, ack model.GameArchiveACK) error {
	if err := validateArchiveACK(ack); err != nil {
		return err
	}
	code, err := blockArchiveScript.Run(ctx, r.client, gameArchiveKeys(ack.RoomID)[:4], ack.TimelineID, ack.CommitID, ack.PayloadHash,
		ack.OwnerToken, ack.Position, ack.RoomID).Int64()
	if err != nil {
		return ErrGameRuntimeUnavailable
	}
	if issue := archiveError(code); issue != nil {
		return issue
	}
	if code != 1 {
		return ErrGameArchiveLeaseConflict
	}
	return nil
}

var memoryAutoSavesScript = redis.NewScript(`
if redis.call('TYPE', KEYS[1]).ok ~= 'hash' and redis.call('TYPE', KEYS[1]).ok ~= 'none' then return redis.error_reply('invalid autosave type') end
return redis.call('HGETALL', KEYS[1])
`)

func (r *RedisGameStateRepo) ListPendingMemoryAutoSaves(ctx context.Context, roomID uint) ([]model.PendingMemoryAutoSave, error) {
	if roomID == 0 {
		return nil, model.ErrInvalidMemoryData
	}
	fields, err := memoryAutoSavesScript.Run(ctx, r.client, []string{gameArchiveKeys(roomID)[4]}).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("%w: list memory autosaves", ErrGameRuntimeUnavailable)
	}
	if len(fields)%2 != 0 {
		return nil, ErrGameArchiveCorrupt
	}
	result := make([]model.PendingMemoryAutoSave, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		parts := strings.Split(fields[i], ":")
		if len(parts) != 3 || !model.ValidMemoryUUID(parts[0]) || !json.Valid([]byte(fields[i+1])) {
			return nil, ErrGameArchiveCorrupt
		}
		position, posErr := strconv.ParseUint(parts[1], 10, 64)
		round, roundErr := strconv.Atoi(parts[2])
		if posErr != nil || roundErr != nil || position == 0 || position > maxArchiveRedisPosition || round < 1 {
			return nil, ErrGameArchiveCorrupt
		}
		result = append(result, model.PendingMemoryAutoSave{TimelineID: parts[0], Position: position, RoundNumber: round, Snapshot: []byte(fields[i+1])})
	}
	return result, nil
}

var ackMemoryAutoSaveScript = redis.NewScript(`
if (redis.call('TYPE', KEYS[1]).ok ~= 'none' and redis.call('TYPE', KEYS[1]).ok ~= 'hash') or
   (redis.call('TYPE', KEYS[2]).ok ~= 'none' and redis.call('TYPE', KEYS[2]).ok ~= 'zset') then return 92 end
local stored = redis.call('HGET', KEYS[1], ARGV[1])
if stored and stored ~= ARGV[2] then return 92 end
redis.call('HDEL', KEYS[1], ARGV[1])
if redis.call('HLEN', KEYS[1]) == 0 then redis.call('ZREM', KEYS[2], ARGV[3]) end
return 1
`)

func (r *RedisGameStateRepo) AcknowledgeMemoryAutoSave(ctx context.Context, roomID uint, pending model.PendingMemoryAutoSave) error {
	if roomID == 0 || !model.ValidMemoryUUID(pending.TimelineID) || pending.Position == 0 || pending.Position > maxArchiveRedisPosition || pending.RoundNumber < 1 || !json.Valid(pending.Snapshot) {
		return model.ErrInvalidMemoryData
	}
	keys := gameArchiveKeys(roomID)
	identity := fmt.Sprintf("%s:%d:%d", pending.TimelineID, pending.Position, pending.RoundNumber)
	code, err := ackMemoryAutoSaveScript.Run(ctx, r.client, []string{keys[4], keys[5]}, identity, string(pending.Snapshot), roomID).Int64()
	if err != nil {
		return fmt.Errorf("%w: ACK memory autosave", ErrGameRuntimeUnavailable)
	}
	if issue := archiveError(code); issue != nil {
		return issue
	}
	if code != 1 {
		return ErrGameArchiveCorrupt
	}
	return nil
}

func (r *RedisGameStateRepo) ListPendingMemoryAutoSaveRooms(ctx context.Context, now time.Time, limit int) ([]uint, error) {
	if now.IsZero() || limit < 1 || limit > 256 {
		return nil, model.ErrInvalidMemoryData
	}
	values, err := listArchiveRoomsScript.Run(ctx, r.client, []string{gameArchiveKeys(1)[5]}, now.UTC().UnixMilli(), limit).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("%w: list memory autosave rooms", ErrGameRuntimeUnavailable)
	}
	rooms := make([]uint, 0, len(values))
	for _, text := range values {
		id, err := strconv.ParseUint(text, 10, 64)
		if err != nil || id == 0 {
			return nil, ErrGameArchiveCorrupt
		}
		rooms = append(rooms, uint(id))
	}
	return rooms, nil
}
