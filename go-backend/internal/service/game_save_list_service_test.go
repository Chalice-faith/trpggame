package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"trpggame/internal/model"
)

func TestGameServiceListGameSavesReturnsSafeSummaries(t *testing.T) {
	createdLater := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	createdEarlier := createdLater.Add(-time.Hour)
	gameRepository := &fakeGameRepository{
		room: &model.GameRoom{
			ID: 41, OwnerID: 7, Status: model.RoomStatusEnded, IsSolo: true,
		},
		listedSaves: []model.GameSave{
			{
				ID: 92, RoomID: 41, SaveName: "  自动存档-10  ", RoundNumber: 10,
				IsAuto: true, CreatedAt: createdLater,
				RedisSnapshot:  json.RawMessage(`{"secret":"must-not-leak"}`),
				RecentMessages: json.RawMessage(`[{"content":"must-not-leak"}]`),
			},
			{
				ID: 91, RoomID: 41, SaveName: "进入书房前", RoundNumber: 3,
				CreatedAt: createdEarlier,
			},
		},
	}
	gameService := newSaveListService(gameRepository)

	result, err := gameService.ListGameSaves(context.Background(), &ListGameSavesRequest{
		UserID: 7, RoomID: 41,
	})

	if err != nil {
		t.Fatalf("ListGameSaves() error = %v", err)
	}
	if gameRepository.roomQueryID != 41 || gameRepository.roomQueryOwnerID != 7 ||
		gameRepository.listSavesRoomID != 41 {
		t.Fatalf("queries = room(%d,%d), saves(%d)",
			gameRepository.roomQueryID, gameRepository.roomQueryOwnerID,
			gameRepository.listSavesRoomID)
	}
	if result == nil || result.Total != 2 || len(result.Items) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Items[0].ID != 92 || result.Items[0].SaveName != "自动存档-10" ||
		result.Items[0].RoundNumber != 10 || !result.Items[0].IsAuto ||
		!result.Items[0].CreatedAt.Equal(createdLater) || result.Items[1].ID != 91 {
		t.Fatalf("items = %#v", result.Items)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(encoded) == "" || jsonContainsAny(encoded, "redis_snapshot", "recent_messages", "must-not-leak") {
		t.Fatalf("safe summary leaked snapshot data: %s", encoded)
	}
}

func TestGameServiceListGameSavesReturnsNonNilEmptyList(t *testing.T) {
	gameRepository := validSaveListRepository()
	gameRepository.listedSaves = nil

	result, err := newSaveListService(gameRepository).ListGameSaves(
		context.Background(),
		&ListGameSavesRequest{UserID: 7, RoomID: 41},
	)

	if err != nil {
		t.Fatalf("ListGameSaves() error = %v", err)
	}
	if result == nil || result.Items == nil || len(result.Items) != 0 || result.Total != 0 {
		t.Fatalf("empty result = %#v", result)
	}
}

func TestGameServiceListGameSavesRejectsInvalidRequest(t *testing.T) {
	for _, request := range []*ListGameSavesRequest{
		nil,
		{RoomID: 41},
		{UserID: 7},
	} {
		gameRepository := validSaveListRepository()
		_, err := newSaveListService(gameRepository).ListGameSaves(context.Background(), request)
		if !errors.Is(err, ErrInvalidGameSaveQuery) {
			t.Fatalf("request %#v error = %v", request, err)
		}
		if gameRepository.roomQueryID != 0 || gameRepository.listSavesRoomID != 0 {
			t.Fatal("invalid request reached a dependency")
		}
	}
}

func TestGameServiceListGameSavesValidatesOwnedRoom(t *testing.T) {
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository := validSaveListRepository()
			test.configure(gameRepository)
			_, err := newSaveListService(gameRepository).ListGameSaves(
				context.Background(),
				&ListGameSavesRequest{UserID: 7, RoomID: 41},
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if gameRepository.listSavesRoomID != 0 {
				t.Fatal("invalid room reached save list query")
			}
		})
	}
}

func TestGameServiceListGameSavesMapsRepositoryFailure(t *testing.T) {
	gameRepository := validSaveListRepository()
	gameRepository.listSavesErr = errors.New("mysql unavailable")

	_, err := newSaveListService(gameRepository).ListGameSaves(
		context.Background(),
		&ListGameSavesRequest{UserID: 7, RoomID: 41},
	)

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("list failure error = %v", err)
	}
}

func TestGameServiceListGameSavesRejectsInvalidRepositoryResults(t *testing.T) {
	createdAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []model.GameSave{
		{RoomID: 41, SaveName: "存档", CreatedAt: createdAt},
		{ID: 91, RoomID: 42, SaveName: "存档", CreatedAt: createdAt},
		{ID: 91, RoomID: 41, SaveName: "  ", CreatedAt: createdAt},
		{ID: 91, RoomID: 41, SaveName: "存档", RoundNumber: -1, CreatedAt: createdAt},
		{ID: 91, RoomID: 41, SaveName: "存档"},
	}
	for index, save := range tests {
		gameRepository := validSaveListRepository()
		gameRepository.listedSaves = []model.GameSave{save}
		_, err := newSaveListService(gameRepository).ListGameSaves(
			context.Background(),
			&ListGameSavesRequest{UserID: 7, RoomID: 41},
		)
		if !errors.Is(err, ErrInternal) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
}

func validSaveListRepository() *fakeGameRepository {
	return &fakeGameRepository{
		room: &model.GameRoom{ID: 41, OwnerID: 7, Status: model.RoomStatusEnded, IsSolo: true},
		listedSaves: []model.GameSave{{
			ID: 91, RoomID: 41, SaveName: "存档", RoundNumber: 3,
			CreatedAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		}},
	}
}

func newSaveListService(gameRepository *fakeGameRepository) *GameService {
	return NewGameService(
		gameRepository,
		&fakeGameScriptRepository{},
		&fakeGameInferenceClient{},
		&fakeGameRuntimeRepository{},
	)
}

func jsonContainsAny(encoded []byte, candidates ...string) bool {
	text := string(encoded)
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}
