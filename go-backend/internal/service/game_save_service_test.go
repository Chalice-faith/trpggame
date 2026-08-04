package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

func TestGameServiceCreateManualSave(t *testing.T) {
	gameRepository, runtimeRepository, gameService := manualSaveFixture(model.RoomStatusPlaying)

	result, err := gameService.CreateManualSave(context.Background(), &CreateManualSaveRequest{
		UserID: 7, RoomID: 41, SaveName: "  进入书房前  ",
	})

	if err != nil {
		t.Fatalf("CreateManualSave() error = %v", err)
	}
	if gameRepository.roomQueryID != 41 || gameRepository.roomQueryOwnerID != 7 ||
		runtimeRepository.captureRoomID != 41 || runtimeRepository.captureUserID != 7 {
		t.Fatalf("dependency queries = room(%d,%d), runtime(%d,%d)",
			gameRepository.roomQueryID, gameRepository.roomQueryOwnerID,
			runtimeRepository.captureRoomID, runtimeRepository.captureUserID)
	}
	if result == nil || result.Save == nil || result.Save != gameRepository.createdSave || result.Save.ID != 91 {
		t.Fatalf("result = %#v, created save = %#v", result, gameRepository.createdSave)
	}
	save := result.Save
	if save.RoomID != 41 || save.SaveName != "进入书房前" || save.RoundNumber != 3 ||
		save.SummaryMemory != "玩家进入了书房。" || save.IsAuto {
		t.Fatalf("save = %#v", save)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(save.RedisSnapshot, &snapshot); err != nil {
		t.Fatalf("decode Redis snapshot: %v", err)
	}
	if snapshot["version"] != float64(model.SoloRuntimeSnapshotVersion) || snapshot["turn"] != float64(3) ||
		snapshot["status"] != "playing" || snapshot["summary"] != nil || snapshot["recent_messages"] != nil ||
		snapshot["room_id"] != nil || snapshot["user_id"] != nil {
		t.Fatalf("encoded Redis snapshot = %#v", snapshot)
	}
	var messages []model.RuntimeMessage
	if err := json.Unmarshal(save.RecentMessages, &messages); err != nil || len(messages) != 3 ||
		messages[0].Role != "assistant" || messages[1].Role != "user" {
		t.Fatalf("recent messages = %#v, error = %v", messages, err)
	}
}

func TestGameServiceCreateManualSaveAllowsPausedRoom(t *testing.T) {
	_, _, gameService := manualSaveFixture(model.RoomStatusPaused)
	result, err := gameService.CreateManualSave(context.Background(), validManualSaveRequest())
	if err != nil || result == nil || result.Save == nil {
		t.Fatalf("paused save result = %#v, error = %v", result, err)
	}
}

func TestGameServiceCreateManualSaveRejectsInvalidRequest(t *testing.T) {
	tests := []*CreateManualSaveRequest{
		nil,
		{RoomID: 41, SaveName: "存档"},
		{UserID: 7, SaveName: "存档"},
		{UserID: 7, RoomID: 41},
		{UserID: 7, RoomID: 41, SaveName: "  "},
		{UserID: 7, RoomID: 41, SaveName: "存档\n伪造日志"},
		{UserID: 7, RoomID: 41, SaveName: strings.Repeat("档", 257)},
	}
	for index, request := range tests {
		gameRepository, runtimeRepository, gameService := manualSaveFixture(model.RoomStatusPlaying)
		if _, err := gameService.CreateManualSave(context.Background(), request); !errors.Is(err, ErrInvalidGameSave) {
			t.Fatalf("case %d error = %v", index, err)
		}
		if gameRepository.roomQueryID != 0 || runtimeRepository.captureRoomID != 0 ||
			gameRepository.createdSave != nil {
			t.Fatalf("case %d invalid request reached a dependency", index)
		}
	}
}

func TestGameServiceCreateManualSaveValidatesOwnedRoom(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*fakeGameRepository)
		want        error
		wantCapture bool
	}{
		{"room not found", func(repository *fakeGameRepository) {
			repository.roomErr = gorm.ErrRecordNotFound
		}, ErrGameRoomNotFound, false},
		{"room query failure", func(repository *fakeGameRepository) {
			repository.roomErr = errors.New("mysql unavailable")
		}, ErrInternal, false},
		{"nil room result", func(repository *fakeGameRepository) {
			repository.room = nil
		}, ErrInternal, false},
		{"mismatched owner", func(repository *fakeGameRepository) {
			repository.room.OwnerID = 8
		}, ErrInternal, false},
		{"waiting room", func(repository *fakeGameRepository) {
			repository.room.Status = model.RoomStatusWaiting
		}, ErrGameRoomNotSavable, false},
		{"ended room", func(repository *fakeGameRepository) {
			repository.room.Status = model.RoomStatusEnded
		}, ErrGameRoomNotSavable, false},
		{"multiplayer room", func(repository *fakeGameRepository) {
			repository.room.IsSolo = false
		}, ErrGameRoomNotSavable, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository, runtimeRepository, gameService := manualSaveFixture(model.RoomStatusPlaying)
			test.configure(gameRepository)
			_, err := gameService.CreateManualSave(context.Background(), validManualSaveRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if !test.wantCapture && runtimeRepository.captureRoomID != 0 {
				t.Fatal("invalid room reached Redis capture")
			}
			if gameRepository.createdSave != nil {
				t.Fatal("invalid room created a save")
			}
		})
	}
}

func TestGameServiceCreateManualSaveMapsRuntimeFailures(t *testing.T) {
	for _, runtimeErr := range []error{repo.ErrGameRuntimeUnavailable, repo.ErrInvalidGameRuntimeState} {
		gameRepository, runtimeRepository, gameService := manualSaveFixture(model.RoomStatusPlaying)
		runtimeRepository.captureErr = runtimeErr
		if _, err := gameService.CreateManualSave(context.Background(), validManualSaveRequest()); !errors.Is(err, ErrGameRuntimeUnavailable) {
			t.Fatalf("runtime error %v mapped to %v", runtimeErr, err)
		}
		if gameRepository.createdSave != nil {
			t.Fatal("runtime failure created a save")
		}
	}

	gameRepository, runtimeRepository, gameService := manualSaveFixture(model.RoomStatusPlaying)
	runtimeRepository.captureErr = errors.New("unexpected runtime error")
	if _, err := gameService.CreateManualSave(context.Background(), validManualSaveRequest()); !errors.Is(err, ErrInternal) {
		t.Fatalf("unexpected runtime error mapped to %v", err)
	}
	if gameRepository.createdSave != nil {
		t.Fatal("unexpected runtime failure created a save")
	}
}

func TestGameServiceCreateManualSaveRejectsInvalidCapturedSnapshot(t *testing.T) {
	tests := []func(*fakeGameRuntimeRepository){
		func(repository *fakeGameRuntimeRepository) { repository.captureResult = nil },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.Version = 0 },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.RoomID = 42 },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.UserID = 8 },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.Status = model.RoomStatusPaused },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.Turn = -1 },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.TurnOrder = []uint{8} },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.PlayerState = nil },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.Items = nil },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.Buffs = nil },
		func(repository *fakeGameRuntimeRepository) { repository.captureResult.RecentMessages = nil },
	}
	for index, configure := range tests {
		gameRepository, runtimeRepository, gameService := manualSaveFixture(model.RoomStatusPlaying)
		configure(runtimeRepository)
		if _, err := gameService.CreateManualSave(context.Background(), validManualSaveRequest()); !errors.Is(err, ErrGameRuntimeUnavailable) {
			t.Fatalf("case %d error = %v", index, err)
		}
		if gameRepository.createdSave != nil {
			t.Fatalf("case %d invalid snapshot created a save", index)
		}
	}
}

func TestGameServiceCreateManualSaveHandlesPersistenceFailures(t *testing.T) {
	gameRepository, _, gameService := manualSaveFixture(model.RoomStatusPlaying)
	gameRepository.createSaveErr = errors.New("mysql unavailable")
	if _, err := gameService.CreateManualSave(context.Background(), validManualSaveRequest()); !errors.Is(err, ErrInternal) {
		t.Fatalf("create failure error = %v", err)
	}

	gameRepository, _, gameService = manualSaveFixture(model.RoomStatusPlaying)
	gameRepository.assignSaveID = 0
	if _, err := gameService.CreateManualSave(context.Background(), validManualSaveRequest()); !errors.Is(err, ErrInternal) {
		t.Fatalf("empty save ID error = %v", err)
	}
}

func manualSaveFixture(
	status model.RoomStatus,
) (*fakeGameRepository, *fakeGameRuntimeRepository, *GameService) {
	gameRepository := &fakeGameRepository{
		room:         &model.GameRoom{ID: 41, OwnerID: 7, Status: status, IsSolo: true},
		assignSaveID: 91,
	}
	runtimeRepository := &fakeGameRuntimeRepository{captureResult: manualSaveSnapshotFixture(status)}
	gameService := NewGameService(
		gameRepository,
		&fakeGameScriptRepository{},
		&fakeGameInferenceClient{},
		runtimeRepository,
	)
	return gameRepository, runtimeRepository, gameService
}

func validManualSaveRequest() *CreateManualSaveRequest {
	return &CreateManualSaveRequest{UserID: 7, RoomID: 41, SaveName: "手动存档"}
}

func manualSaveSnapshotFixture(status model.RoomStatus) *model.SoloRuntimeSnapshot {
	return &model.SoloRuntimeSnapshot{
		Version:   model.SoloRuntimeSnapshotVersion,
		RoomID:    41,
		UserID:    7,
		Status:    status,
		Turn:      3,
		TurnOrder: []uint{7},
		PlayerState: map[string]string{
			"character_id": "13", "hp": "8", "location": "书房",
		},
		Items:   []model.RuntimeItem{{Name: "钥匙", Quantity: 1, Description: "黄铜钥匙"}},
		Buffs:   []model.RuntimeBuff{},
		Summary: "玩家进入了书房。",
		RecentMessages: []model.RuntimeMessage{
			{Role: "assistant", Content: "你找到了钥匙。"},
			{Role: "user", Content: "我调查书房。"},
			{Role: "assistant", Content: "你进入了古宅。"},
		},
	}
}
