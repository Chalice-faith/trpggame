package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/model"
)

type redisMultiplayerAutoSaveReader interface {
	ZRange(context.Context, string, int64, int64) *redis.StringSliceCmd
	HGetAll(context.Context, string) *redis.MapStringStringCmd
}

var acknowledgeMultiplayerAutoSaveScript = redis.NewScript(`
local encoded = redis.call('HGET', KEYS[1], ARGV[1])
if not encoded then return 1 end
local ok, snapshot = pcall(cjson.decode, encoded)
if not ok or type(snapshot) ~= 'table' or snapshot.generation ~= ARGV[2] then return 0 end
redis.call('HDEL', KEYS[1], ARGV[1])
if redis.call('HLEN', KEYS[1]) == 0 then redis.call('ZREM', KEYS[2], ARGV[3]) end
return 1
`)

// ListPendingMultiplayerAutoSaveRooms returns the rooms whose atomic turn commits left saves to persist.
func (r *RedisGameStateRepo) ListPendingMultiplayerAutoSaveRooms(ctx context.Context, limit int) ([]uint, error) {
	if limit <= 0 || limit > 256 {
		return nil, ErrInvalidGameRuntimeState
	}
	reader, ok := r.client.(redisMultiplayerAutoSaveReader)
	if !ok {
		return nil, ErrGameRuntimeUnavailable
	}
	roomIDs, err := reader.ZRange(ctx, pendingMultiplayerAutoSaveRoomsKey(), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("%w: list pending multiplayer auto-save rooms: %v", ErrGameRuntimeUnavailable, err)
	}
	result := make([]uint, 0, len(roomIDs))
	for _, raw := range roomIDs {
		roomID, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil || roomID == 0 {
			return nil, ErrGameRuntimeUnavailable
		}
		result = append(result, uint(roomID))
	}
	return result, nil
}

// ListPendingMultiplayerAutoSaves reads every unpersisted round snapshot for one room.
func (r *RedisGameStateRepo) ListPendingMultiplayerAutoSaves(ctx context.Context, roomID uint) ([]model.PendingMultiplayerAutoSave, error) {
	if roomID == 0 {
		return nil, ErrInvalidGameRuntimeState
	}
	reader, ok := r.client.(redisMultiplayerAutoSaveReader)
	if !ok {
		return nil, ErrGameRuntimeUnavailable
	}
	values, err := reader.HGetAll(ctx, pendingMultiplayerAutoSavesKey(roomID)).Result()
	if err != nil {
		return nil, fmt.Errorf("%w: list pending multiplayer auto-saves: %v", ErrGameRuntimeUnavailable, err)
	}
	rounds := make([]int, 0, len(values))
	for rawRound := range values {
		round, parseErr := strconv.Atoi(rawRound)
		if parseErr != nil || round <= 0 || round%5 != 0 {
			return nil, ErrGameRuntimeUnavailable
		}
		rounds = append(rounds, round)
	}
	sort.Ints(rounds)
	result := make([]model.PendingMultiplayerAutoSave, 0, len(rounds))
	for _, round := range rounds {
		var snapshot model.MultiplayerRuntimeSnapshot
		if strictDecodeMultiplayerSnapshot([]byte(values[strconv.Itoa(round)]), &snapshot) != nil ||
			snapshot.RoomID != roomID || snapshot.RoundNumber != round || validateMultiplayerSnapshot(&snapshot, model.RoomStatusPlaying) != nil {
			return nil, ErrGameRuntimeUnavailable
		}
		result = append(result, model.PendingMultiplayerAutoSave{Generation: snapshot.Generation, Snapshot: &snapshot})
	}
	return result, nil
}

// AcknowledgeMultiplayerAutoSave removes a pending snapshot only if it is still the same generation.
func (r *RedisGameStateRepo) AcknowledgeMultiplayerAutoSave(ctx context.Context, roomID uint, round int, generation string) error {
	if roomID == 0 || round <= 0 || round%5 != 0 || uuid.Validate(generation) != nil {
		return ErrInvalidGameRuntimeState
	}
	acknowledged, err := acknowledgeMultiplayerAutoSaveScript.Run(ctx, r.client, []string{
		pendingMultiplayerAutoSavesKey(roomID), pendingMultiplayerAutoSaveRoomsKey(),
	}, round, generation, roomID).Int64()
	if err != nil {
		return fmt.Errorf("%w: acknowledge multiplayer auto-save: %v", ErrGameRuntimeUnavailable, err)
	}
	if acknowledged != 1 {
		return ErrMultiplayerRuntimeConflict
	}
	return nil
}

func strictDecodeMultiplayerSnapshot(encoded []byte, snapshot *model.MultiplayerRuntimeSnapshot) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(snapshot); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing multiplayer snapshot data")
	}
	return nil
}
