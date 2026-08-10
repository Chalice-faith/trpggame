package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

// LoadGameRequest 是从指定房间存档恢复运行态的服务层请求。
type LoadGameRequest struct {
	UserID uint
	RoomID uint
	SaveID uint
}

// LoadGameResult 返回恢复后的安全运行态摘要。读档完成后房间保持暂停。
type LoadGameResult struct {
	RoomID uint             `json:"room_id"`
	SaveID uint             `json:"save_id"`
	Status model.RoomStatus `json:"status"`
	Turn   int              `json:"turn"`
}

// LoadGame 校验房间和存档归属，暂停当前时间线后原子恢复 Redis 快照。
// 恢复后的房间保持 paused，由客户端显式调用恢复接口继续游戏。
func (s *GameService) LoadGame(
	ctx context.Context,
	req *LoadGameRequest,
) (*LoadGameResult, error) {
	if req == nil || req.UserID == 0 || req.RoomID == 0 || req.SaveID == 0 {
		return nil, ErrInvalidGameLoad
	}

	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, req.RoomID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find room for load: %v", ErrInternal, err)
	}
	if room == nil || room.ID != req.RoomID || room.OwnerID != req.UserID {
		return nil, fmt.Errorf("%w: invalid room repository result", ErrInternal)
	}
	if !room.IsSolo || (room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused) {
		return nil, ErrGameRoomNotLoadable
	}

	save, err := s.gameRepo.FindSaveByID(ctx, room.ID, req.SaveID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameSaveNotFound
		}
		return nil, fmt.Errorf("%w: find game save for load: %v", ErrInternal, err)
	}
	snapshot, err := decodeGameSaveSnapshot(save, room.ID, req.UserID, req.SaveID)
	if err != nil {
		return nil, err
	}

	if _, err := s.PauseGame(ctx, &PauseGameRequest{UserID: req.UserID, RoomID: room.ID}); err != nil {
		switch {
		case errors.Is(err, ErrGameRoomNotFound):
			return nil, ErrGameRoomNotFound
		case errors.Is(err, ErrGameRoomNotPausable):
			return nil, ErrGameRoomNotLoadable
		default:
			return nil, err
		}
	}

	if err := s.runtimeRepo.RestoreSoloRoom(ctx, snapshot); err == nil {
		if err := s.replaceLoadedGameProgress(ctx, room.ID, req.UserID, snapshot.Turn); err != nil {
			return nil, err
		}
		return loadedGameResult(room.ID, save.ID, snapshot.Turn), nil
	} else if !errors.Is(err, repo.ErrGameRuntimeUnavailable) {
		if errors.Is(err, repo.ErrInvalidGameRuntimeState) {
			return nil, fmt.Errorf("%w: validated snapshot was rejected", ErrInternal)
		}
		return nil, fmt.Errorf("%w: restore game save: %v", ErrInternal, err)
	}

	return s.reconcileGameLoad(ctx, room.ID, req.UserID, save.ID, snapshot)
}

func decodeGameSaveSnapshot(
	save *model.GameSave,
	roomID uint,
	userID uint,
	saveID uint,
) (*model.SoloRuntimeSnapshot, error) {
	if save == nil || save.ID != saveID || save.RoomID != roomID || save.RoundNumber < 0 {
		return nil, fmt.Errorf("%w: invalid game save repository result", ErrInternal)
	}
	var snapshot model.SoloRuntimeSnapshot
	if decodeStrictJSON(save.RedisSnapshot, &snapshot) != nil {
		return nil, ErrGameSaveCorrupt
	}
	var messages []model.RuntimeMessage
	if decodeStrictJSON(save.RecentMessages, &messages) != nil {
		return nil, ErrGameSaveCorrupt
	}
	if snapshot.Status != model.RoomStatusPlaying && snapshot.Status != model.RoomStatusPaused {
		return nil, ErrGameSaveCorrupt
	}
	snapshot.RoomID = roomID
	snapshot.UserID = userID
	snapshot.Summary = save.SummaryMemory
	snapshot.RecentMessages = messages
	if snapshot.Turn != save.RoundNumber {
		return nil, ErrGameSaveCorrupt
	}
	snapshot.Status = model.RoomStatusPaused
	normalized, err := repo.NormalizeSoloRuntimeSnapshot(&snapshot)
	if err != nil {
		return nil, ErrGameSaveCorrupt
	}
	return normalized, nil
}

func (s *GameService) reconcileGameLoad(
	ctx context.Context,
	roomID uint,
	userID uint,
	saveID uint,
	want *model.SoloRuntimeSnapshot,
) (*LoadGameResult, error) {
	reconcileContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
	defer cancel()
	got, err := s.runtimeRepo.CaptureSoloRoom(reconcileContext, roomID, userID)
	if err != nil {
		return nil, ErrGameRuntimeUnavailable
	}
	normalized, normalizeErr := repo.NormalizeSoloRuntimeSnapshot(got)
	if normalizeErr != nil || !reflect.DeepEqual(normalized, want) {
		return nil, ErrGameRuntimeUnavailable
	}
	if err := s.replaceLoadedGameProgress(ctx, roomID, userID, want.Turn); err != nil {
		return nil, err
	}
	return loadedGameResult(roomID, saveID, want.Turn), nil
}

func loadedGameResult(roomID, saveID uint, turn int) *LoadGameResult {
	return &LoadGameResult{
		RoomID: roomID,
		SaveID: saveID,
		Status: model.RoomStatusPaused,
		Turn:   turn,
	}
}
