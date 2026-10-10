//go:build integration

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"trpggame/internal/ai_client"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

// Exercise the business entry points, rather than committing fixture records
// directly to Redis. Only model inference is stubbed; DB/runtime/archive/save
// implementations are real and this test never uses a provider credential.
func TestGameMemoryLifecycleIntegrationServiceActions(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		for _, streaming := range []bool{false, true} {
			name := mode + "/rest"
			if streaming {
				name = mode + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f, lifecycle, game := memoryLifecycleFixture(t, mode)
				game.ConfigureMultiplayer(&multiplayerTurnEventRecorder{})
				resetMemoryStartFixture(t, f)
				lifecycle.ConfigureRoomStarter(repo.NewRoomRepo(f.db))
				solo, multi := memorySnapshotFixture(mode, f.room.ID)
				if solo != nil {
					solo.Turn = 0
				} else {
					multi.CurrentTurn, multi.RoundNumber, multi.CurrentActorID = 0, 0, 7
				}
				if _, err := lifecycle.Start(ctx, f.room, solo, multi); err != nil {
					t.Fatal(err)
				}
				if mode == "multiplayer" {
					if _, err := f.runtime.GetMultiplayerRoom(ctx, f.room.ID); err != nil {
						t.Fatal("multiplayer fresh-start runtime", err)
					}
				}
				ai := &fakeGameInferenceClient{actionResponse: &ai_client.GameActionResponse{Narrative: "A distinct committed observation",
					StatusChanges: effectChanges(effectCall("trigger_event", map[string]any{"event_name": "Found journal", "description": "The journal is in the study"}))}}
				game.aiClient = ai
				game.ConfigureInferenceMemory(f.store)
				var last *SubmitGameActionRequest
				for turn := 0; turn < 11; turn++ {
					source, err := f.runtime.GetMemoryControlSource(ctx, f.room.ID, 7, mode)
					if err != nil {
						t.Fatal(err)
					}
					actor := uint(7)
					if mode == "multiplayer" && turn%2 == 1 {
						actor = 8
					}
					last = &SubmitGameActionRequest{UserID: actor, RoomID: f.room.ID, ExpectedTurn: turn,
						ExpectedTimelineID: source.Memory.TimelineID, ExpectedGeneration: source.Generation,
						RequestID: uuid.NewString(), Action: "inspect the journal"}
					var result *SubmitGameActionResult
					if streaming {
						result, err = game.SubmitActionStream(ctx, last, func(GameActionStreamEvent) {})
					} else {
						result, err = game.SubmitAction(ctx, last)
					}
					if err != nil || result == nil || result.CurrentTurn != turn+1 || result.Memory == nil || result.Memory.DurablePosition != uint64(turn+2) || result.Generation != source.Generation {
						t.Fatalf("business action %d: result=%#v err=%v", turn, result, err)
					}
					if ai.actionRequest == nil || ai.actionRequest.MemoryContext == nil || ai.actionRequest.MemoryContext.ThroughPosition != uint64(turn+1) {
						t.Fatal("missing authoritative inference history")
					}
				}
				ai.actionRequest = nil
				replayed, err := game.SubmitAction(ctx, last)
				if err != nil || !replayed.Duplicate || ai.actionRequest != nil {
					t.Fatalf("replay called model: %#v %v", replayed, err)
				}
				var count int64
				f.db.Model(&model.GameActionRecord{}).Where("room_id = ? AND kind = 'action'", f.room.ID).Count(&count)
				if count != 11 {
					t.Fatalf("archive count=%d", count)
				}
				f.db.Model(&model.KeyEvent{}).Where("room_id = ?", f.room.ID).Count(&count)
				if count != 11 {
					t.Fatalf("business events missing or duplicated: %d", count)
				}
				var auto int64
				f.db.Model(&model.GameSave{}).Where("room_id = ? AND is_auto = 1", f.room.ID).Count(&auto)
				if auto != 1 {
					t.Fatalf("tenth-action autosave missing: %d", auto)
				}
				saved, err := game.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "business branch"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := game.LoadGame(ctx, &LoadGameRequest{UserID: 7, RoomID: f.room.ID, SaveID: saved.Save.ID, RequestID: uuid.NewString()}); err != nil {
					t.Fatal(err)
				}
				if _, err := game.SubmitAction(ctx, last); !errors.Is(err, repo.ErrMemoryConflict) {
					t.Fatalf("stale branch allowed: %v", err)
				}
			})
		}
	}
}

func TestGameMemoryLifecycleIntegrationServiceSkips(t *testing.T) {
	ctx := context.Background()
	f, lifecycle, game := memoryLifecycleFixture(t, "multiplayer")
	game.ConfigureMultiplayer(&multiplayerTurnEventRecorder{})
	resetMemoryStartFixture(t, f)
	lifecycle.ConfigureRoomStarter(repo.NewRoomRepo(f.db))
	_, multi := memorySnapshotFixture("multiplayer", f.room.ID)
	multi.CurrentTurn, multi.RoundNumber, multi.CurrentActorID = 0, 0, 7
	if _, err := lifecycle.Start(ctx, f.room, nil, multi); err != nil {
		t.Fatal(err)
	}
	source, err := f.runtime.GetMemoryControlSource(ctx, f.room.ID, 7, "multiplayer")
	if err != nil {
		t.Fatal(err)
	}
	req := &SkipMultiplayerTurnRequest{UserID: 7, RoomID: f.room.ID, ExpectedTurn: 0, RequestID: uuid.NewString(), ExpectedTimelineID: source.Memory.TimelineID, ExpectedGeneration: source.Generation}
	result, err := game.SkipMultiplayerTurn(ctx, req)
	if err != nil || result == nil || result.Memory == nil || result.Memory.DurablePosition != 2 {
		t.Fatalf("manual skip %#v %v", result, err)
	}
	if duplicate, err := game.SkipMultiplayerTurn(ctx, req); err != nil || !duplicate.Duplicate {
		t.Fatalf("manual skip replay %#v %v", duplicate, err)
	}
	now := time.Now().UTC().Add(121 * time.Second)
	tasks, err := f.runtime.ListDueMultiplayerDeadlines(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, task := range tasks {
		if task.RoomID == f.room.ID {
			matched = true
			if err := game.ProcessMultiplayerDeadline(ctx, task, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !matched {
		t.Fatal("confirmed skip did not reactivate next deadline")
	}
	var records []model.GameActionRecord
	if err := f.db.Where("room_id = ? AND kind LIKE 'skip_%'", f.room.ID).Order("position").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Kind != "skip_manual" || records[1].Kind != "skip_timeout" || records[1].RequestNamespace != "timeout" {
		t.Fatalf("skip archive %#v", records)
	}
}
