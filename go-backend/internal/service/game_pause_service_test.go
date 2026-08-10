package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

func TestGameServicePauseGameTransitionsRedisBeforeMySQL(t *testing.T) {
	events := make([]string, 0, 2)
	gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
	runtimeRepository.statusTransitionHook = func() { events = append(events, "redis") }
	gameRepository.transitionHook = func() { events = append(events, "mysql") }

	result, err := gameService.PauseGame(context.Background(), validPauseGameRequest())

	if err != nil {
		t.Fatalf("PauseGame() error = %v", err)
	}
	if result == nil || result.RoomID != 41 || result.Status != model.RoomStatusPaused {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(events, []string{"redis", "mysql"}) {
		t.Fatalf("transition order = %#v", events)
	}
	assertRuntimeStatusTransition(
		t,
		runtimeRepository.statusTransitions,
		0,
		[]model.RoomStatus{model.RoomStatusPlaying, model.RoomStatusPaused},
		model.RoomStatusPaused,
	)
	assertTransition(t, gameRepository.transitions, 0, model.RoomStatusPlaying, model.RoomStatusPaused)
}

func TestGameServicePauseGameIsIdempotentForPausedRoom(t *testing.T) {
	gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPaused)

	result, err := gameService.PauseGame(context.Background(), validPauseGameRequest())

	if err != nil || result == nil || result.Status != model.RoomStatusPaused {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(runtimeRepository.statusTransitions) != 1 || len(gameRepository.transitions) != 0 {
		t.Fatalf("runtime transitions = %#v, MySQL transitions = %#v",
			runtimeRepository.statusTransitions, gameRepository.transitions)
	}
}

func TestGameServicePauseGameRejectsInvalidRequest(t *testing.T) {
	for _, request := range []*PauseGameRequest{nil, {RoomID: 41}, {UserID: 7}} {
		gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
		if _, err := gameService.PauseGame(context.Background(), request); !errors.Is(err, ErrInvalidGamePause) {
			t.Fatalf("request %#v error = %v", request, err)
		}
		if gameRepository.roomQueryID != 0 || len(runtimeRepository.statusTransitions) != 0 {
			t.Fatal("invalid request reached a dependency")
		}
	}
}

func TestGameServicePauseGameValidatesOwnedRoom(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeGameRepository)
		want      error
	}{
		{"room not found", func(repository *fakeGameRepository) {
			repository.roomErr = gorm.ErrRecordNotFound
		}, ErrGameRoomNotFound},
		{"room query failure", func(repository *fakeGameRepository) {
			repository.roomErr = errors.New("mysql unavailable")
		}, ErrInternal},
		{"nil room", func(repository *fakeGameRepository) {
			repository.room = nil
		}, ErrInternal},
		{"mismatched room", func(repository *fakeGameRepository) {
			repository.room.ID = 42
		}, ErrInternal},
		{"mismatched owner", func(repository *fakeGameRepository) {
			repository.room.OwnerID = 8
		}, ErrInternal},
		{"waiting room", func(repository *fakeGameRepository) {
			repository.room.Status = model.RoomStatusWaiting
		}, ErrGameRoomNotPausable},
		{"ended room", func(repository *fakeGameRepository) {
			repository.room.Status = model.RoomStatusEnded
		}, ErrGameRoomNotPausable},
		{"multiplayer room", func(repository *fakeGameRepository) {
			repository.room.IsSolo = false
		}, ErrGameRoomNotPausable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
			test.configure(gameRepository)
			_, err := gameService.PauseGame(context.Background(), validPauseGameRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(runtimeRepository.statusTransitions) != 0 || len(gameRepository.transitions) != 0 {
				t.Fatal("invalid room reached a status transition")
			}
		})
	}
}

func TestGameServicePauseGameMapsRuntimeFailuresBeforeMySQL(t *testing.T) {
	tests := []struct {
		name    string
		result  runtimeStatusTransitionResult
		wantErr error
	}{
		{"status conflict", runtimeStatusTransitionResult{err: repo.ErrGameRuntimeStatusConflict}, ErrGameRoomNotPausable},
		{"runtime unavailable", runtimeStatusTransitionResult{err: repo.ErrGameRuntimeUnavailable}, ErrGameRuntimeUnavailable},
		{"invalid runtime", runtimeStatusTransitionResult{err: repo.ErrInvalidGameRuntimeState}, ErrGameRuntimeUnavailable},
		{"empty update", runtimeStatusTransitionResult{}, ErrGameRuntimeUnavailable},
		{"unexpected error", runtimeStatusTransitionResult{err: errors.New("unexpected")}, ErrInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
			runtimeRepository.statusResults = []runtimeStatusTransitionResult{test.result}
			_, err := gameService.PauseGame(context.Background(), validPauseGameRequest())
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if len(gameRepository.transitions) != 0 {
				t.Fatal("Redis failure reached MySQL transition")
			}
		})
	}
}

func TestGameServicePauseGameAcceptsAmbiguousMySQLSuccess(t *testing.T) {
	gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
	gameRepository.roomResults = []gameRoomQueryResult{
		{room: pauseRoom(model.RoomStatusPlaying)},
		{room: pauseRoom(model.RoomStatusPaused)},
	}
	gameRepository.transitionResults = []gameTransitionResult{{err: errors.New("response lost")}}

	result, err := gameService.PauseGame(context.Background(), validPauseGameRequest())

	if err != nil || result == nil || result.Status != model.RoomStatusPaused {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(runtimeRepository.statusTransitions) != 1 {
		t.Fatalf("unexpected Redis compensation: %#v", runtimeRepository.statusTransitions)
	}
}

func TestGameServicePauseGameRollsBackRedisWhenMySQLRemainsPlaying(t *testing.T) {
	tests := []struct {
		name       string
		transition gameTransitionResult
		want       error
	}{
		{"status conflict", gameTransitionResult{}, ErrGameRoomNotPausable},
		{"MySQL failure", gameTransitionResult{err: errors.New("mysql unavailable")}, ErrInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
			gameRepository.transitionResults = []gameTransitionResult{test.transition}
			_, err := gameService.PauseGame(context.Background(), validPauseGameRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(runtimeRepository.statusTransitions) != 2 {
				t.Fatalf("runtime transitions = %#v", runtimeRepository.statusTransitions)
			}
			assertRuntimeStatusTransition(
				t,
				runtimeRepository.statusTransitions,
				1,
				[]model.RoomStatus{model.RoomStatusPaused},
				model.RoomStatusPlaying,
			)
			if runtimeRepository.statusTransitions[1].contextErr != nil {
				t.Fatalf("rollback context error = %v", runtimeRepository.statusTransitions[1].contextErr)
			}
		})
	}
}

func TestGameServicePauseGameKeepsRedisPausedWhenRoomMovedForward(t *testing.T) {
	gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
	gameRepository.roomResults = []gameRoomQueryResult{
		{room: pauseRoom(model.RoomStatusPlaying)},
		{room: pauseRoom(model.RoomStatusEnded)},
	}
	gameRepository.transitionResults = []gameTransitionResult{{}}

	_, err := gameService.PauseGame(context.Background(), validPauseGameRequest())

	if !errors.Is(err, ErrGameRoomNotPausable) {
		t.Fatalf("error = %v", err)
	}
	if len(runtimeRepository.statusTransitions) != 1 {
		t.Fatalf("unsafe Redis rollback occurred: %#v", runtimeRepository.statusTransitions)
	}
}

func TestGameServicePauseGameReportsReconcileAndRollbackFailures(t *testing.T) {
	t.Run("reconcile query failure", func(t *testing.T) {
		gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
		gameRepository.roomResults = []gameRoomQueryResult{
			{room: pauseRoom(model.RoomStatusPlaying)},
			{err: errors.New("mysql unavailable")},
		}
		gameRepository.transitionResults = []gameTransitionResult{{err: errors.New("response lost")}}
		_, err := gameService.PauseGame(context.Background(), validPauseGameRequest())
		if !errors.Is(err, ErrInternal) || len(runtimeRepository.statusTransitions) != 1 {
			t.Fatalf("error = %v, transitions = %#v", err, runtimeRepository.statusTransitions)
		}
	})

	t.Run("Redis rollback failure", func(t *testing.T) {
		gameRepository, runtimeRepository, gameService := pauseGameFixture(model.RoomStatusPlaying)
		gameRepository.transitionResults = []gameTransitionResult{{err: errors.New("mysql unavailable")}}
		runtimeRepository.statusResults = []runtimeStatusTransitionResult{
			{updated: true},
			{err: repo.ErrGameRuntimeUnavailable},
		}
		_, err := gameService.PauseGame(context.Background(), validPauseGameRequest())
		if !errors.Is(err, ErrInternal) || len(runtimeRepository.statusTransitions) != 2 {
			t.Fatalf("error = %v, transitions = %#v", err, runtimeRepository.statusTransitions)
		}
	})
}

func TestGameServicePauseGameReconcilesAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gameRepository, _, gameService := pauseGameFixture(model.RoomStatusPlaying)
	gameRepository.roomResults = []gameRoomQueryResult{
		{room: pauseRoom(model.RoomStatusPlaying)},
		{room: pauseRoom(model.RoomStatusPaused)},
	}
	gameRepository.transitionResults = []gameTransitionResult{{err: errors.New("response lost")}}
	gameRepository.transitionHook = cancel

	result, err := gameService.PauseGame(ctx, validPauseGameRequest())

	if err != nil || result == nil || result.Status != model.RoomStatusPaused {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(gameRepository.roomQueryContexts) != 2 || gameRepository.roomQueryContexts[1] != nil {
		t.Fatalf("room query context errors = %#v", gameRepository.roomQueryContexts)
	}
}

func pauseGameFixture(
	status model.RoomStatus,
) (*fakeGameRepository, *fakeGameRuntimeRepository, *GameService) {
	gameRepository := &fakeGameRepository{room: pauseRoom(status)}
	runtimeRepository := &fakeGameRuntimeRepository{}
	gameService := NewGameService(
		gameRepository,
		&fakeGameScriptRepository{},
		&fakeGameInferenceClient{},
		runtimeRepository,
	)
	return gameRepository, runtimeRepository, gameService
}

func pauseRoom(status model.RoomStatus) *model.GameRoom {
	return &model.GameRoom{ID: 41, OwnerID: 7, Status: status, IsSolo: true}
}

func validPauseGameRequest() *PauseGameRequest {
	return &PauseGameRequest{UserID: 7, RoomID: 41}
}

func assertRuntimeStatusTransition(
	t *testing.T,
	transitions []runtimeStatusTransitionCall,
	index int,
	from []model.RoomStatus,
	to model.RoomStatus,
) {
	t.Helper()
	if len(transitions) <= index {
		t.Fatalf("missing runtime transition %d: %#v", index, transitions)
	}
	transition := transitions[index]
	if transition.roomID != 41 || transition.userID != 7 || transition.to != to ||
		!reflect.DeepEqual(transition.from, from) {
		t.Fatalf("runtime transition %d = %#v", index, transition)
	}
}
