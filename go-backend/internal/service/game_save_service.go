package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

const maxManualSaveNameLength = 256

// CreateManualSaveRequest 是手动存档的服务层请求。
type CreateManualSaveRequest struct {
	UserID   uint
	RoomID   uint
	SaveName string
}

// CreateManualSaveResult 是手动存档的服务层结果。
type CreateManualSaveResult struct {
	Save *model.GameSave
}

// CreateManualSave 校验房间所有权，捕获 Redis 一致性快照并持久化到 MySQL。
func (s *GameService) CreateManualSave(
	ctx context.Context,
	req *CreateManualSaveRequest,
) (*CreateManualSaveResult, error) {
	saveName, err := validateManualSaveRequest(req)
	if err != nil {
		return nil, err
	}

	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, req.RoomID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find room for save: %v", ErrInternal, err)
	}
	if room == nil || room.ID != req.RoomID || room.OwnerID != req.UserID {
		return nil, fmt.Errorf("%w: invalid room repository result", ErrInternal)
	}
	if !room.IsSolo || (room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused) {
		return nil, ErrGameRoomNotSavable
	}

	snapshot, err := s.runtimeRepo.CaptureSoloRoom(ctx, room.ID, req.UserID)
	if err != nil {
		switch {
		case errors.Is(err, repo.ErrGameRuntimeUnavailable),
			errors.Is(err, repo.ErrInvalidGameRuntimeState):
			return nil, ErrGameRuntimeUnavailable
		default:
			return nil, fmt.Errorf("%w: capture game save: %v", ErrInternal, err)
		}
	}
	if !validManualSaveSnapshot(snapshot, room, req.UserID) {
		return nil, fmt.Errorf("%w: invalid captured game snapshot", ErrGameRuntimeUnavailable)
	}

	redisSnapshot, err := json.Marshal(snapshot)
	if err != nil || !json.Valid(redisSnapshot) || len(redisSnapshot) == 0 || redisSnapshot[0] != '{' {
		return nil, fmt.Errorf("%w: encode Redis snapshot", ErrInternal)
	}
	recentMessages, err := json.Marshal(snapshot.RecentMessages)
	if err != nil || !json.Valid(recentMessages) || len(recentMessages) == 0 || recentMessages[0] != '[' {
		return nil, fmt.Errorf("%w: encode recent messages", ErrInternal)
	}

	save := &model.GameSave{
		RoomID:         room.ID,
		SaveName:       saveName,
		RoundNumber:    snapshot.Turn,
		SummaryMemory:  snapshot.Summary,
		RedisSnapshot:  append(json.RawMessage(nil), redisSnapshot...),
		RecentMessages: append(json.RawMessage(nil), recentMessages...),
		IsAuto:         false,
	}
	if err := s.gameRepo.CreateSave(ctx, save); err != nil {
		return nil, fmt.Errorf("%w: create manual game save: %v", ErrInternal, err)
	}
	if save.ID == 0 {
		return nil, fmt.Errorf("%w: game save repository returned an empty ID", ErrInternal)
	}
	return &CreateManualSaveResult{Save: save}, nil
}

func validateManualSaveRequest(req *CreateManualSaveRequest) (string, error) {
	if req == nil || req.UserID == 0 || req.RoomID == 0 {
		return "", ErrInvalidGameSave
	}
	saveName := strings.TrimSpace(req.SaveName)
	if saveName == "" || utf8.RuneCountInString(saveName) > maxManualSaveNameLength {
		return "", ErrInvalidGameSave
	}
	for _, character := range saveName {
		if unicode.IsControl(character) {
			return "", ErrInvalidGameSave
		}
	}
	return saveName, nil
}

func validManualSaveSnapshot(
	snapshot *model.SoloRuntimeSnapshot,
	room *model.GameRoom,
	userID uint,
) bool {
	return snapshot != nil && snapshot.Version == model.SoloRuntimeSnapshotVersion &&
		snapshot.RoomID == room.ID && snapshot.UserID == userID && snapshot.Status == room.Status &&
		snapshot.Turn >= 0 && len(snapshot.Summary) <= 65535 &&
		snapshot.TurnOrder != nil && len(snapshot.TurnOrder) == 1 && snapshot.TurnOrder[0] == userID &&
		snapshot.PlayerState != nil && len(snapshot.PlayerState) > 0 &&
		snapshot.Items != nil && snapshot.Buffs != nil &&
		snapshot.RecentMessages != nil && len(snapshot.RecentMessages) > 0 &&
		len(snapshot.RecentMessages) <= 10
}
