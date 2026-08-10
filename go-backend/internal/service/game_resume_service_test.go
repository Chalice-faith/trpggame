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

func TestGameServiceResumeGameTransitionsMySQLBeforeRedis(t *testing.T) {
	events := make([]string, 0, 2)
	gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
	gameRepository.transitionHook = func() { events = append(events, "mysql") }
	runtimeRepository.statusTransitionHook = func() { events = append(events, "redis") }

	result, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())

	if err != nil {
		t.Fatalf("ResumeGame() error = %v", err)
	}
	if result == nil || result.RoomID != 41 || result.Status != model.RoomStatusPlaying {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(events, []string{"mysql", "redis"}) {
		t.Fatalf("transition order = %#v", events)
	}
	assertTransition(t, gameRepository.transitions, 0, model.RoomStatusPaused, model.RoomStatusPlaying)
	assertRuntimeStatusTransition(
		t,
		runtimeRepository.statusTransitions,
		0,
		[]model.RoomStatus{model.RoomStatusPaused, model.RoomStatusPlaying},
		model.RoomStatusPlaying,
	)
}

func TestGameServiceResumeGameRepairsRedisForAlreadyPlayingRoom(t *testing.T) {
	gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPlaying)

	result, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())

	if err != nil || result == nil || result.Status != model.RoomStatusPlaying {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(gameRepository.transitions) != 0 || len(runtimeRepository.statusTransitions) != 1 {
		t.Fatalf("MySQL transitions = %#v, Redis transitions = %#v",
			gameRepository.transitions, runtimeRepository.statusTransitions)
	}
}

func TestGameServiceResumeGameRejectsInvalidRequest(t *testing.T) {
	for _, request := range []*ResumeGameRequest{nil, {RoomID: 41}, {UserID: 7}} {
		gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
		if _, err := gameService.ResumeGame(context.Background(), request); !errors.Is(err, ErrInvalidGameResume) {
			t.Fatalf("request %#v error = %v", request, err)
		}
		if gameRepository.roomQueryID != 0 || len(runtimeRepository.statusTransitions) != 0 {
			t.Fatal("invalid request reached a dependency")
		}
	}
}

func TestGameServiceResumeGameValidatesOwnedRoom(t *testing.T) {
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
		{"nil room", func(repository *fakeGameRepository) { repository.room = nil }, ErrInternal},
		{"mismatched room", func(repository *fakeGameRepository) { repository.room.ID = 42 }, ErrInternal},
		{"mismatched owner", func(repository *fakeGameRepository) { repository.room.OwnerID = 8 }, ErrInternal},
		{"waiting room", func(repository *fakeGameRepository) {
			repository.room.Status = model.RoomStatusWaiting
		}, ErrGameRoomNotResumable},
		{"ended room", func(repository *fakeGameRepository) {
			repository.room.Status = model.RoomStatusEnded
		}, ErrGameRoomNotResumable},
		{"multiplayer room", func(repository *fakeGameRepository) {
			repository.room.IsSolo = false
		}, ErrGameRoomNotResumable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
			test.configure(gameRepository)
			_, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(gameRepository.transitions) != 0 || len(runtimeRepository.statusTransitions) != 0 {
				t.Fatal("invalid room reached a status transition")
			}
		})
	}
}

func TestGameServiceResumeGameHandlesMySQLTransitionOutcomes(t *testing.T) {
	t.Run("ambiguous success", func(t *testing.T) {
		gameRepository, _, gameService := resumeGameFixture(model.RoomStatusPaused)
		gameRepository.roomResults = []gameRoomQueryResult{
			{room: pauseRoom(model.RoomStatusPaused)},
			{room: pauseRoom(model.RoomStatusPlaying)},
		}
		gameRepository.transitionResults = []gameTransitionResult{{err: errors.New("response lost")}}

		result, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())
		if err != nil || result == nil || result.Status != model.RoomStatusPlaying {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})

	t.Run("conflict remains paused", func(t *testing.T) {
		gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
		gameRepository.transitionResults = []gameTransitionResult{{}}
		_, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())
		if !errors.Is(err, ErrGameRoomNotResumable) {
			t.Fatalf("error = %v", err)
		}
		if len(runtimeRepository.statusTransitions) != 0 {
			t.Fatal("failed MySQL transition reached Redis")
		}
	})

	t.Run("failure remains paused", func(t *testing.T) {
		gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
		gameRepository.transitionResults = []gameTransitionResult{{err: errors.New("mysql unavailable")}}
		_, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())
		if !errors.Is(err, ErrInternal) {
			t.Fatalf("error = %v", err)
		}
		if len(runtimeRepository.statusTransitions) != 0 {
			t.Fatal("failed MySQL transition reached Redis")
		}
	})
}

func TestGameServiceResumeGameAcceptsAmbiguousRedisSuccess(t *testing.T) {
	gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
	runtimeRepository.statusResults = []runtimeStatusTransitionResult{{err: repo.ErrGameRuntimeUnavailable}}
	runtimeRepository.captureResult = resumeSnapshot(model.RoomStatusPlaying)

	result, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())

	if err != nil || result == nil || result.Status != model.RoomStatusPlaying {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(gameRepository.transitions) != 1 {
		t.Fatalf("unexpected MySQL rollback: %#v", gameRepository.transitions)
	}
}

func TestGameServiceResumeGameRollsBackMySQLWhenRedisRemainsPaused(t *testing.T) {
	gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
	runtimeRepository.statusResults = []runtimeStatusTransitionResult{{err: repo.ErrGameRuntimeUnavailable}}
	runtimeRepository.captureResult = resumeSnapshot(model.RoomStatusPaused)

	_, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())

	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if len(gameRepository.transitions) != 2 {
		t.Fatalf("MySQL transitions = %#v", gameRepository.transitions)
	}
	assertTransition(t, gameRepository.transitions, 1, model.RoomStatusPlaying, model.RoomStatusPaused)
	if gameRepository.transitions[1].contextErr != nil || runtimeRepository.captureContextErr != nil {
		t.Fatalf("reconcile contexts = transition:%v capture:%v",
			gameRepository.transitions[1].contextErr, runtimeRepository.captureContextErr)
	}
}

func TestGameServiceResumeGameDoesNotRollBackPreexistingPlayingRoom(t *testing.T) {
	gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPlaying)
	runtimeRepository.statusResults = []runtimeStatusTransitionResult{{err: repo.ErrGameRuntimeUnavailable}}
	runtimeRepository.captureResult = resumeSnapshot(model.RoomStatusPaused)

	_, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())

	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if len(gameRepository.transitions) != 0 {
		t.Fatalf("preexisting playing room was rolled back: %#v", gameRepository.transitions)
	}
}

func TestGameServiceResumeGameLeavesMySQLPlayingWhenRedisStateUnknown(t *testing.T) {
	gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
	runtimeRepository.statusResults = []runtimeStatusTransitionResult{{err: repo.ErrGameRuntimeUnavailable}}
	runtimeRepository.captureErr = repo.ErrGameRuntimeUnavailable

	_, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())

	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if len(gameRepository.transitions) != 1 {
		t.Fatalf("unknown Redis state triggered unsafe rollback: %#v", gameRepository.transitions)
	}
}

func TestGameServiceResumeGameReportsMySQLRollbackFailure(t *testing.T) {
	gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
	gameRepository.roomResults = []gameRoomQueryResult{
		{room: pauseRoom(model.RoomStatusPaused)},
		{room: pauseRoom(model.RoomStatusPlaying)},
	}
	gameRepository.transitionResults = []gameTransitionResult{
		{updated: true},
		{err: errors.New("rollback unavailable")},
	}
	runtimeRepository.statusResults = []runtimeStatusTransitionResult{{err: repo.ErrGameRuntimeUnavailable}}
	runtimeRepository.captureResult = resumeSnapshot(model.RoomStatusPaused)

	_, err := gameService.ResumeGame(context.Background(), validResumeGameRequest())

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("error = %v", err)
	}
}

func TestGameServiceResumeGameReconcilesAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gameRepository, runtimeRepository, gameService := resumeGameFixture(model.RoomStatusPaused)
	gameRepository.roomResults = []gameRoomQueryResult{
		{room: pauseRoom(model.RoomStatusPaused)},
		{room: pauseRoom(model.RoomStatusPlaying)},
	}
	gameRepository.transitionResults = []gameTransitionResult{{err: errors.New("response lost")}}
	gameRepository.transitionHook = cancel
	runtimeRepository.statusResults = []runtimeStatusTransitionResult{{err: repo.ErrGameRuntimeUnavailable}}
	runtimeRepository.captureResult = resumeSnapshot(model.RoomStatusPlaying)

	result, err := gameService.ResumeGame(ctx, validResumeGameRequest())

	if err != nil || result == nil || result.Status != model.RoomStatusPlaying {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if len(gameRepository.roomQueryContexts) != 2 || gameRepository.roomQueryContexts[1] != nil ||
		runtimeRepository.captureContextErr != nil {
		t.Fatalf("reconcile context errors = room:%#v capture:%v",
			gameRepository.roomQueryContexts, runtimeRepository.captureContextErr)
	}
}

func resumeGameFixture(
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

func resumeSnapshot(status model.RoomStatus) *model.SoloRuntimeSnapshot {
	return &model.SoloRuntimeSnapshot{RoomID: 41, UserID: 7, Status: status}
}

func validResumeGameRequest() *ResumeGameRequest {
	return &ResumeGameRequest{UserID: 7, RoomID: 41}
}
