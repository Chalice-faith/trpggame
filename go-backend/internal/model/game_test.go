package model

import (
	"encoding/json"
	"testing"
)

func TestRoomStatusValid(t *testing.T) {
	validStatuses := []RoomStatus{
		RoomStatusWaiting,
		RoomStatusPlaying,
		RoomStatusPaused,
		RoomStatusEnded,
	}
	for _, status := range validStatuses {
		if !status.Valid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if RoomStatus("finished").Valid() || RoomStatus("").Valid() {
		t.Fatal("unknown room status must be invalid")
	}
}

func TestGameModelsUseMigrationTableNames(t *testing.T) {
	if got := (GameRoom{}).TableName(); got != "game_rooms" {
		t.Fatalf("GameRoom table = %q, want game_rooms", got)
	}
	if got := (RoomPlayer{}).TableName(); got != "room_players" {
		t.Fatalf("RoomPlayer table = %q, want room_players", got)
	}
	if got := (GameSave{}).TableName(); got != "game_saves" {
		t.Fatalf("GameSave table = %q, want game_saves", got)
	}
}

func TestGameRoomJSONContractEmbedsTurnOrder(t *testing.T) {
	room := GameRoom{
		ID:          7,
		Name:        "古宅独奏",
		ScriptID:    11,
		OwnerID:     13,
		Status:      RoomStatusWaiting,
		MaxPlayers:  1,
		CurrentTurn: 0,
		RoundNumber: 0,
		TurnOrder:   json.RawMessage(`[13]`),
		IsSolo:      true,
	}

	encoded, err := json.Marshal(room)
	if err != nil {
		t.Fatalf("marshal GameRoom: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode GameRoom JSON: %v", err)
	}
	order, ok := payload["turn_order"].([]any)
	if !ok || len(order) != 1 || order[0] != float64(13) {
		t.Fatalf("turn_order = %#v, want [13]", payload["turn_order"])
	}
	if payload["owner_id"] != float64(13) || payload["round_number"] != float64(0) {
		t.Fatalf("unexpected room contract: %#v", payload)
	}
}

func TestRoomPlayerCharacterIsNullable(t *testing.T) {
	encoded, err := json.Marshal(RoomPlayer{RoomID: 1, UserID: 2})
	if err != nil {
		t.Fatalf("marshal RoomPlayer: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode RoomPlayer JSON: %v", err)
	}
	if _, exists := payload["character_id"]; exists {
		t.Fatal("nil character_id should be omitted")
	}
}
