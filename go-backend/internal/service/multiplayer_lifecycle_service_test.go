package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type lifecycleGameRepository struct {
	*fakeGameRepository
	players             []model.RoomPlayer
	endedStatuses       []model.RoomStatus
	endedTurns          []int
	endedRounds         []int
	replacedTurns       []int
	replacedRounds      []int
	advancedMultiplayer int
}

func (r *lifecycleGameRepository) FindRoomByID(_ context.Context, roomID uint) (*model.GameRoom, error) {
	if r.room == nil || r.room.ID != roomID {
		return nil, errors.New("room not found")
	}
	copyRoom := *r.room
	return &copyRoom, nil
}

func (r *lifecycleGameRepository) FindPlayersByRoom(_ context.Context, _ uint) ([]model.RoomPlayer, error) {
	return append([]model.RoomPlayer(nil), r.players...), nil
}

func (r *lifecycleGameRepository) AdvanceMultiplayerRoomProgress(_ context.Context, _ uint, _, _ int) (bool, error) {
	r.advancedMultiplayer++
	return true, nil
}

func (r *lifecycleGameRepository) TransitionRoomStatus(ctx context.Context, roomID, ownerID uint, from []model.RoomStatus, to model.RoomStatus) (bool, error) {
	updated, err := r.fakeGameRepository.TransitionRoomStatus(ctx, roomID, ownerID, from, to)
	if updated && err == nil && r.room != nil {
		r.room.Status = to
	}
	return updated, err
}

func (r *lifecycleGameRepository) ReplacePausedMultiplayerRoomProgress(_ context.Context, _, _ uint, turn, round int) (bool, error) {
	r.replacedTurns = append(r.replacedTurns, turn)
	r.replacedRounds = append(r.replacedRounds, round)
	if r.room != nil {
		r.room.CurrentTurn, r.room.RoundNumber = turn, round
	}
	return true, nil
}

func (r *lifecycleGameRepository) EndMultiplayerRoom(_ context.Context, _, _ uint, from []model.RoomStatus, turn, round int) (bool, error) {
	r.endedStatuses = append([]model.RoomStatus(nil), from...)
	r.endedTurns = append(r.endedTurns, turn)
	r.endedRounds = append(r.endedRounds, round)
	if r.room != nil {
		r.room.Status, r.room.CurrentTurn, r.room.RoundNumber = model.RoomStatusEnded, turn, round
	}
	return true, nil
}

type lifecycleRuntimeRepository struct {
	*fakeGameRuntimeRepository
	snapshot     *model.MultiplayerRuntimeSnapshot
	transitions  []multiplayerLifecycleTransition
	restored     *model.MultiplayerRuntimeSnapshot
	pending      []model.PendingMultiplayerAutoSave
	acknowledged []int
}

type multiplayerLifecycleTransition struct {
	from, to       model.RoomStatus
	oldGen, newGen string
	deadline       time.Time
}

func (r *lifecycleRuntimeRepository) GetMultiplayerRoom(_ context.Context, _ uint) (*model.MultiplayerRuntimeSnapshot, error) {
	if r.snapshot == nil {
		return nil, repo.ErrGameRuntimeUnavailable
	}
	if r.snapshot.Status == model.RoomStatusEnded {
		return nil, repo.ErrGameRuntimeStatusConflict
	}
	encoded, _ := json.Marshal(r.snapshot)
	var copySnapshot model.MultiplayerRuntimeSnapshot
	_ = json.Unmarshal(encoded, &copySnapshot)
	copySnapshot.ActionLease = r.snapshot.ActionLease
	return &copySnapshot, nil
}

func (r *lifecycleRuntimeRepository) TransitionMultiplayerRoom(_ context.Context, _ uint, oldGen, newGen string, from, to model.RoomStatus, deadline time.Time) (int, error) {
	if r.snapshot == nil || r.snapshot.Generation != oldGen || r.snapshot.Status != from {
		return 0, repo.ErrMultiplayerRuntimeConflict
	}
	r.transitions = append(r.transitions, multiplayerLifecycleTransition{from: from, to: to, oldGen: oldGen, newGen: newGen, deadline: deadline})
	r.snapshot.Generation, r.snapshot.Status, r.snapshot.ActionLease = newGen, to, nil
	if to == model.RoomStatusPlaying {
		copyDeadline := deadline.UTC()
		r.snapshot.DeadlineAt = &copyDeadline
	} else {
		r.snapshot.DeadlineAt = nil
	}
	return r.snapshot.CurrentTurn, nil
}

func (r *lifecycleRuntimeRepository) RestoreMultiplayerRoom(_ context.Context, expectedGeneration string, snapshot *model.MultiplayerRuntimeSnapshot) error {
	if r.snapshot == nil || r.snapshot.Status != model.RoomStatusPaused || r.snapshot.Generation != expectedGeneration {
		return repo.ErrMultiplayerRuntimeConflict
	}
	if len(r.pending) > len(r.acknowledged) {
		return errors.New("pending auto-save was not acknowledged before restore")
	}
	encoded, _ := json.Marshal(snapshot)
	var copySnapshot model.MultiplayerRuntimeSnapshot
	_ = json.Unmarshal(encoded, &copySnapshot)
	r.restored, r.snapshot = &copySnapshot, &copySnapshot
	return nil
}

func (r *lifecycleRuntimeRepository) ListPendingMultiplayerAutoSaveRooms(context.Context, int) ([]uint, error) {
	if len(r.pending) == 0 {
		return nil, nil
	}
	return []uint{41}, nil
}

func (r *lifecycleRuntimeRepository) ListPendingMultiplayerAutoSaves(context.Context, uint) ([]model.PendingMultiplayerAutoSave, error) {
	return append([]model.PendingMultiplayerAutoSave(nil), r.pending...), nil
}

func (r *lifecycleRuntimeRepository) AcknowledgeMultiplayerAutoSave(_ context.Context, _ uint, round int, _ string) error {
	r.acknowledged = append(r.acknowledged, round)
	return nil
}

type lifecyclePublisher struct {
	events []string
}

func (p *lifecyclePublisher) PublishMultiplayerActionEvent(uint, string, GameActionStreamEvent) {}
func (p *lifecyclePublisher) PublishMultiplayerLifecycle(snapshot *model.MultiplayerRuntimeSnapshot) {
	p.events = append(p.events, string(snapshot.Status))
}
func (p *lifecyclePublisher) PublishMultiplayerEnded(uint, string, int, int) {
	p.events = append(p.events, string(model.RoomStatusEnded))
}

type lifecyclePresenceRecorder struct{ userIDs [][]uint }

func (p *lifecyclePresenceRecorder) NotifyGameStatusChanged(_ context.Context, ids []uint) {
	p.userIDs = append(p.userIDs, append([]uint(nil), ids...))
}

func newMultiplayerLifecycleFixture(status model.RoomStatus, turn int) (*lifecycleGameRepository, *lifecycleRuntimeRepository, *GameService, *lifecyclePublisher, *lifecyclePresenceRecorder) {
	turnOrder, _ := json.Marshal([]uint{7, 8})
	room := &model.GameRoom{ID: 41, OwnerID: 7, Status: status, IsSolo: false, TurnOrder: turnOrder, TurnTimeoutSeconds: 30}
	charA, charB := uint(101), uint(102)
	players := []model.RoomPlayer{
		{RoomID: 41, UserID: 7, CharacterID: &charA, PlayerOrder: 0, Status: model.RoomPlayerStatusActive},
		{RoomID: 41, UserID: 8, CharacterID: &charB, PlayerOrder: 1, Status: model.RoomPlayerStatusActive},
	}
	baseRepo := &fakeGameRepository{room: room, assignSaveID: 91, autoSaveCreated: true}
	gameRepo := &lifecycleGameRepository{fakeGameRepository: baseRepo, players: players}
	snapshot := validLifecycleMultiplayerSnapshot(status, turn)
	runtime := &lifecycleRuntimeRepository{fakeGameRuntimeRepository: &fakeGameRuntimeRepository{}, snapshot: snapshot}
	service := NewGameService(gameRepo, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, runtime)
	publisher, presence := &lifecyclePublisher{}, &lifecyclePresenceRecorder{}
	service.ConfigureMultiplayer(publisher)
	service.ConfigurePresence(presence)
	service.now = func() time.Time { return time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC) }
	return gameRepo, runtime, service, publisher, presence
}

func validLifecycleMultiplayerSnapshot(status model.RoomStatus, turn int) *model.MultiplayerRuntimeSnapshot {
	order := []uint{7, 8}
	snapshot := &model.MultiplayerRuntimeSnapshot{
		Version: model.MultiplayerRuntimeSnapshotVersion, RoomID: 41, Status: status, Generation: uuid.NewString(),
		CurrentTurn: turn, RoundNumber: turn / len(order), TurnOrder: order, CurrentActorID: order[turn%len(order)],
		Players: []model.MultiplayerRuntimePlayer{
			{UserID: 7, CharacterID: 101, PlayerState: map[string]string{"hp": "12"}, Items: []model.RuntimeItem{}, Buffs: []model.RuntimeBuff{}},
			{UserID: 8, CharacterID: 102, PlayerState: map[string]string{"hp": "9"}, Items: []model.RuntimeItem{}, Buffs: []model.RuntimeBuff{}},
		},
		SummaryMemory: "the party entered the hall", RecentMessages: []model.RuntimeMessage{{Role: "assistant", Content: "The door opens."}},
	}
	if status == model.RoomStatusPlaying {
		deadline := time.Date(2026, 9, 23, 10, 1, 0, 0, time.UTC)
		snapshot.DeadlineAt = &deadline
	}
	return snapshot
}

func TestMultiplayerLifecyclePauseResumeAndPresence(t *testing.T) {
	gameRepo, runtime, service, publisher, presence := newMultiplayerLifecycleFixture(model.RoomStatusPlaying, 4)
	originalGeneration := runtime.snapshot.Generation
	paused, err := service.PauseGame(context.Background(), &PauseGameRequest{UserID: 7, RoomID: 41})
	if err != nil || paused.Status != model.RoomStatusPaused || gameRepo.room.Status != model.RoomStatusPaused {
		t.Fatalf("PauseGame() = (%#v, %v), room=%s", paused, err, gameRepo.room.Status)
	}
	if len(runtime.transitions) != 1 || runtime.transitions[0].from != model.RoomStatusPlaying ||
		runtime.transitions[0].to != model.RoomStatusPaused || runtime.transitions[0].newGen == originalGeneration ||
		runtime.snapshot.DeadlineAt != nil || len(presence.userIDs) != 1 {
		t.Fatalf("pause transition=%#v runtime=%#v presence=%#v", runtime.transitions, runtime.snapshot, presence.userIDs)
	}
	resumed, err := service.ResumeGame(context.Background(), &ResumeGameRequest{UserID: 7, RoomID: 41})
	if err != nil || resumed.Status != model.RoomStatusPlaying || gameRepo.room.Status != model.RoomStatusPlaying {
		t.Fatalf("ResumeGame() = (%#v, %v), room=%s", resumed, err, gameRepo.room.Status)
	}
	if len(runtime.transitions) != 2 || runtime.transitions[1].to != model.RoomStatusPlaying ||
		runtime.transitions[1].newGen == runtime.transitions[0].newGen || runtime.snapshot.DeadlineAt == nil ||
		len(presence.userIDs) != 2 || len(publisher.events) != 2 {
		t.Fatalf("resume transition=%#v runtime=%#v presence=%#v events=%#v", runtime.transitions, runtime.snapshot, presence.userIDs, publisher.events)
	}
}

func TestMultiplayerPauseRestoresRuntimeWhenMySQLConfirmsItStayedPlaying(t *testing.T) {
	gameRepo, runtime, service, _, _ := newMultiplayerLifecycleFixture(model.RoomStatusPlaying, 4)
	gameRepo.transitionResults = []gameTransitionResult{{err: errors.New("mysql unavailable")}}
	gameRepo.roomResults = []gameRoomQueryResult{
		{room: gameRepo.room},
		{room: gameRepo.room},
	}
	_, err := service.PauseGame(context.Background(), &PauseGameRequest{UserID: 7, RoomID: 41})
	if !errors.Is(err, ErrInternal) || runtime.snapshot.Status != model.RoomStatusPlaying || runtime.snapshot.DeadlineAt == nil ||
		len(runtime.transitions) != 2 || runtime.transitions[0].to != model.RoomStatusPaused ||
		runtime.transitions[1].from != model.RoomStatusPaused || runtime.transitions[1].to != model.RoomStatusPlaying {
		t.Fatalf("PauseGame() error=%v transitions=%#v runtime=%#v", err, runtime.transitions, runtime.snapshot)
	}
}

func TestMultiplayerManualSavePausesAndStoresSplitSnapshotColumns(t *testing.T) {
	gameRepo, runtime, service, _, _ := newMultiplayerLifecycleFixture(model.RoomStatusPlaying, 4)
	result, err := service.CreateManualSave(context.Background(), &CreateManualSaveRequest{UserID: 7, RoomID: 41, SaveName: " before gate "})
	if err != nil {
		t.Fatalf("CreateManualSave() error = %v", err)
	}
	if result == nil || result.Save == nil || result.Save.ID != 91 || result.Save.SaveName != "before gate" ||
		result.Save.RoundNumber != 2 || result.Save.IsAuto || gameRepo.room.Status != model.RoomStatusPaused || runtime.snapshot.Status != model.RoomStatusPaused {
		t.Fatalf("save=%#v room=%#v runtime=%#v", result, gameRepo.room, runtime.snapshot)
	}
	var persisted map[string]any
	if err := json.Unmarshal(result.Save.RedisSnapshot, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, exists := persisted["summary_memory"]; exists || persisted["recent_messages"] != nil ||
		result.Save.SummaryMemory != "the party entered the hall" || len(result.Save.RecentMessages) == 0 {
		t.Fatalf("save split fields incorrect: %#v / %s", persisted, result.Save.RecentMessages)
	}
}

func TestMultiplayerLoadRestoresValidatedV2SaveAsPaused(t *testing.T) {
	gameRepo, runtime, service, _, _ := newMultiplayerLifecycleFixture(model.RoomStatusPlaying, 6)
	saved := validLifecycleMultiplayerSnapshot(model.RoomStatusPlaying, 4)
	save, err := multiplayerSaveFromSnapshot(41, "round two", saved, false)
	if err != nil {
		t.Fatal(err)
	}
	save.ID = 93
	gameRepo.foundSave = save
	result, err := service.LoadGame(context.Background(), &LoadGameRequest{UserID: 7, RoomID: 41, SaveID: 93})
	if err != nil {
		t.Fatalf("LoadGame() error = %v", err)
	}
	if result == nil || result.Status != model.RoomStatusPaused || result.Turn != 4 || gameRepo.room.Status != model.RoomStatusPaused ||
		runtime.restored == nil || runtime.restored.CurrentTurn != 4 || runtime.restored.RoundNumber != 2 ||
		runtime.restored.Generation == saved.Generation || runtime.restored.DeadlineAt != nil ||
		runtime.restored.SummaryMemory != saved.SummaryMemory || len(gameRepo.replacedTurns) != 1 || gameRepo.replacedRounds[0] != 2 {
		t.Fatalf("load=%#v room=%#v restored=%#v progress=%#v", result, gameRepo.room, runtime.restored, gameRepo.replacedTurns)
	}
}

func TestMultiplayerLoadFlushesPendingAutoSaveBeforeReplacingRuntime(t *testing.T) {
	gameRepo, runtime, service, _, _ := newMultiplayerLifecycleFixture(model.RoomStatusPlaying, 10)
	pending := validLifecycleMultiplayerSnapshot(model.RoomStatusPlaying, 10)
	runtime.pending = []model.PendingMultiplayerAutoSave{{Generation: pending.Generation, Snapshot: pending}}
	saved := validLifecycleMultiplayerSnapshot(model.RoomStatusPlaying, 4)
	save, err := multiplayerSaveFromSnapshot(41, "earlier turn", saved, false)
	if err != nil {
		t.Fatal(err)
	}
	save.ID, gameRepo.foundSave = 93, save
	if _, err := service.LoadGame(context.Background(), &LoadGameRequest{UserID: 7, RoomID: 41, SaveID: 93}); err != nil {
		t.Fatalf("LoadGame() error = %v", err)
	}
	if gameRepo.createdAutoSave == nil || gameRepo.createdAutoSave.RoundNumber != 5 ||
		len(runtime.acknowledged) != 1 || runtime.acknowledged[0] != 5 || runtime.restored == nil {
		t.Fatalf("auto-save=%#v acknowledged=%#v restored=%#v", gameRepo.createdAutoSave, runtime.acknowledged, runtime.restored)
	}
}

func TestMultiplayerLoadKeepsPausedRuntimeWhenPendingAutoSaveCannotPersist(t *testing.T) {
	gameRepo, runtime, service, _, _ := newMultiplayerLifecycleFixture(model.RoomStatusPlaying, 10)
	pending := validLifecycleMultiplayerSnapshot(model.RoomStatusPlaying, 10)
	runtime.pending = []model.PendingMultiplayerAutoSave{{Generation: pending.Generation, Snapshot: pending}}
	gameRepo.createAutoSaveErr = errors.New("database unavailable")
	saved := validLifecycleMultiplayerSnapshot(model.RoomStatusPlaying, 4)
	save, err := multiplayerSaveFromSnapshot(41, "earlier turn", saved, false)
	if err != nil {
		t.Fatal(err)
	}
	save.ID, gameRepo.foundSave = 93, save
	_, err = service.LoadGame(context.Background(), &LoadGameRequest{UserID: 7, RoomID: 41, SaveID: 93})
	if !errors.Is(err, ErrInternal) || gameRepo.room.Status != model.RoomStatusPaused ||
		runtime.snapshot.Status != model.RoomStatusPaused || runtime.restored != nil || len(runtime.acknowledged) != 0 {
		t.Fatalf("LoadGame() error=%v room=%#v runtime=%#v acknowledged=%#v", err, gameRepo.room, runtime.snapshot, runtime.acknowledged)
	}
}

func TestMultiplayerLoadRejectsSaveFromDifferentFrozenRoster(t *testing.T) {
	gameRepo, runtime, service, _, _ := newMultiplayerLifecycleFixture(model.RoomStatusPaused, 4)
	saved := validLifecycleMultiplayerSnapshot(model.RoomStatusPaused, 4)
	saved.TurnOrder = []uint{7, 9}
	saved.CurrentActorID = 7
	saved.Players[1].UserID, saved.Players[1].CharacterID = 9, 103
	save, err := multiplayerSaveFromSnapshot(41, "wrong roster", saved, false)
	if err != nil {
		t.Fatal(err)
	}
	save.ID, gameRepo.foundSave = 95, save
	_, err = service.LoadGame(context.Background(), &LoadGameRequest{UserID: 7, RoomID: 41, SaveID: 95})
	if !errors.Is(err, ErrMultiplayerSaveIncompatible) || len(runtime.transitions) != 0 || runtime.restored != nil {
		t.Fatalf("LoadGame() error=%v, transitions=%#v restored=%#v", err, runtime.transitions, runtime.restored)
	}
}

func TestMultiplayerEndPersistsFinalProgressAndFencesRuntime(t *testing.T) {
	gameRepo, runtime, service, publisher, presence := newMultiplayerLifecycleFixture(model.RoomStatusPlaying, 8)
	result, err := service.EndGame(context.Background(), &EndGameRequest{UserID: 7, RoomID: 41})
	if err != nil || result.Status != model.RoomStatusEnded || gameRepo.room.Status != model.RoomStatusEnded ||
		gameRepo.room.CurrentTurn != 8 || gameRepo.room.RoundNumber != 4 || runtime.snapshot.Status != model.RoomStatusEnded {
		t.Fatalf("EndGame() = (%#v,%v), room=%#v runtime=%#v", result, err, gameRepo.room, runtime.snapshot)
	}
	if len(gameRepo.endedTurns) != 1 || gameRepo.endedTurns[0] != 8 || len(runtime.transitions) != 2 ||
		runtime.transitions[0].to != model.RoomStatusPaused || runtime.transitions[1].to != model.RoomStatusEnded ||
		len(publisher.events) != 1 || publisher.events[0] != string(model.RoomStatusEnded) || len(presence.userIDs) != 1 {
		t.Fatalf("end calls=%#v transitions=%#v publish=%#v presence=%#v", gameRepo.endedTurns, runtime.transitions, publisher.events, presence.userIDs)
	}
}

func TestFlushPendingMultiplayerAutoSavePersistsThenAcknowledges(t *testing.T) {
	gameRepo, runtime, service, _, _ := newMultiplayerLifecycleFixture(model.RoomStatusPlaying, 10)
	snapshot := validLifecycleMultiplayerSnapshot(model.RoomStatusPlaying, 10)
	runtime.pending = []model.PendingMultiplayerAutoSave{{Generation: snapshot.Generation, Snapshot: snapshot}}
	if err := service.FlushPendingMultiplayerAutoSaves(context.Background(), 41); err != nil {
		t.Fatalf("FlushPendingMultiplayerAutoSaves() error = %v", err)
	}
	if gameRepo.createdAutoSave == nil || gameRepo.createdAutoSave.RoundNumber != 5 || !gameRepo.createdAutoSave.IsAuto ||
		gameRepo.createdAutoSave.SaveName != "自动存档-5轮" || len(runtime.acknowledged) != 1 || runtime.acknowledged[0] != 5 {
		t.Fatalf("saved=%#v acknowledged=%#v", gameRepo.createdAutoSave, runtime.acknowledged)
	}
}
