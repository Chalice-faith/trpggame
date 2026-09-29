package repo

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"trpggame/internal/model"
)

//go:embed lua/game_memory_control.lua
var gameMemoryControlLua string
var gameMemoryControlScript = redis.NewScript(gameArchiveLua + gameMemoryControlLua)

type memoryRuntimeWrite struct {
	Index  int      `json:"index"`
	Kind   string   `json:"kind"`
	Values []string `json:"values"`
}

type memoryRuntimePlan struct {
	OperationID      string               `json:"operation_id"`
	SnapshotHash     string               `json:"snapshot_hash"`
	RoomID           string               `json:"room_id"`
	Mode             string               `json:"mode"`
	Kind             string               `json:"kind"`
	SourceTimeline   string               `json:"source_timeline"`
	SourceGeneration string               `json:"source_generation"`
	SourceRevision   string               `json:"source_revision"`
	FenceGeneration  string               `json:"fence_generation"`
	TimelineID       string               `json:"timeline_id"`
	Generation       string               `json:"generation"`
	Revision         string               `json:"revision"`
	Position         string               `json:"position"`
	TimeoutMS        int64                `json:"timeout_ms"`
	TTLMS            int64                `json:"ttl_ms"`
	StatusKey        int                  `json:"status_key"`
	GenerationKey    int                  `json:"generation_key"`
	TurnKey          int                  `json:"turn_key"`
	OrderKey         int                  `json:"order_key"`
	DeadlineKey      int                  `json:"deadline_key,omitempty"`
	LeaseKey         int                  `json:"lease_key,omitempty"`
	Clear            []int                `json:"clear"`
	Writes           []memoryRuntimeWrite `json:"writes"`
	Recovery         bool                 `json:"recovery"`
}

func (r *RedisGameStateRepo) memoryRuntimePlan(input model.MemoryRuntimeReplacement) ([]string, *memoryRuntimePlan, error) {
	if input.RoomID == 0 || input.OwnerID == 0 || (input.Mode != "solo" && input.Mode != "multiplayer") {
		return nil, nil, model.ErrInvalidMemoryData
	}
	keys := append(gameArchiveKeys(input.RoomID), fmt.Sprintf("game:archive:room:%d:control", input.RoomID), fmt.Sprintf("game:archive:room:%d:control_receipts", input.RoomID), multiplayerDeadlineQueueKey(), pendingMultiplayerAutoSaveRoomsKey())
	indices := make(map[string]int)
	for index, key := range keys {
		indices[key] = index + 1
	}
	p := &memoryRuntimePlan{OperationID: input.OperationID, SnapshotHash: input.SnapshotHash, RoomID: strconv.FormatUint(uint64(input.RoomID), 10), Mode: input.Mode, Kind: input.Kind,
		SourceTimeline: input.SourceTimeline, SourceGeneration: input.SourceGeneration, SourceRevision: strconv.FormatUint(input.SourceRevision, 10),
		TimelineID: input.TimelineID, Generation: input.Generation, Revision: strconv.FormatUint(input.Revision, 10), Position: strconv.FormatUint(input.Position, 10),
		TimeoutMS: input.TurnTimeoutMS, TTLMS: r.ttl.Milliseconds(), Clear: []int{}, Writes: []memoryRuntimeWrite{}, Recovery: input.Recovery}
	if model.ValidMemoryUUID(input.OperationID) {
		id, _ := uuid.Parse(input.OperationID)
		p.FenceGeneration = uuid.NewSHA1(id, []byte("fence-generation")).String()
	}
	add := func(key string) int {
		if n, ok := indices[key]; ok {
			return n
		}
		keys = append(keys, key)
		n := len(keys)
		indices[key] = n
		p.Clear = append(p.Clear, n)
		return n
	}
	write := func(key, kind string, values ...string) {
		if values == nil {
			values = []string{}
		}
		p.Writes = append(p.Writes, memoryRuntimeWrite{add(key), kind, values})
	}
	p.StatusKey = add(runtimeStatusKey(input.RoomID))
	p.GenerationKey = add(runtimeGenerationKey(input.RoomID))
	p.TurnKey = add(runtimeTurnKey(input.RoomID))
	p.OrderKey = add(fmt.Sprintf("room:%d:turn_order", input.RoomID))
	add(actionResultsKey(input.RoomID))
	add(fmt.Sprintf("room:%d:summary", input.RoomID))
	add(fmt.Sprintf("room:%d:rounds", input.RoomID))
	if input.Mode == "solo" {
		for _, key := range soloRuntimeCleanupKeys(input.RoomID, input.OwnerID) {
			add(key)
		}
	} else {
		for _, key := range multiplayerCommonRuntimeKeys(input.RoomID) {
			add(key)
		}
		p.DeadlineKey = add(multiplayerDeadlineKey(input.RoomID))
		p.LeaseKey = add(multiplayerActionLeaseKey(input.RoomID))
		add(pendingMultiplayerAutoSavesKey(input.RoomID))
		for _, user := range input.Roster {
			add(runtimePlayerKey(input.RoomID, user))
			add(itemStateKey(input.RoomID, user))
			add(buffStateKey(input.RoomID, user))
		}
	}
	if input.Solo == nil && input.Multiplayer == nil {
		return keys, p, nil
	}
	if (input.Solo == nil) == (input.Multiplayer == nil) || !model.ValidMemoryUUID(input.Generation) || !model.ValidMemoryUUID(input.TimelineID) ||
		input.Position > maxArchiveRedisPosition || input.Revision == 0 || input.Revision > maxArchiveRedisPosition {
		return nil, nil, model.ErrInvalidMemoryData
	}
	if input.Mode == "solo" {
		if input.Solo == nil || input.Solo.RoomID != input.RoomID || input.Solo.UserID != input.OwnerID || input.TurnTimeoutMS != 0 {
			return nil, nil, model.ErrInvalidMemoryData
		}
		copy := *input.Solo
		copy.Memory = nil
		copy.Status = model.RoomStatusPaused
		snapshot, err := normalizeSoloRuntimeSnapshot(&copy)
		if err != nil {
			return nil, nil, err
		}
		write(runtimeTurnKey(input.RoomID), "string", strconv.Itoa(snapshot.Turn))
		write(fmt.Sprintf("room:%d:summary", input.RoomID), "string", snapshot.Summary)
		write(runtimeGenerationKey(input.RoomID), "string", input.Generation)
		write(fmt.Sprintf("room:%d:turn_order", input.RoomID), "list", strconv.FormatUint(uint64(input.OwnerID), 10))
		var state []string
		for field, value := range snapshot.PlayerState {
			state = append(state, field, value)
		}
		write(runtimePlayerKey(input.RoomID, input.OwnerID), "hash", state...)
		var items, buffs, messages []string
		for _, item := range snapshot.Items {
			items = append(items, fmt.Sprintf("%s|%d|%s", item.Name, item.Quantity, item.Description))
		}
		for _, buff := range snapshot.Buffs {
			buffs = append(buffs, buff.Name, strconv.Itoa(buff.Duration))
		}
		for _, message := range snapshot.RecentMessages {
			raw, _ := json.Marshal(message)
			messages = append(messages, string(raw))
		}
		write(itemStateKey(input.RoomID, input.OwnerID), "set", items...)
		write(buffStateKey(input.RoomID, input.OwnerID), "hash", buffs...)
		write(fmt.Sprintf("room:%d:rounds", input.RoomID), "list", messages...)
	} else {
		if input.Multiplayer == nil || input.Multiplayer.RoomID != input.RoomID || input.TurnTimeoutMS < 1000 || input.TurnTimeoutMS > 3600000 {
			return nil, nil, model.ErrInvalidMemoryData
		}
		copy := *input.Multiplayer
		copy.Memory = nil
		copy.Status = model.RoomStatusPaused
		copy.Generation = input.Generation
		copy.DeadlineAt = nil
		copy.ActionLease = nil
		if err := ValidateMemoryMultiplayerSnapshot(&copy); err != nil {
			return nil, nil, err
		}
		write(multiplayerRuntimeVersionKey(input.RoomID), "string", "2")
		write(runtimeTurnKey(input.RoomID), "string", strconv.Itoa(copy.CurrentTurn))
		write(runtimeGenerationKey(input.RoomID), "string", input.Generation)
		write(fmt.Sprintf("room:%d:summary", input.RoomID), "string", copy.SummaryMemory)
		write(multiplayerDeadlineKey(input.RoomID), "string", "")
		var order, messages []string
		for _, user := range copy.TurnOrder {
			order = append(order, strconv.FormatUint(uint64(user), 10))
		}
		write(fmt.Sprintf("room:%d:turn_order", input.RoomID), "list", order...)
		write(multiplayerRuntimePlayersKey(input.RoomID), "list", order...)
		// Multiplayer Redis rounds are newest first; V2 snapshots are chronological.
		for i := len(copy.RecentMessages) - 1; i >= 0; i-- {
			raw, _ := json.Marshal(copy.RecentMessages[i])
			messages = append(messages, string(raw))
		}
		write(fmt.Sprintf("room:%d:rounds", input.RoomID), "list", messages...)
		for _, player := range copy.Players {
			var state, items, buffs []string
			state = append(state, "character_id", strconv.FormatUint(uint64(player.CharacterID), 10))
			for field, value := range player.PlayerState {
				state = append(state, field, value)
			}
			for _, item := range player.Items {
				raw, _ := json.Marshal(item)
				items = append(items, string(raw))
			}
			for _, buff := range player.Buffs {
				buffs = append(buffs, buff.Name, strconv.Itoa(buff.Duration))
			}
			write(runtimePlayerKey(input.RoomID, player.UserID), "hash", state...)
			write(itemStateKey(input.RoomID, player.UserID), "set", items...)
			write(buffStateKey(input.RoomID, player.UserID), "hash", buffs...)
		}
	}
	return keys, p, nil
}

func (r *RedisGameStateRepo) runMemoryControl(ctx context.Context, action string, input model.MemoryRuntimeReplacement) ([]any, error) {
	if !model.ValidMemoryUUID(input.OperationID) || !model.ValidMemoryHash(input.SnapshotHash) || (input.Kind != "start" && input.Kind != "load" && input.Kind != "end") {
		return nil, model.ErrInvalidMemoryData
	}
	keys, plan, err := r.memoryRuntimePlan(input)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 2*model.GameMemoryMaxPayloadBytes {
		return nil, model.ErrInvalidMemoryData
	}
	var record, cached string
	if input.Opening != nil {
		copy := *input.Opening
		if err := copy.Seal(); err != nil {
			return nil, err
		}
		if copy.RoomID != input.RoomID || copy.TimelineID != input.TimelineID || copy.SourceGeneration != input.Generation || copy.SourceRevision != input.Revision || copy.Kind != "opening" {
			return nil, model.ErrInvalidMemoryData
		}
		body, _ := json.Marshal(copy)
		record = string(body)
		response, _ := json.Marshal(cachedActionResult{Fingerprint: copy.Fingerprint, Response: copy.Payload})
		cached = string(response)
	}
	values, err := gameMemoryControlScript.Run(ctx, r.client, keys, action, string(raw), record, cached).Slice()
	if err != nil {
		return nil, fmt.Errorf("%w: memory control %s", ErrGameRuntimeUnavailable, action)
	}
	if len(values) < 1 {
		return nil, ErrGameArchiveCorrupt
	}
	code, ok := redisInt64(values[0])
	if !ok {
		return nil, ErrGameArchiveCorrupt
	}
	if issue := archiveError(code); issue != nil {
		return nil, issue
	}
	if code != 1 && code != 2 {
		return nil, ErrGameArchiveNotReady
	}
	return values, nil
}

func (r *RedisGameStateRepo) InitializeMemoryStart(ctx context.Context, input model.MemoryRuntimeReplacement) error {
	_, err := r.runMemoryControl(ctx, "initialize", input)
	return err
}
func (r *RedisGameStateRepo) AbortMemoryStart(ctx context.Context, input model.MemoryRuntimeReplacement) error {
	_, err := r.runMemoryControl(ctx, "abort_start", input)
	return err
}
func (r *RedisGameStateRepo) FenceMemoryOperation(ctx context.Context, input model.MemoryRuntimeReplacement) error {
	_, err := r.runMemoryControl(ctx, "fence", input)
	return err
}
func (r *RedisGameStateRepo) ApplyMemoryOperation(ctx context.Context, input model.MemoryRuntimeReplacement) error {
	_, err := r.runMemoryControl(ctx, "apply", input)
	return err
}
func (r *RedisGameStateRepo) FinishMemoryOperation(ctx context.Context, input model.MemoryRuntimeReplacement) error {
	_, err := r.runMemoryControl(ctx, "finish", input)
	return err
}

func (r *RedisGameStateRepo) InspectMemoryOperation(ctx context.Context, input model.MemoryRuntimeReplacement) (*model.MemoryControlProgress, error) {
	values, err := r.runMemoryControl(ctx, "inspect", input)
	if err != nil {
		return nil, err
	}
	if len(values) != 2 {
		return nil, ErrGameArchiveCorrupt
	}
	text, ok := redisString(values[1])
	if !ok {
		return nil, ErrGameArchiveCorrupt
	}
	var result model.MemoryControlProgress
	if json.Unmarshal([]byte(text), &result) != nil || result.SnapshotHash != input.SnapshotHash || (result.Phase != "fenced" && result.Phase != "applied" && result.Phase != "finished") || result.SourceTurn < 0 || result.SourceRound < 0 {
		return nil, ErrGameArchiveCorrupt
	}
	return &result, nil
}

func (r *RedisGameStateRepo) GetMemoryControlSource(ctx context.Context, roomID, ownerID uint, mode string) (*model.MemoryControlSource, error) {
	keys, p, err := r.memoryRuntimePlan(model.MemoryRuntimeReplacement{RoomID: roomID, OwnerID: ownerID, Mode: mode})
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(p)
	values, err := gameMemoryControlScript.Run(ctx, r.client, keys, "source", string(raw)).Slice()
	if err != nil {
		return nil, ErrGameRuntimeUnavailable
	}
	if len(values) == 1 {
		code, _ := redisInt64(values[0])
		if code == 0 {
			return nil, nil
		}
		if issue := archiveError(code); issue != nil {
			return nil, issue
		}
	}
	if len(values) != 6 {
		return nil, ErrGameArchiveCorrupt
	}
	memory, err := decodeArchiveMetadata(values[1])
	if err != nil {
		return nil, err
	}
	gen, _ := redisString(values[2])
	status, _ := redisString(values[3])
	turnText, _ := redisString(values[4])
	roundText, _ := redisString(values[5])
	turn, e1 := strconv.Atoi(turnText)
	round, e2 := strconv.Atoi(roundText)
	if !model.ValidMemoryUUID(gen) || e1 != nil || e2 != nil || turn < 0 || round < 0 || (status != "playing" && status != "paused" && status != "provisional") {
		return nil, ErrGameArchiveCorrupt
	}
	return &model.MemoryControlSource{Memory: memory, Generation: gen, Status: model.RoomStatus(status), Turn: turn, Round: round}, nil
}
