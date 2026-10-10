package service

import (
	"context"
	"errors"
	"testing"

	"trpggame/internal/model"
)

func TestGameServiceCreateAutomaticGameSave(t *testing.T) {
	gameRepository := &fakeGameRepository{autoSaveCreated: true}
	gameService := NewGameService(
		gameRepository, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, &fakeGameRuntimeRepository{},
	)
	snapshot := automaticSaveSnapshot()

	if err := gameService.createAutomaticGameSave(context.Background(), snapshot); err != nil {
		t.Fatalf("createAutomaticGameSave() error = %v", err)
	}
	save := gameRepository.createdAutoSave
	if save == nil || save.RoomID != 41 || save.SaveName != "自动存档-10" ||
		save.RoundNumber != 10 || save.SummaryMemory != "第十回合" || !save.IsAuto ||
		len(save.RedisSnapshot) == 0 || len(save.RecentMessages) == 0 {
		t.Fatalf("automatic save = %#v", save)
	}
}

func TestGameServiceCreateAutomaticGameSaveTreatsDuplicateAsSuccess(t *testing.T) {
	gameRepository := &fakeGameRepository{autoSaveCreated: false}
	gameService := NewGameService(
		gameRepository, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, &fakeGameRuntimeRepository{},
	)
	if err := gameService.createAutomaticGameSave(context.Background(), automaticSaveSnapshot()); err != nil {
		t.Fatalf("duplicate automatic save error = %v", err)
	}
}

func TestGameServiceCreateAutomaticGameSaveRejectsInvalidSnapshot(t *testing.T) {
	tests := []*model.SoloRuntimeSnapshot{
		nil,
		gameLoadSnapshot(9),
		gameLoadSnapshot(10),
	}
	tests[2].Status = model.RoomStatusPaused
	for index, snapshot := range tests {
		gameRepository := &fakeGameRepository{autoSaveCreated: true}
		gameService := NewGameService(
			gameRepository, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, &fakeGameRuntimeRepository{},
		)
		if err := gameService.createAutomaticGameSave(context.Background(), snapshot); !errors.Is(err, ErrInternal) {
			t.Fatalf("case %d error = %v", index, err)
		}
		if gameRepository.createdAutoSave != nil {
			t.Fatalf("case %d invalid snapshot reached repository", index)
		}
	}
}

func TestGameServiceCreateAutomaticGameSaveMapsPersistenceFailure(t *testing.T) {
	gameRepository := &fakeGameRepository{createAutoSaveErr: errors.New("mysql unavailable")}
	gameService := NewGameService(
		gameRepository, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, &fakeGameRuntimeRepository{},
	)
	if err := gameService.createAutomaticGameSave(
		context.Background(), automaticSaveSnapshot(),
	); !errors.Is(err, ErrInternal) {
		t.Fatalf("persistence error = %v", err)
	}
}

func TestGameServiceFlushPendingAutomaticGameSavesPersistsAndAcknowledges(t *testing.T) {
	gameRepository := &fakeGameRepository{autoSaveCreated: true}
	runtimeRepository := &fakeGameRuntimeRepository{pendingAutoSaves: []model.PendingAutoSave{{
		Generation: "11111111-1111-4111-8111-111111111111",
		Snapshot:   automaticSaveSnapshot(),
	}}}
	gameService := NewGameService(
		gameRepository, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, runtimeRepository,
	)

	if err := gameService.flushPendingAutomaticGameSaves(context.Background(), 41, 7); err != nil {
		t.Fatalf("flushPendingAutomaticGameSaves() error = %v", err)
	}
	if gameRepository.createdAutoSave == nil || len(runtimeRepository.acknowledgedAutoSaves) != 1 ||
		len(runtimeRepository.pendingAutoSaves) != 0 {
		t.Fatalf("save = %#v, acknowledged = %#v, pending = %#v",
			gameRepository.createdAutoSave,
			runtimeRepository.acknowledgedAutoSaves,
			runtimeRepository.pendingAutoSaves,
		)
	}
}

func TestGameServiceFlushPendingAutomaticGameSavesRetainsFailedSave(t *testing.T) {
	gameRepository := &fakeGameRepository{createAutoSaveErr: errors.New("mysql unavailable")}
	runtimeRepository := &fakeGameRuntimeRepository{pendingAutoSaves: []model.PendingAutoSave{{
		Generation: "11111111-1111-4111-8111-111111111111",
		Snapshot:   automaticSaveSnapshot(),
	}}}
	gameService := NewGameService(
		gameRepository, &fakeGameScriptRepository{}, &fakeGameInferenceClient{}, runtimeRepository,
	)

	if err := gameService.flushPendingAutomaticGameSaves(
		context.Background(), 41, 7,
	); !errors.Is(err, ErrInternal) {
		t.Fatalf("flush error = %v", err)
	}
	if len(runtimeRepository.acknowledgedAutoSaves) != 0 || len(runtimeRepository.pendingAutoSaves) != 1 {
		t.Fatalf("failed save was acknowledged: %#v", runtimeRepository)
	}
}

func automaticSaveSnapshot() *model.SoloRuntimeSnapshot {
	snapshot := gameLoadSnapshot(10)
	snapshot.Status = model.RoomStatusPlaying
	snapshot.Summary = "第十回合"
	return snapshot
}
