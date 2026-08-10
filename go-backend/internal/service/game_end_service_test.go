package service

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

func TestGameServiceEndGameStopsActionsPersistsEndAndCleansRuntime(t *testing.T) {
	gameRepository, runtimeRepository, gameService := gameEndFixture(model.RoomStatusPlaying)

	result, err := gameService.EndGame(context.Background(), validEndGameRequest())

	if err != nil {
		t.Fatalf("EndGame() error = %v", err)
	}
	if result == nil || result.RoomID != 41 || result.Status != model.RoomStatusEnded {
		t.Fatalf("result = %#v", result)
	}
	if len(runtimeRepository.statusTransitions) != 1 ||
		runtimeRepository.statusTransitions[0].to != model.RoomStatusPaused {
		t.Fatalf("runtime transitions = %#v", runtimeRepository.statusTransitions)
	}
	if len(gameRepository.transitions) != 1 ||
		gameRepository.transitions[0].to != model.RoomStatusEnded {
		t.Fatalf("MySQL transitions = %#v", gameRepository.transitions)
	}
	if len(runtimeRepository.deleteCalls) != 1 || runtimeRepository.deleteCalls[0].roomID != 41 ||
		runtimeRepository.deleteCalls[0].userID != 7 {
		t.Fatalf("delete calls = %#v", runtimeRepository.deleteCalls)
	}
}

func TestGameServiceEndGameAcceptsPausedAndAlreadyEndedRoom(t *testing.T) {
	for _, status := range []model.RoomStatus{model.RoomStatusPaused, model.RoomStatusEnded} {
		gameRepository, runtimeRepository, gameService := gameEndFixture(status)
		result, err := gameService.EndGame(context.Background(), validEndGameRequest())
		if err != nil || result == nil || result.Status != model.RoomStatusEnded {
			t.Fatalf("status %s EndGame() = (%#v, %v)", status, result, err)
		}
		if status == model.RoomStatusEnded {
			if len(runtimeRepository.statusTransitions) != 0 || len(gameRepository.transitions) != 0 {
				t.Fatal("already ended room repeated status transitions")
			}
		}
		if len(runtimeRepository.deleteCalls) != 1 {
			t.Fatalf("status %s cleanup calls = %d", status, len(runtimeRepository.deleteCalls))
		}
	}
}

func TestGameServiceEndGameRejectsInvalidRequestBeforeDependencies(t *testing.T) {
	for _, request := range []*EndGameRequest{nil, {RoomID: 41}, {UserID: 7}} {
		gameRepository, runtimeRepository, gameService := gameEndFixture(model.RoomStatusPlaying)
		if _, err := gameService.EndGame(context.Background(), request); !errors.Is(err, ErrInvalidGameEnd) {
			t.Fatalf("request %#v error = %v", request, err)
		}
		if gameRepository.roomQueryID != 0 || len(runtimeRepository.statusTransitions) != 0 {
			t.Fatal("invalid request reached dependencies")
		}
	}
}

func TestGameServiceEndGameValidatesOwnedSoloRoom(t *testing.T) {
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
		}, ErrGameRoomNotEndable},
		{"waiting room", func(repository *fakeGameRepository) {
			repository.room.Status = model.RoomStatusWaiting
		}, ErrGameRoomNotEndable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository, runtimeRepository, gameService := gameEndFixture(model.RoomStatusPlaying)
			test.configure(gameRepository)
			_, err := gameService.EndGame(context.Background(), validEndGameRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("EndGame() error = %v, want %v", err, test.want)
			}
			if len(runtimeRepository.statusTransitions) != 0 || len(runtimeRepository.deleteCalls) != 0 {
				t.Fatal("invalid room reached runtime mutation")
			}
		})
	}
}

func TestGameServiceEndGameMapsRuntimePauseFailures(t *testing.T) {
	tests := []struct {
		name   string
		result runtimeStatusTransitionResult
		want   error
	}{
		{"status conflict", runtimeStatusTransitionResult{err: repo.ErrGameRuntimeStatusConflict}, ErrGameRoomNotEndable},
		{"runtime unavailable", runtimeStatusTransitionResult{err: repo.ErrGameRuntimeUnavailable}, ErrGameRuntimeUnavailable},
		{"missing update", runtimeStatusTransitionResult{}, ErrGameRuntimeUnavailable},
		{"unexpected", runtimeStatusTransitionResult{err: errors.New("unexpected")}, ErrInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository, runtimeRepository, gameService := gameEndFixture(model.RoomStatusPlaying)
			runtimeRepository.statusResults = []runtimeStatusTransitionResult{test.result}
			_, err := gameService.EndGame(context.Background(), validEndGameRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("EndGame() error = %v, want %v", err, test.want)
			}
			if len(gameRepository.transitions) != 0 || len(runtimeRepository.deleteCalls) != 0 {
				t.Fatal("failed runtime pause reached MySQL end or cleanup")
			}
		})
	}
}

func TestGameServiceEndGameRollsBackRuntimeWhenMySQLDoesNotEnd(t *testing.T) {
	for _, originalStatus := range []model.RoomStatus{model.RoomStatusPlaying, model.RoomStatusPaused} {
		gameRepository, runtimeRepository, gameService := gameEndFixture(originalStatus)
		gameRepository.transitionResults = []gameTransitionResult{{updated: false}}
		runtimeRepository.statusResults = []runtimeStatusTransitionResult{{updated: true}, {updated: true}}

		_, err := gameService.EndGame(context.Background(), validEndGameRequest())

		if !errors.Is(err, ErrGameRoomNotEndable) {
			t.Fatalf("status %s error = %v", originalStatus, err)
		}
		if len(runtimeRepository.statusTransitions) != 2 ||
			runtimeRepository.statusTransitions[1].from[0] != model.RoomStatusPaused ||
			runtimeRepository.statusTransitions[1].to != originalStatus {
			t.Fatalf("status %s transitions = %#v", originalStatus, runtimeRepository.statusTransitions)
		}
		if len(runtimeRepository.deleteCalls) != 0 {
			t.Fatal("failed MySQL end cleaned active runtime")
		}
	}
}

func TestGameServiceEndGameReconcilesAppliedMySQLTransition(t *testing.T) {
	gameRepository, runtimeRepository, gameService := gameEndFixture(model.RoomStatusPlaying)
	gameRepository.transitionResults = []gameTransitionResult{{updated: false, err: errors.New("response lost")}}
	gameRepository.transitionHook = func() { gameRepository.room.Status = model.RoomStatusEnded }

	result, err := gameService.EndGame(context.Background(), validEndGameRequest())

	if err != nil || result == nil || result.Status != model.RoomStatusEnded {
		t.Fatalf("EndGame() = (%#v, %v)", result, err)
	}
	if len(runtimeRepository.deleteCalls) != 1 {
		t.Fatalf("cleanup calls = %#v", runtimeRepository.deleteCalls)
	}
}

func TestGameServiceEndGameRetriesAmbiguousRuntimeCleanup(t *testing.T) {
	_, runtimeRepository, gameService := gameEndFixture(model.RoomStatusPlaying)
	runtimeRepository.deleteResults = []error{repo.ErrGameRuntimeUnavailable, nil}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := gameService.EndGame(ctx, validEndGameRequest())

	if err != nil || result == nil || result.Status != model.RoomStatusEnded {
		t.Fatalf("EndGame() = (%#v, %v)", result, err)
	}
	if len(runtimeRepository.deleteCalls) != 2 || runtimeRepository.deleteCalls[1].contextErr != nil {
		t.Fatalf("cleanup calls = %#v", runtimeRepository.deleteCalls)
	}
}

func TestGameServiceEndGameReportsRepeatedCleanupFailure(t *testing.T) {
	_, runtimeRepository, gameService := gameEndFixture(model.RoomStatusEnded)
	runtimeRepository.deleteResults = []error{
		repo.ErrGameRuntimeUnavailable,
		repo.ErrGameRuntimeUnavailable,
	}
	_, err := gameService.EndGame(context.Background(), validEndGameRequest())
	if !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("EndGame() error = %v", err)
	}
	if len(runtimeRepository.deleteCalls) != 2 {
		t.Fatalf("cleanup calls = %d", len(runtimeRepository.deleteCalls))
	}
}

func gameEndFixture(
	status model.RoomStatus,
) (*fakeGameRepository, *fakeGameRuntimeRepository, *GameService) {
	gameRepository := &fakeGameRepository{room: &model.GameRoom{
		ID: 41, OwnerID: 7, ScriptID: 11, IsSolo: true, Status: status,
	}}
	runtimeRepository := &fakeGameRuntimeRepository{}
	return gameRepository, runtimeRepository, NewGameService(
		gameRepository, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, runtimeRepository,
	)
}

func validEndGameRequest() *EndGameRequest {
	return &EndGameRequest{UserID: 7, RoomID: 41}
}
