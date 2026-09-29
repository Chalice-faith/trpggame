package service

import (
	"encoding/json"
	"fmt"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type MemorySaveMetadata struct {
	TimelineID      string  `json:"timeline_id"`
	Position        *uint64 `json:"position"`
	HistoryComplete *bool   `json:"history_complete"`
	SummarySource   string  `json:"summary_source"`
}

type MemorySaveEnvelope struct {
	SchemaVersion int                `json:"schema_version"`
	Mode          string             `json:"mode"`
	Runtime       json.RawMessage    `json:"runtime"`
	Memory        MemorySaveMetadata `json:"memory"`
}

type decodedMemorySave struct {
	Envelope    MemorySaveEnvelope
	Solo        *model.SoloRuntimeSnapshot
	Multiplayer *model.MultiplayerRuntimeSnapshot
	Legacy      bool
}

// V3 deliberately stores summary/messages only in the existing save columns.
// Timers, action leases, generation and mutable archive delivery state are not saves.
func encodeMemorySave(roomID uint, name string, timeline string, position uint64, complete bool, source string,
	solo *model.SoloRuntimeSnapshot, multi *model.MultiplayerRuntimeSnapshot, auto bool) (*model.GameSave, error) {
	if roomID == 0 || (solo != nil && solo.RoomID != roomID) || (multi != nil && multi.RoomID != roomID) || (solo == nil) == (multi == nil) || !model.ValidMemoryUUID(timeline) || position > 9007199254740990 ||
		(source != "opening" && source != "runtime" && source != "legacy") {
		return nil, ErrGameSaveCorrupt
	}
	envelope := MemorySaveEnvelope{SchemaVersion: 3, Memory: MemorySaveMetadata{timeline, &position, &complete, source}}
	save := &model.GameSave{RoomID: roomID, SaveName: name, TimelineID: &timeline, MemoryPosition: &position, IsAuto: auto}
	if solo != nil {
		copy := *solo
		copy.Memory = nil
		copy.Status = model.RoomStatusPaused
		normalized, err := repo.NormalizeSoloRuntimeSnapshot(&copy)
		if err != nil {
			return nil, ErrGameSaveCorrupt
		}
		envelope.Mode = "solo"
		envelope.Runtime, _ = json.Marshal(normalized)
		save.RoundNumber = normalized.Turn
		save.SummaryMemory = normalized.Summary
		save.RecentMessages, _ = json.Marshal(normalized.RecentMessages)
	} else {
		copy := *multi
		copy.Memory = nil
		copy.Status = model.RoomStatusPaused
		copy.ActionLease = nil
		copy.DeadlineAt = nil
		if err := repo.ValidateMemoryMultiplayerSnapshot(&copy); err != nil {
			return nil, ErrGameSaveCorrupt
		}
		envelope.Mode = "multiplayer"
		runtime, _ := json.Marshal(copy)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(runtime, &fields)
		for _, key := range []string{"summary_memory", "recent_messages", "generation", "deadline_at"} {
			delete(fields, key)
		}
		envelope.Runtime, _ = json.Marshal(fields)
		save.RoundNumber = copy.RoundNumber
		save.SummaryMemory = copy.SummaryMemory
		save.RecentMessages, _ = json.Marshal(copy.RecentMessages)
	}
	save.RedisSnapshot, _ = json.Marshal(envelope)
	if _, err := model.CanonicalMemoryJSON(save.RedisSnapshot); err != nil {
		return nil, ErrGameSaveCorrupt
	}
	return save, nil
}

func decodeMemorySave(save *model.GameSave, room *model.GameRoom, owner uint) (*decodedMemorySave, error) {
	if save == nil || room == nil || save.RoomID != room.ID || owner != room.OwnerID || save.ID == 0 {
		return nil, ErrGameSaveCorrupt
	}
	var discriminator struct {
		SchemaVersion int `json:"schema_version"`
		Version       int `json:"version"`
	}
	if json.Unmarshal(save.RedisSnapshot, &discriminator) != nil {
		return nil, ErrGameSaveCorrupt
	}
	decoded := &decodedMemorySave{}
	if discriminator.SchemaVersion == 0 {
		if save.TimelineID != nil || save.MemoryPosition != nil {
			return nil, ErrGameSaveCorrupt
		}
		decoded.Legacy = true
		var err error
		if room.IsSolo {
			decoded.Solo, err = decodeGameSaveSnapshot(save, room.ID, owner, save.ID)
		} else {
			decoded.Multiplayer, err = decodeMultiplayerSaveSnapshot(save, room.ID, save.ID)
		}
		return decoded, err
	}
	if decodeStrictJSON(save.RedisSnapshot, &decoded.Envelope) != nil {
		return nil, ErrGameSaveCorrupt
	}
	e := &decoded.Envelope
	if e.SchemaVersion != 3 || save.TimelineID == nil || save.MemoryPosition == nil || !model.ValidMemoryUUID(e.Memory.TimelineID) ||
		e.Memory.Position == nil || e.Memory.HistoryComplete == nil || e.Memory.TimelineID != *save.TimelineID || *e.Memory.Position != *save.MemoryPosition ||
		*e.Memory.Position > 9007199254740990 || (e.Memory.SummarySource != "opening" && e.Memory.SummarySource != "runtime" && e.Memory.SummarySource != "legacy") ||
		(room.IsSolo && e.Mode != "solo") || (!room.IsSolo && e.Mode != "multiplayer") {
		return nil, ErrGameSaveCorrupt
	}
	var runtimeFields map[string]json.RawMessage
	if json.Unmarshal(e.Runtime, &runtimeFields) != nil || runtimeFields == nil {
		return nil, ErrGameSaveCorrupt
	}
	required := []string{"version", "status", "turn_order"}
	if room.IsSolo {
		required = append(required, "turn", "player_state", "items", "buffs")
	} else {
		required = append(required, "room_id", "current_turn", "round_number", "current_actor_id", "players")
	}
	for _, key := range required {
		value, exists := runtimeFields[key]
		if !exists || string(value) == "null" {
			return nil, ErrGameSaveCorrupt
		}
	}
	for key := range runtimeFields {
		allowed := false
		for _, requiredKey := range required {
			if key == requiredKey {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, ErrGameSaveCorrupt
		}
	}
	var messages []model.RuntimeMessage
	if decodeStrictJSON(save.RecentMessages, &messages) != nil || messages == nil {
		return nil, ErrGameSaveCorrupt
	}
	if room.IsSolo {
		var snapshot model.SoloRuntimeSnapshot
		if decodeStrictJSON(e.Runtime, &snapshot) != nil || snapshot.Memory != nil || snapshot.Status != model.RoomStatusPaused || snapshot.Turn != save.RoundNumber || snapshot.Items == nil || snapshot.Buffs == nil {
			return nil, ErrGameSaveCorrupt
		}
		snapshot.RoomID = room.ID
		snapshot.UserID = owner
		snapshot.Summary = save.SummaryMemory
		snapshot.RecentMessages = messages
		var err error
		decoded.Solo, err = repo.NormalizeSoloRuntimeSnapshot(&snapshot)
		if err != nil {
			return nil, ErrGameSaveCorrupt
		}
	} else {
		// Reject duplicated columns and persisted delivery metadata even when empty.
		var fields map[string]json.RawMessage
		if json.Unmarshal(e.Runtime, &fields) != nil {
			return nil, ErrGameSaveCorrupt
		}
		for _, key := range []string{"memory", "generation", "deadline_at", "summary_memory", "recent_messages", "action_lease"} {
			if _, exists := fields[key]; exists {
				return nil, ErrGameSaveCorrupt
			}
		}
		var snapshot model.MultiplayerRuntimeSnapshot
		if decodeStrictJSON(e.Runtime, &snapshot) != nil || snapshot.RoomID != room.ID || snapshot.Status != model.RoomStatusPaused || snapshot.RoundNumber != save.RoundNumber {
			return nil, ErrGameSaveCorrupt
		}
		snapshot.Generation = "11111111-1111-4111-8111-111111111111" // validation placeholder; never restored
		snapshot.SummaryMemory = save.SummaryMemory
		snapshot.RecentMessages = messages
		if err := repo.ValidateMemoryMultiplayerSnapshot(&snapshot); err != nil {
			return nil, ErrGameSaveCorrupt
		}
		decoded.Multiplayer = &snapshot
	}
	return decoded, nil
}

func memoryBoundarySave(pending model.PendingMemoryAutoSave, room *model.GameRoom, complete bool) (*model.GameSave, error) {
	if room == nil || !model.ValidMemoryUUID(pending.TimelineID) || pending.Position == 0 {
		return nil, ErrGameSaveCorrupt
	}
	if room.IsSolo {
		var boundary struct {
			model.SoloRuntimeSnapshot
			RoomID   uint                   `json:"room_id"`
			UserID   uint                   `json:"user_id"`
			Summary  string                 `json:"summary"`
			Messages []model.RuntimeMessage `json:"recent_messages"`
		}
		if decodeStrictJSON(pending.Snapshot, &boundary) != nil || boundary.RoomID != room.ID || boundary.UserID != room.OwnerID ||
			boundary.Memory == nil || boundary.Memory.TimelineID != pending.TimelineID || boundary.Memory.HeadPosition != pending.Position ||
			boundary.Turn != pending.RoundNumber || boundary.Turn <= 0 || boundary.Turn%10 != 0 {
			return nil, ErrGameSaveCorrupt
		}
		boundary.SoloRuntimeSnapshot.RoomID = room.ID
		boundary.SoloRuntimeSnapshot.UserID = room.OwnerID
		boundary.SoloRuntimeSnapshot.Summary = boundary.Summary
		boundary.SoloRuntimeSnapshot.RecentMessages = boundary.Messages
		return encodeMemorySave(room.ID, fmt.Sprintf("自动存档-%d", pending.RoundNumber), pending.TimelineID, pending.Position, complete, "runtime", &boundary.SoloRuntimeSnapshot, nil, true)
	}
	var boundary model.MultiplayerRuntimeSnapshot
	if decodeStrictJSON(pending.Snapshot, &boundary) != nil || boundary.RoomID != room.ID || boundary.Memory == nil ||
		boundary.Memory.TimelineID != pending.TimelineID || boundary.Memory.HeadPosition != pending.Position ||
		boundary.RoundNumber != pending.RoundNumber || boundary.RoundNumber <= 0 || boundary.RoundNumber%5 != 0 {
		return nil, ErrGameSaveCorrupt
	}
	return encodeMemorySave(room.ID, fmt.Sprintf("自动存档-%d", pending.RoundNumber), pending.TimelineID, pending.Position, complete, "runtime", nil, &boundary, true)
}
