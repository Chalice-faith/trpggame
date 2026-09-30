//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"sync/atomic"
	"testing"
	"time"
	"trpggame/internal/config"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type memoryRuntimeFault struct {
	MemoryLifecycleRuntime
	phase string
	after bool
	fired atomic.Bool
}

func (r *memoryRuntimeFault) call(phase string, fn func() error) error {
	if r.phase == phase && r.fired.CompareAndSwap(false, true) {
		if r.after {
			if err := fn(); err != nil {
				return err
			}
		}
		return errors.New("injected Redis response loss")
	}
	return fn()
}
func (r *memoryRuntimeFault) InitializeMemoryStart(ctx context.Context, p model.MemoryRuntimeReplacement) error {
	return r.call("initialize", func() error { return r.MemoryLifecycleRuntime.InitializeMemoryStart(ctx, p) })
}
func (r *memoryRuntimeFault) FenceMemoryOperation(ctx context.Context, p model.MemoryRuntimeReplacement) error {
	return r.call("fence", func() error { return r.MemoryLifecycleRuntime.FenceMemoryOperation(ctx, p) })
}
func (r *memoryRuntimeFault) ApplyMemoryOperation(ctx context.Context, p model.MemoryRuntimeReplacement) error {
	return r.call("apply", func() error { return r.MemoryLifecycleRuntime.ApplyMemoryOperation(ctx, p) })
}
func (r *memoryRuntimeFault) FinishMemoryOperation(ctx context.Context, p model.MemoryRuntimeReplacement) error {
	return r.call("finish", func() error { return r.MemoryLifecycleRuntime.FinishMemoryOperation(ctx, p) })
}

func memoryLifecycleFixture(t *testing.T, mode string) (*archiveIntegrationFixture, *GameMemoryLifecycleService, *GameService) {
	t.Helper()
	f := newArchiveIntegrationFixture(t, mode)
	if err := f.db.Model(f.room).Update("turn_timeout_seconds", 120).Error; err != nil {
		t.Fatal(err)
	}
	f.room.TurnTimeoutSeconds = 120
	for i, user := range []uint{7, 8} {
		if mode == "solo" && i == 1 {
			break
		}
		char := uint(101 + i)
		if err := f.db.Create(&model.RoomPlayer{RoomID: f.room.ID, UserID: user, CharacterID: &char, PlayerOrder: i, Status: model.RoomPlayerStatusActive, IsReady: true, JoinedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { f.db.Where("room_id = ?", f.room.ID).Delete(&model.RoomPlayer{}) })
	games := repo.NewGameRepo(f.db)
	s, err := NewGameMemoryLifecycleService(f.store, f.runtime, games, f.service, config.DefaultGameArchiveConfig())
	if err != nil {
		t.Fatal(err)
	}
	g := NewGameService(games, nil, nil, f.runtime)
	g.ConfigureArchive(f.service)
	g.ConfigureMemoryLifecycle(s, false)
	return f, s, g
}

func TestGameMemoryA5StatusAuthorizationAndBranchExpectation(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, _, game := memoryLifecycleFixture(t, mode)
			if _, err := game.GetGameMemoryStatus(ctx, 9, f.room.ID); !errors.Is(err, ErrGameRoomNotFound) {
				t.Fatalf("unauthorized status: %v", err)
			}
			if mode == "multiplayer" {
				if member, err := game.GetGameMemoryStatus(ctx, 8, f.room.ID); err != nil || !member.Enabled {
					t.Fatalf("member status=%#v err=%v", member, err)
				}
			}
			pending, err := game.GetGameMemoryStatus(ctx, 7, f.room.ID)
			if err != nil || pending.Status != "pending" || pending.TimelineID != f.timeline || pending.Generation != f.generation || pending.HeadPosition != 2 || pending.DurablePosition != 1 {
				t.Fatalf("pending status=%#v err=%v", pending, err)
			}
			if err := game.requireMemoryReady(ctx, f.room); !errors.Is(err, repo.ErrGameArchiveNotReady) {
				t.Fatalf("pending write gate: %v", err)
			}
			if _, err := f.service.ArchivePending(ctx, f.room.ID); err != nil {
				t.Fatal(err)
			}
			ready, err := game.GetGameMemoryStatus(ctx, 7, f.room.ID)
			if err != nil || ready.Status != "ready" || ready.DurablePosition != 2 {
				t.Fatalf("ready status=%#v err=%v", ready, err)
			}
			if _, err := game.verifyMemoryExpectation(ctx, f.room, uuid.NewString(), f.generation); !errors.Is(err, repo.ErrMemoryConflict) {
				t.Fatalf("stale branch: %v", err)
			}
			if _, err := game.verifyMemoryExpectation(ctx, f.room, f.timeline, f.generation); err != nil {
				t.Fatalf("current branch: %v", err)
			}
		})
	}
}

func TestGameMemoryLifecycleIntegrationSaveLoadBranchAndEnd(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, s, g := memoryLifecycleFixture(t, mode)
			manual, err := g.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "branch"})
			if err != nil {
				t.Fatal(err)
			}
			if *manual.Save.MemoryPosition != 2 {
				t.Fatal("pending action not drained")
			}
			request := &LoadGameRequest{UserID: 7, RoomID: f.room.ID, SaveID: manual.Save.ID, RequestID: uuid.NewString()}
			result, err := g.LoadGame(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			state, _ := s.State(ctx, f.room.ID)
			if state.Revision != 5 || *state.ActiveTimelineID == f.timeline || result.Turn != 1 {
				t.Fatalf("branch %#v %#v", state, result)
			}
			first := *state.ActiveTimelineID
			if _, err = g.LoadGame(ctx, request); err != nil {
				t.Fatal("idempotent replay", err)
			}
			state, _ = s.State(ctx, f.room.ID)
			if *state.ActiveTimelineID != first || state.Revision != 5 {
				t.Fatal("duplicate fork")
			}
			second, err := g.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "fork"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = g.LoadGame(ctx, &LoadGameRequest{UserID: 7, RoomID: f.room.ID, SaveID: second.Save.ID, RequestID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			state, _ = s.State(ctx, f.room.ID)
			timeline, err := f.store.FindTimeline(ctx, f.room.ID, *state.ActiveTimelineID)
			if err != nil || *timeline.ParentID != first || state.Revision != 8 {
				t.Fatalf("second branch %#v %v", timeline, err)
			}
			// An old completed operation must not restore or clean up the newer branch.
			old, _ := f.store.FindOperation(ctx, f.room.ID, request.RequestID)
			if err = s.ProcessOperation(ctx, old, true); err != nil {
				t.Fatal(err)
			}
			current, _ := f.runtime.GetGameArchive(ctx, f.room.ID)
			if current.TimelineID != timeline.ID {
				t.Fatal("old worker replaced branch")
			}
			if _, err = g.ResumeGame(ctx, &ResumeGameRequest{UserID: 7, RoomID: f.room.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err = g.EndGame(ctx, &EndGameRequest{UserID: 7, RoomID: f.room.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err = g.EndGame(ctx, &EndGameRequest{UserID: 7, RoomID: f.room.ID}); err != nil {
				t.Fatal("end replay", err)
			}
			room, _ := s.games.FindRoomByID(ctx, f.room.ID)
			current, _ = f.runtime.GetGameArchive(ctx, f.room.ID)
			if room.Status != model.RoomStatusEnded || current == nil || current.DurablePosition != 2 {
				t.Fatal("end erased archive")
			}
			if n := f.client.Exists(ctx, fmt.Sprintf("room:%d:status", f.room.ID)).Val(); n != 0 {
				t.Fatal("ordinary runtime survived end")
			}
		})
	}
}

func TestGameMemoryLifecycleIntegrationLoadRecovery(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		for _, phase := range []string{"fence", "apply", "finish"} {
			for _, after := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/after=%t", mode, phase, after), func(t *testing.T) {
					ctx := context.Background()
					f, s, g := memoryLifecycleFixture(t, mode)
					manual, err := g.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "recover"})
					if err != nil {
						t.Fatal(err)
					}
					fault := &memoryRuntimeFault{MemoryLifecycleRuntime: f.runtime, phase: phase, after: after}
					s.runtime = fault
					request := &LoadGameRequest{UserID: 7, RoomID: f.room.ID, SaveID: manual.Save.ID, RequestID: uuid.NewString()}
					if _, err = g.LoadGame(ctx, request); err == nil {
						t.Fatal("fault hidden")
					}
					op, err := f.store.FindOperation(ctx, f.room.ID, request.RequestID)
					if err != nil {
						t.Fatal(err)
					}
					// Expire ordinary runtime before or after fencing, retaining the ledger.
					if phase == "fence" || phase == "apply" || (phase == "finish" && !after) {
						keys, _ := f.client.Keys(ctx, fmt.Sprintf("room:%d:*", f.room.ID)).Result()
						if len(keys) > 0 {
							f.client.Del(ctx, keys...)
						}
					}
					s.runtime = f.runtime
					if err = s.ProcessOperation(ctx, op, true); err != nil {
						t.Fatal("recovery", err)
					}
					state, _ := s.State(ctx, f.room.ID)
					meta, _ := f.runtime.GetGameArchive(ctx, f.room.ID)
					if state.Status != "ready" || state.Revision != 5 || meta.ControlOperationID != "" || meta.TimelineID != *state.ActiveTimelineID {
						t.Fatalf("recovery state %#v %#v", state, meta)
					}
					if _, err = g.LoadGame(ctx, request); err != nil {
						t.Fatal("replay", err)
					}
					var count int64
					f.db.Model(&model.GameTimeline{}).Where("room_id = ?", f.room.ID).Count(&count)
					if count != 2 {
						t.Fatal("duplicate timelines", count)
					}
				})
			}
		}
	}
}

func TestGameMemoryLifecycleIntegrationLegacyBaseline(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, s, g := memoryLifecycleFixture(t, mode)
			solo, multi := memorySnapshotFixture(mode, f.room.ID)
			var raw, messages []byte
			round := 10
			if solo != nil {
				raw, _ = json.Marshal(solo)
				messages, _ = json.Marshal(solo.RecentMessages)
			} else {
				raw, _ = json.Marshal(multi)
				messages, _ = json.Marshal(multi.RecentMessages)
				round = 5
			}
			legacy := &model.GameSave{RoomID: f.room.ID, RoundNumber: round, RedisSnapshot: raw, SummaryMemory: "legacy summary", RecentMessages: messages}
			if err := s.games.CreateSave(ctx, legacy); err != nil {
				t.Fatal(err)
			}
			if _, err := g.LoadGame(ctx, &LoadGameRequest{RoomID: f.room.ID, UserID: 7, SaveID: legacy.ID, RequestID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			state, _ := s.State(ctx, f.room.ID)
			timeline, _ := f.store.FindTimeline(ctx, f.room.ID, *state.ActiveTimelineID)
			if timeline.ParentID != nil || timeline.HistoryComplete || timeline.DurablePosition != 0 {
				t.Fatalf("fabricated history %#v", timeline)
			}
			var records []model.GameActionRecord
			f.db.Where("room_id = ? AND timeline_id = ?", f.room.ID, timeline.ID).Find(&records)
			if len(records) != 1 || records[0].Kind != "legacy_baseline" || records[0].Position != 0 {
				t.Fatal("missing baseline", records)
			}
			manual, err := g.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "converted"})
			if err != nil {
				t.Fatal(err)
			}
			var envelope MemorySaveEnvelope
			json.Unmarshal(manual.Save.RedisSnapshot, &envelope)
			if *envelope.Memory.HistoryComplete {
				t.Fatal("legacy marked complete")
			}
		})
	}
}

// A controlled starter stands in for the already-tested room roster/version transaction.
type memoryIntegrationStarter struct {
	games MemoryLifecycleGames
	err   error
}

func (s memoryIntegrationStarter) Start(ctx context.Context, owner, room uint, version uint64) (*repo.RoomRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	_, err := s.games.TransitionRoomStatus(ctx, room, owner, []model.RoomStatus{model.RoomStatusWaiting}, model.RoomStatusPlaying)
	return nil, err
}

func TestGameMemoryLifecycleIntegrationStartRecovery(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		for _, phase := range []string{"initialize", "apply", "finish"} {
			for _, after := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/after=%t", mode, phase, after), func(t *testing.T) {
					ctx := context.Background()
					f, s, _ := memoryLifecycleFixture(t, mode)
					for _, table := range []string{"game_action_records", "game_memory_operations", "game_memory_states", "game_timelines"} {
						f.db.Table(table).Where("room_id = ?", f.room.ID).Delete(map[string]any{})
					}
					for _, pattern := range []string{fmt.Sprintf("room:%d:*", f.room.ID), fmt.Sprintf("game:archive:room:%d:*", f.room.ID)} {
						keys, _ := f.client.Keys(ctx, pattern).Result()
						if len(keys) > 0 {
							f.client.Del(ctx, keys...)
						}
					}
					f.client.ZRem(ctx, "game:archive:pending_rooms", fmt.Sprint(f.room.ID))
					f.db.Model(f.room).Updates(map[string]any{"status": model.RoomStatusWaiting, "current_turn": 0, "round_number": 0})
					f.room.Status = model.RoomStatusWaiting
					solo, multi := memorySnapshotFixture(mode, f.room.ID)
					if solo != nil {
						solo.Turn = 0
					} else {
						multi.CurrentTurn = 0
						multi.RoundNumber = 0
						multi.CurrentActorID = 7
					}
					s.ConfigureRoomStarter(repo.NewRoomRepo(f.db))
					s.runtime = &memoryRuntimeFault{MemoryLifecycleRuntime: f.runtime, phase: phase, after: after}
					op, err := s.Start(ctx, f.room, solo, multi)
					if err == nil || op == nil {
						t.Fatalf("fault start %#v %v", op, err)
					}
					var records int64
					f.db.Model(&model.GameActionRecord{}).Where("room_id = ?", f.room.ID).Count(&records)
					if records != 0 {
						t.Fatal("provisional history visible")
					}
					s.runtime = f.runtime
					if err = s.ProcessOperation(ctx, op, true); err != nil {
						t.Fatal("recover start", err)
					}
					state, _ := s.State(ctx, f.room.ID)
					meta, _ := f.runtime.GetGameArchive(ctx, f.room.ID)
					if state.Status != "ready" || state.Revision != 3 || meta.DurablePosition != 1 {
						t.Fatalf("start %#v %#v", state, meta)
					}
					f.db.Model(&model.GameActionRecord{}).Where("room_id = ? AND kind = 'opening'", f.room.ID).Count(&records)
					if records != 1 {
						t.Fatal("opening duplicates", records)
					}
					if err = s.ProcessOperation(ctx, op, true); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestGameMemoryLifecycleIntegrationEndRecovery(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		for _, phase := range []string{"fence", "apply", "finish"} {
			for _, after := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/after=%t", mode, phase, after), func(t *testing.T) {
					ctx := context.Background()
					f, s, g := memoryLifecycleFixture(t, mode)
					s.runtime = &memoryRuntimeFault{MemoryLifecycleRuntime: f.runtime, phase: phase, after: after}
					if _, err := g.EndGame(ctx, &EndGameRequest{UserID: 7, RoomID: f.room.ID}); err == nil {
						t.Fatal("fault hidden")
					}
					op, err := f.store.FindEndOperation(ctx, f.room.ID, f.timeline)
					if err != nil {
						t.Fatal(err)
					}
					s.runtime = f.runtime
					if err = s.ProcessOperation(ctx, op, true); err != nil {
						t.Fatal("end recovery", err)
					}
					room, _ := s.games.FindRoomByID(ctx, f.room.ID)
					state, _ := s.State(ctx, f.room.ID)
					meta, _ := f.runtime.GetGameArchive(ctx, f.room.ID)
					if room.Status != model.RoomStatusEnded || room.CurrentTurn != 1 || state.Status != "ended" || meta.DurablePosition != 2 {
						t.Fatalf("end progress %#v %#v %#v", room, state, meta)
					}
					if f.client.Exists(ctx, fmt.Sprintf("room:%d:status", f.room.ID)).Val() != 0 {
						t.Fatal("end cleanup incomplete")
					}
					if _, err = g.EndGame(ctx, &EndGameRequest{UserID: 7, RoomID: f.room.ID}); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

type memoryJournalFault struct {
	MemoryLifecycleJournal
	phase string
	fired atomic.Bool
}

func (r *memoryJournalFault) AdvanceOperation(ctx context.Context, room uint, id, from, to string) error {
	err := r.MemoryLifecycleJournal.AdvanceOperation(ctx, room, id, from, to)
	if err == nil && to == r.phase && r.fired.CompareAndSwap(false, true) {
		return errors.New("lost MySQL phase response")
	}
	return err
}
func (r *memoryJournalFault) CompleteOperationAndProgress(ctx context.Context, room uint, id string, p repo.MemoryOperationProgress) error {
	err := r.MemoryLifecycleJournal.CompleteOperationAndProgress(ctx, room, id, p)
	if err == nil && r.phase == "completed" && r.fired.CompareAndSwap(false, true) {
		return errors.New("lost MySQL completion response")
	}
	return err
}

func TestGameMemoryLifecycleIntegrationLostMySQLAndWorker(t *testing.T) {
	for _, phase := range []string{"redis_applied", "completed"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			f, s, g := memoryLifecycleFixture(t, "solo")
			manual, err := g.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "recover"})
			if err != nil {
				t.Fatal(err)
			}
			s.journal = &memoryJournalFault{MemoryLifecycleJournal: f.store, phase: phase}
			req := &LoadGameRequest{UserID: 7, RoomID: f.room.ID, SaveID: manual.Save.ID, RequestID: uuid.NewString()}
			if _, err = g.LoadGame(ctx, req); err == nil {
				t.Fatal("fault hidden")
			}
			s.journal = f.store
			NewGameMemoryLifecycleWorker(s).processDue()
			op, _ := f.store.FindOperation(ctx, f.room.ID, req.RequestID)
			state, _ := s.State(ctx, f.room.ID)
			if op.Phase != "completed" || op.NextRetryAt.Year() != 9999 || state.Revision != 5 {
				t.Fatalf("worker %#v %#v", op, state)
			}
			if _, err = g.LoadGame(ctx, req); err != nil {
				t.Fatal(err)
			}
			worker := NewGameMemoryLifecycleWorker(s)
			go worker.Run()
			worker.Stop()
		})
	}
}

func memoryCommitNext(t *testing.T, f *archiveIntegrationFixture, mode string) {
	t.Helper()
	ctx := context.Background()
	source, err := f.runtime.GetMemoryControlSource(ctx, f.room.ID, 7, mode)
	if err != nil {
		t.Fatal(err)
	}
	actor := uint(7)
	count := 1
	if mode == "multiplayer" {
		count = 2
		actor = uint(7 + source.Turn%2)
	}
	request, fingerprint := uuid.NewString(), fmt.Sprintf("%064x", source.Turn+1)
	round := (source.Turn + 1) / count
	record := &model.GameActionRecord{CommitID: uuid.NewString(), RoomID: f.room.ID, TimelineID: source.Memory.TimelineID, Position: source.Memory.HeadPosition + 1,
		Kind: "action", ActorID: &actor, RequestNamespace: "client", RequestID: request, Fingerprint: fingerprint, SourceGeneration: source.Generation, SourceRevision: source.Memory.Revision,
		TurnBefore: source.Turn, TurnAfter: source.Turn + 1, RoundBefore: source.Round, RoundAfter: round, PayloadVersion: 1}
	response := []byte(fmt.Sprintf(`{"narrative":"boundary action","current_turn":%d,"round_number":%d,"generation":"%s"}`, source.Turn+1, round, source.Generation))
	messages := []model.RuntimeMessage{{Role: "user", Content: "look"}, {Role: "assistant", Content: "boundary action"}}
	if mode == "solo" {
		_, err = f.runtime.CommitAction(ctx, &model.ActionRuntimeMutation{RoomID: f.room.ID, UserID: actor, Generation: source.Generation, ExpectedTurn: source.Turn, RequestID: request, RequestFingerprint: fingerprint, ResponseJSON: response, Messages: messages, Archive: record})
	} else {
		_, err = f.runtime.AcquireMemoryMultiplayerAction(ctx, f.room.ID, actor, source.Turn, request, fingerprint, time.Now(), model.GameArchiveExpectation{TimelineID: source.Memory.TimelineID, Generation: source.Generation})
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.runtime.CommitMultiplayerAction(ctx, &model.MultiplayerActionMutation{RoomID: f.room.ID, UserID: actor, Generation: source.Generation, ExpectedTurn: source.Turn, RequestID: request, RequestFingerprint: fingerprint, ResponseJSON: response, Messages: messages, NextDeadline: time.Now().Add(time.Minute), Archive: record})
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.ArchivePending(ctx, f.room.ID); err != nil {
		t.Fatal(err)
	}
}

type memoryAutoSaveFault struct {
	MemoryLifecycleGames
	fired atomic.Bool
}

func (r *memoryAutoSaveFault) CreateAutoSave(ctx context.Context, save *model.GameSave) (bool, error) {
	created, err := r.MemoryLifecycleGames.CreateAutoSave(ctx, save)
	if err == nil && r.fired.CompareAndSwap(false, true) {
		return false, errors.New("lost autosave SQL response")
	}
	return created, err
}

func TestGameMemoryLifecycleIntegrationOriginalAutoSave(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, s, _ := memoryLifecycleFixture(t, mode)
			if _, err := f.service.ArchivePending(ctx, f.room.ID); err != nil {
				t.Fatal(err)
			}
			for turn := 1; turn < 10; turn++ {
				memoryCommitNext(t, f, mode)
			}
			pending, err := f.runtime.ListPendingMemoryAutoSaves(ctx, f.room.ID)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending %#v %v", pending, err)
			}
			// Simulate later mutable summary data; the save must use the sealed boundary.
			f.client.Set(ctx, fmt.Sprintf("room:%d:summary", f.room.ID), "later summary", time.Hour)
			s.games = &memoryAutoSaveFault{MemoryLifecycleGames: s.games}
			if err = s.FlushAutoSaves(ctx, f.room.ID); err == nil {
				t.Fatal("fault hidden")
			}
			still, _ := f.runtime.ListPendingMemoryAutoSaves(ctx, f.room.ID)
			if len(still) != 1 {
				t.Fatal("pending deleted before confirmation")
			}
			if err = s.FlushAutoSaves(ctx, f.room.ID); err != nil {
				t.Fatal("retry", err)
			}
			var saves []model.GameSave
			f.db.Where("room_id = ? AND is_auto = true", f.room.ID).Find(&saves)
			if len(saves) != 1 || saves[0].SummaryMemory == "later summary" || *saves[0].MemoryPosition != 11 || saves[0].RoundNumber != pending[0].RoundNumber {
				t.Fatalf("boundary %#v", saves)
			}
			still, _ = f.runtime.ListPendingMemoryAutoSaves(ctx, f.room.ID)
			if len(still) != 0 {
				t.Fatal("auto ACK missing")
			}
			var envelope MemorySaveEnvelope
			json.Unmarshal(saves[0].RedisSnapshot, &envelope)
			if envelope.SchemaVersion != 3 {
				t.Fatal("auto format")
			}
		})
	}
}

func resetMemoryStartFixture(t *testing.T, f *archiveIntegrationFixture) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"game_action_records", "game_memory_operations", "game_memory_states", "game_timelines"} {
		if err := f.db.Table(table).Where("room_id = ?", f.room.ID).Delete(map[string]any{}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, pattern := range []string{fmt.Sprintf("room:%d:*", f.room.ID), fmt.Sprintf("game:archive:room:%d:*", f.room.ID)} {
		keys, err := f.client.Keys(ctx, pattern).Result()
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) > 0 {
			if err = f.client.Del(ctx, keys...).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.client.ZRem(ctx, "game:archive:pending_rooms", fmt.Sprint(f.room.ID))
	order := []byte(`[7,8]`)
	if f.room.IsSolo {
		order = []byte(`[7]`)
	}
	if err := f.db.Model(f.room).Updates(map[string]any{"status": model.RoomStatusWaiting, "current_turn": 0, "round_number": 0, "turn_order": order}).Error; err != nil {
		t.Fatal(err)
	}
	f.room.Status = model.RoomStatusWaiting
	f.room.TurnOrder = order
}

func TestGameMemoryLifecycleIntegrationStartPendingGateAndAbort(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, s, _ := memoryLifecycleFixture(t, mode)
			resetMemoryStartFixture(t, f)
			solo, multi := memorySnapshotFixture(mode, f.room.ID)
			if solo != nil {
				solo.Turn = 0
			} else {
				multi.CurrentTurn = 0
				multi.RoundNumber = 0
				multi.CurrentActorID = 7
			}
			s.ConfigureRoomStarter(repo.NewRoomRepo(f.db))
			outage := &archiveOutageStore{GameArchivePersistence: f.store}
			outage.fail.Store(true)
			s.archive.store = outage
			op, err := s.Start(ctx, f.room, solo, multi)
			if err != nil {
				t.Fatal(err)
			}
			meta, _ := f.runtime.GetGameArchive(ctx, f.room.ID)
			source, _ := f.runtime.GetMemoryControlSource(ctx, f.room.ID, 7, mode)
			if meta.ArchiveState != "pending" || meta.HeadPosition != 1 || meta.DurablePosition != 0 || source.Status != model.RoomStatusPlaying {
				t.Fatalf("opening gate %#v %#v", meta, source)
			}
			expected := model.GameArchiveExpectation{TimelineID: meta.TimelineID, Generation: source.Generation}
			if mode == "solo" {
				_, err = f.runtime.BeginMemorySoloAction(ctx, f.room.ID, 7, 0, expected)
			} else {
				_, err = f.runtime.AcquireMemoryMultiplayerAction(ctx, f.room.ID, 7, 0, uuid.NewString(), fmt.Sprintf("%064x", 1), time.Now(), expected)
			}
			if !errors.Is(err, repo.ErrGameArchiveNotReady) {
				t.Fatal("first action accepted before opening archive", err)
			}
			time.Sleep(1100 * time.Millisecond)
			if _, err = f.service.ArchivePending(ctx, f.room.ID); err != nil {
				t.Fatal(err)
			}
			meta, _ = f.runtime.GetGameArchive(ctx, f.room.ID)
			if meta.DurablePosition != 1 {
				t.Fatal("opening not durable")
			}
			if mode == "multiplayer" {
				snapshot, err := f.runtime.GetMultiplayerRoom(ctx, f.room.ID)
				if err != nil || snapshot.DeadlineAt == nil {
					t.Fatalf("first timer %#v %v", snapshot, err)
				}
			}
			if err = s.ProcessOperation(ctx, op, true); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("explicit_start_failure", func(t *testing.T) {
		ctx := context.Background()
		f, s, _ := memoryLifecycleFixture(t, "multiplayer")
		resetMemoryStartFixture(t, f)
		_, multi := memorySnapshotFixture("multiplayer", f.room.ID)
		multi.CurrentTurn = 0
		multi.RoundNumber = 0
		multi.CurrentActorID = 7
		s.ConfigureRoomStarter(memoryIntegrationStarter{games: s.games, err: repo.ErrRoomStartConditions})
		op, err := s.Start(ctx, f.room, nil, multi)
		if !errors.Is(err, repo.ErrRoomStartConditions) {
			t.Fatal(err)
		}
		op, _ = f.store.FindOperation(ctx, f.room.ID, op.OperationID)
		state, _ := s.State(ctx, f.room.ID)
		var count int64
		f.db.Model(&model.GameActionRecord{}).Where("room_id = ?", f.room.ID).Count(&count)
		if op.Phase != "aborted" || state.Status != "blocked" || count != 0 || f.client.Exists(ctx, fmt.Sprintf("room:%d:status", f.room.ID)).Val() != 0 {
			t.Fatalf("failed provisional %#v %#v records=%d", op, state, count)
		}
	})
}

func TestGameMemoryLifecycleIntegrationLateSaveAndProgressFence(t *testing.T) {
	ctx := context.Background()
	f, s, g := memoryLifecycleFixture(t, "solo")
	manual, err := g.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "before"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.LoadGame(ctx, &LoadGameRequest{UserID: 7, RoomID: f.room.ID, SaveID: manual.Save.ID, RequestID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	copy := *manual.Save
	copy.ID = 0
	if err = s.games.CreateCurrentMemorySave(ctx, &copy, 2); !errors.Is(err, repo.ErrMemoryBusy) {
		t.Fatal("stale capture saved", err)
	}
	if changed, err := s.games.AdvanceRoomProgress(ctx, f.room.ID, 7, 999); err != nil || changed {
		t.Fatal("legacy progress overwrite", err)
	}
	room, _ := s.games.FindRoomByID(ctx, f.room.ID)
	if room.CurrentTurn != 1 {
		t.Fatal("load progress replaced", room.CurrentTurn)
	}
}

type memoryExpiryOnFinish struct {
	MemoryLifecycleRuntime
	fixture *archiveIntegrationFixture
	fired   atomic.Bool
}

func (r *memoryExpiryOnFinish) FinishMemoryOperation(ctx context.Context, p model.MemoryRuntimeReplacement) error {
	if r.fired.CompareAndSwap(false, true) {
		keys, err := r.fixture.client.Keys(ctx, fmt.Sprintf("room:%d:*", p.RoomID)).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err = r.fixture.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
	}
	return r.MemoryLifecycleRuntime.FinishMemoryOperation(ctx, p)
}

func TestGameMemoryLifecycleIntegrationExpiryAtFinalization(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, s, g := memoryLifecycleFixture(t, mode)
			manual, err := g.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "expiry"})
			if err != nil {
				t.Fatal(err)
			}
			s.runtime = &memoryExpiryOnFinish{MemoryLifecycleRuntime: f.runtime, fixture: f}
			req := &LoadGameRequest{UserID: 7, RoomID: f.room.ID, SaveID: manual.Save.ID, RequestID: uuid.NewString()}
			if _, err = g.LoadGame(ctx, req); !errors.Is(err, repo.ErrGameArchiveNotReady) {
				t.Fatal("missing runtime finalized", err)
			}
			op, _ := f.store.FindOperation(ctx, f.room.ID, req.RequestID)
			meta, _ := f.runtime.GetGameArchive(ctx, f.room.ID)
			if op.Phase != "completed" || op.NextRetryAt.Year() == 9999 || meta.ControlOperationID != op.OperationID {
				t.Fatalf("lost recovery gate %#v %#v", op, meta)
			}
			s.runtime = f.runtime
			if err = s.ProcessOperation(ctx, op, true); err != nil {
				t.Fatal("restore target", err)
			}
			source, err := f.runtime.GetMemoryControlSource(ctx, f.room.ID, 7, mode)
			if err != nil || source.Status != model.RoomStatusPaused || source.Memory.ControlOperationID != "" || source.Turn != 1 {
				t.Fatalf("restored %#v %v", source, err)
			}
		})
	}
}
