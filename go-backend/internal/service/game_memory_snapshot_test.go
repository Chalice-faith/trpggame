package service

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
	"trpggame/internal/model"
)

func memorySnapshotFixture(mode string, room uint) (*model.SoloRuntimeSnapshot, *model.MultiplayerRuntimeSnapshot) {
	messages := []model.RuntimeMessage{{Role: "assistant", Content: "opening"}, {Role: "user", Content: "look"}, {Role: "assistant", Content: "found key"}}
	if mode == "solo" {
		return &model.SoloRuntimeSnapshot{Version: 1, RoomID: room, UserID: 7, Status: model.RoomStatusPaused, Turn: 10, TurnOrder: []uint{7}, PlayerState: map[string]string{"hp": "8"}, Items: []model.RuntimeItem{}, Buffs: []model.RuntimeBuff{}, Summary: "summary", RecentMessages: messages}, nil
	}
	return nil, &model.MultiplayerRuntimeSnapshot{Version: 2, RoomID: room, Generation: uuid.NewString(), Status: model.RoomStatusPaused, CurrentTurn: 10, RoundNumber: 5, CurrentActorID: 7,
		TurnOrder: []uint{7, 8}, Players: []model.MultiplayerRuntimePlayer{
			{UserID: 7, CharacterID: 101, PlayerState: map[string]string{"hp": "8"}, Items: []model.RuntimeItem{{Name: "key|token", Quantity: 1}}, Buffs: []model.RuntimeBuff{}},
			{UserID: 8, CharacterID: 102, PlayerState: map[string]string{"hp": "10"}, Items: []model.RuntimeItem{}, Buffs: []model.RuntimeBuff{}}}, SummaryMemory: "summary", RecentMessages: messages}
}

func TestMemorySaveV3RoundTripAndStrictness(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			solo, multi := memorySnapshotFixture(mode, 41)
			save, err := encodeMemorySave(41, "save", uuid.NewString(), 12, true, "runtime", solo, multi, false)
			if err != nil {
				t.Fatal(err)
			}
			save.ID = 1
			room := &model.GameRoom{ID: 41, OwnerID: 7, IsSolo: mode == "solo"}
			decoded, err := decodeMemorySave(save, room, 7)
			if err != nil || decoded.Legacy {
				t.Fatalf("decode: %#v %v", decoded, err)
			}
			if strings.Contains(string(save.RedisSnapshot), "summary_memory") || strings.Contains(string(save.RedisSnapshot), "recent_messages") || strings.Contains(string(save.RedisSnapshot), "generation") {
				t.Fatal("duplicated delivery fields")
			}
			if solo != nil && (decoded.Solo.Turn != 10 || decoded.Solo.Summary != "summary" || len(decoded.Solo.RecentMessages) != 3) {
				t.Fatal("solo data lost")
			}
			if multi != nil && (decoded.Multiplayer.CurrentTurn != 10 || decoded.Multiplayer.SummaryMemory != "summary" || decoded.Multiplayer.Players[0].Items[0].Name != "key|token") {
				t.Fatal("multiplayer data lost")
			}
			for _, change := range []string{"unknown", "position", "mode", "metadata", "version", "round", "null_messages", "runtime_unknown", "runtime_memory", "missing_runtime_field"} {
				t.Run(change, func(t *testing.T) {
					copy := *save
					var body map[string]any
					_ = json.Unmarshal(copy.RedisSnapshot, &body)
					switch change {
					case "unknown":
						body["unexpected"] = true
					case "position":
						body["memory"].(map[string]any)["position"] = 999
					case "mode":
						body["mode"] = "other"
					case "metadata":
						copy.TimelineID = nil
					case "version":
						body["schema_version"] = 4
					case "round":
						copy.RoundNumber++
					case "null_messages":
						copy.RecentMessages = []byte("null")
					case "runtime_unknown":
						body["runtime"].(map[string]any)["unexpected"] = true
					case "runtime_memory":
						body["runtime"].(map[string]any)["memory"] = nil
					case "missing_runtime_field":
						delete(body["runtime"].(map[string]any), "status")
					}
					copy.RedisSnapshot, _ = json.Marshal(body)
					if _, err := decodeMemorySave(&copy, room, 7); err == nil {
						t.Fatal("corrupt save accepted")
					}
				})
			}
		})
	}
}
