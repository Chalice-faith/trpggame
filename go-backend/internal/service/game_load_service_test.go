package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

func TestGameServiceLoadGameRestoresValidatedSaveAsPaused(t *testing.T) {
	gameRepository, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPlaying)

	result, err := gameService.LoadGame(context.Background(), validLoadGameRequest())

	if err != nil {
		t.Fatalf("LoadGame() error = %v", err)
	}
	if result == nil || result.RoomID != 41 || result.SaveID != 91 ||
		result.Status != model.RoomStatusPaused || result.Turn != 3 {
		t.Fatalf("result = %#v", result)
	}
	if gameRepository.findSaveRoomID != 41 || gameRepository.findSaveID != 91 {
		t.Fatalf("save query = room:%d save:%d", gameRepository.findSaveRoomID, gameRepository.findSaveID)
	}
	if len(runtimeRepository.statusTransitions) != 1 || len(gameRepository.transitions) != 1 {
		t.Fatalf("pause transitions = redis:%#v mysql:%#v", runtimeRepository.statusTransitions, gameRepository.transitions)
	}
	want, err := decodeGameSaveSnapshot(gameRepository.foundSave, 41, 7, 91)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if !reflect.DeepEqual(runtimeRepository.restored, want) {
		t.Fatalf("restored = %#v, want %#v", runtimeRepository.restored, want)
	}
	if runtimeRepository.restored.Status != model.RoomStatusPaused {
		t.Fatalf("restored status = %s, want paused", runtimeRepository.restored.Status)
	}
	if len(gameRepository.replaceProgressCalls) != 1 || gameRepository.replaceProgressCalls[0].turn != 3 {
		t.Fatalf("replace progress calls = %#v, want turn 3", gameRepository.replaceProgressCalls)
	}
}

func TestGameServiceLoadGameAcceptsAlreadyPausedRoom(t *testing.T) {
	gameRepository, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPaused)
	result, err := gameService.LoadGame(context.Background(), validLoadGameRequest())
	if err != nil || result == nil || result.Status != model.RoomStatusPaused {
		t.Fatalf("LoadGame() = (%#v, %v)", result, err)
	}
	if len(gameRepository.transitions) != 0 || len(runtimeRepository.statusTransitions) != 1 {
		t.Fatalf("transitions = mysql:%#v redis:%#v", gameRepository.transitions, runtimeRepository.statusTransitions)
	}
}

func TestGameServiceLoadGameRejectsInvalidRequestBeforeDependencies(t *testing.T) {
	for _, request := range []*LoadGameRequest{
		nil,
		{RoomID: 41, SaveID: 91},
		{UserID: 7, SaveID: 91},
		{UserID: 7, RoomID: 41},
	} {
		gameRepository, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPlaying)
		if _, err := gameService.LoadGame(context.Background(), request); !errors.Is(err, ErrInvalidGameLoad) {
			t.Fatalf("request %#v error = %v", request, err)
		}
		if gameRepository.roomQueryID != 0 || gameRepository.findSaveID != 0 || runtimeRepository.restored != nil {
			t.Fatal("invalid request reached dependencies")
		}
	}
}

func TestGameServiceLoadGameValidatesRoomAndScopedSave(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeGameRepository)
		want      error
	}{
		{"room not found", func(repository *fakeGameRepository) {
			repository.room = nil
			repository.roomErr = gorm.ErrRecordNotFound
		}, ErrGameRoomNotFound},
		{"room query failure", func(repository *fakeGameRepository) {
			repository.roomErr = errors.New("mysql unavailable")
		}, ErrInternal},
		{"multiplayer room", func(repository *fakeGameRepository) {
			repository.room.IsSolo = false
		}, ErrGameRoomNotLoadable},
		{"ended room", func(repository *fakeGameRepository) {
			repository.room.Status = model.RoomStatusEnded
		}, ErrGameRoomNotLoadable},
		{"save not found", func(repository *fakeGameRepository) {
			repository.foundSave = nil
			repository.findSaveErr = gorm.ErrRecordNotFound
		}, ErrGameSaveNotFound},
		{"save query failure", func(repository *fakeGameRepository) {
			repository.findSaveErr = errors.New("mysql unavailable")
		}, ErrInternal},
		{"wrong save result", func(repository *fakeGameRepository) {
			repository.foundSave.RoomID = 42
		}, ErrInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPlaying)
			test.configure(gameRepository)
			_, err := gameService.LoadGame(context.Background(), validLoadGameRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("LoadGame() error = %v, want %v", err, test.want)
			}
			if runtimeRepository.restored != nil || len(runtimeRepository.statusTransitions) != 0 {
				t.Fatal("invalid room or save reached pause/restore")
			}
		})
	}
}

func TestGameServiceLoadGameRejectsCorruptSaveBeforePause(t *testing.T) {
	tests := []func(*model.GameSave){
		func(save *model.GameSave) { save.RedisSnapshot = json.RawMessage(`invalid`) },
		func(save *model.GameSave) { save.RedisSnapshot = json.RawMessage(`{"version":1,"status":"ended"}`) },
		func(save *model.GameSave) { save.RecentMessages = json.RawMessage(`null`) },
		func(save *model.GameSave) { save.RoundNumber = 4 },
		func(save *model.GameSave) {
			save.RedisSnapshot = append(save.RedisSnapshot[:len(save.RedisSnapshot)-1], []byte(`,"unknown":true}`)...)
		},
	}
	for index, mutate := range tests {
		gameRepository, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPlaying)
		mutate(gameRepository.foundSave)
		_, err := gameService.LoadGame(context.Background(), validLoadGameRequest())
		if !errors.Is(err, ErrGameSaveCorrupt) {
			t.Fatalf("case %d error = %v", index, err)
		}
		if len(runtimeRepository.statusTransitions) != 0 || len(gameRepository.transitions) != 0 ||
			runtimeRepository.restored != nil {
			t.Fatalf("case %d corrupt save changed runtime", index)
		}
	}
}

func TestGameServiceLoadGameStopsWhenPauseFails(t *testing.T) {
	_, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPlaying)
	runtimeRepository.statusResults = []runtimeStatusTransitionResult{{err: repo.ErrGameRuntimeStatusConflict}}

	_, err := gameService.LoadGame(context.Background(), validLoadGameRequest())

	if !errors.Is(err, ErrGameRoomNotLoadable) {
		t.Fatalf("LoadGame() error = %v", err)
	}
	if runtimeRepository.restored != nil {
		t.Fatal("failed pause reached restore")
	}
}

func TestGameServiceLoadGameReconcilesAmbiguousRedisRestore(t *testing.T) {
	gameRepository, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPlaying)
	want, err := decodeGameSaveSnapshot(gameRepository.foundSave, 41, 7, 91)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	runtimeRepository.restoreErr = fmtError(repo.ErrGameRuntimeUnavailable)
	runtimeRepository.captureResult = want

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := gameService.LoadGame(ctx, validLoadGameRequest())

	if err != nil || result == nil || result.Turn != 3 {
		t.Fatalf("LoadGame() = (%#v, %v)", result, err)
	}
	if runtimeRepository.captureContextErr != nil {
		t.Fatalf("reconcile context error = %v, want detached context", runtimeRepository.captureContextErr)
	}
}

func TestGameServiceLoadGameMapsRestoreFailures(t *testing.T) {
	tests := []struct {
		name       string
		restoreErr error
		capture    *model.SoloRuntimeSnapshot
		want       error
	}{
		{"ambiguous but not applied", fmtError(repo.ErrGameRuntimeUnavailable), gameLoadSnapshot(2), ErrGameRuntimeUnavailable},
		{"invalid runtime contract", repo.ErrInvalidGameRuntimeState, nil, ErrInternal},
		{"unexpected restore error", errors.New("unexpected"), nil, ErrInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPlaying)
			runtimeRepository.restoreErr = test.restoreErr
			runtimeRepository.captureResult = test.capture
			_, err := gameService.LoadGame(context.Background(), validLoadGameRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("LoadGame() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestGameServiceLoadGameFailsRetryablyWhenMySQLProgressCannotBeReplaced(t *testing.T) {
	gameRepository, runtimeRepository, gameService := gameLoadFixture(model.RoomStatusPlaying)
	gameRepository.replaceProgressResults = []gameTransitionResult{{err: errors.New("mysql unavailable")}}

	result, err := gameService.LoadGame(context.Background(), validLoadGameRequest())

	if result != nil || !errors.Is(err, ErrInternal) {
		t.Fatalf("LoadGame() = (%#v, %v), want retryable internal error", result, err)
	}
	if runtimeRepository.restored == nil {
		t.Fatal("Redis restore should complete before MySQL progress compensation")
	}
	if len(gameRepository.replaceProgressCalls) != 1 || gameRepository.replaceProgressCalls[0].turn != 3 {
		t.Fatalf("replace progress calls = %#v", gameRepository.replaceProgressCalls)
	}
}

func gameLoadFixture(
	status model.RoomStatus,
) (*fakeGameRepository, *fakeGameRuntimeRepository, *GameService) {
	gameRepository := &fakeGameRepository{
		room: &model.GameRoom{
			ID: 41, OwnerID: 7, ScriptID: 11, IsSolo: true, Status: status,
		},
		foundSave: gameLoadSave(),
	}
	runtimeRepository := &fakeGameRuntimeRepository{}
	return gameRepository, runtimeRepository, NewGameService(
		gameRepository, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, runtimeRepository,
	)
}

func gameLoadSave() *model.GameSave {
	snapshot := gameLoadSnapshot(3)
	snapshot.Status = model.RoomStatusPlaying
	redisSnapshot, _ := json.Marshal(snapshot)
	messages, _ := json.Marshal(snapshot.RecentMessages)
	return &model.GameSave{
		ID: 91, RoomID: 41, SaveName: "进入书房前", RoundNumber: 3,
		SummaryMemory: snapshot.Summary, RedisSnapshot: redisSnapshot, RecentMessages: messages,
	}
}

func gameLoadSnapshot(turn int) *model.SoloRuntimeSnapshot {
	return &model.SoloRuntimeSnapshot{
		Version: model.SoloRuntimeSnapshotVersion, RoomID: 41, UserID: 7,
		Status: model.RoomStatusPaused, Turn: turn, TurnOrder: []uint{7},
		Summary: "进入书房", PlayerState: map[string]string{"hp": "8", "max_hp": "10"},
		Items: []model.RuntimeItem{}, Buffs: []model.RuntimeBuff{},
		RecentMessages: []model.RuntimeMessage{{Role: "assistant", Content: "你进入书房。"}},
	}
}

func validLoadGameRequest() *LoadGameRequest {
	return &LoadGameRequest{UserID: 7, RoomID: 41, SaveID: 91}
}

func fmtError(err error) error {
	return &wrappedTestError{err: err}
}

type wrappedTestError struct{ err error }

func (e *wrappedTestError) Error() string { return "wrapped: " + e.err.Error() }
func (e *wrappedTestError) Unwrap() error { return e.err }
