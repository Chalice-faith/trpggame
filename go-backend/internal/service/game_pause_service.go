package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

const pauseReconcileTimeout = 5 * time.Second

// PauseGameRequest 是暂停单人游戏的服务层请求。
type PauseGameRequest struct {
	UserID uint
	RoomID uint
}

// PauseGameResult 是暂停后的持久化房间状态。
type PauseGameResult struct {
	RoomID uint             `json:"room_id"`
	Status model.RoomStatus `json:"status"`
}

// PauseGame 先暂停 Redis 运行态以阻断行动提交，再同步 MySQL 持久状态。
func (s *GameService) PauseGame(
	ctx context.Context,
	req *PauseGameRequest,
) (*PauseGameResult, error) {
	if req == nil || req.UserID == 0 || req.RoomID == 0 {
		return nil, ErrInvalidGamePause
	}
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, req.RoomID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find room for pause: %v", ErrInternal, err)
	}
	if room == nil || room.ID != req.RoomID || room.OwnerID != req.UserID {
		return nil, fmt.Errorf("%w: invalid room repository result", ErrInternal)
	}
	if !room.IsSolo || (room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused) {
		return nil, ErrGameRoomNotPausable
	}

	updated, err := s.runtimeRepo.TransitionSoloRoomStatus(
		ctx,
		room.ID,
		req.UserID,
		[]model.RoomStatus{model.RoomStatusPlaying, model.RoomStatusPaused},
		model.RoomStatusPaused,
	)
	if err != nil || !updated {
		return nil, mapPauseRuntimeError(err)
	}
	if room.Status == model.RoomStatusPaused {
		return pausedGameResult(room.ID), nil
	}

	updated, transitionErr := s.gameRepo.TransitionRoomStatus(
		ctx,
		room.ID,
		req.UserID,
		[]model.RoomStatus{model.RoomStatusPlaying},
		model.RoomStatusPaused,
	)
	if transitionErr == nil && updated {
		return pausedGameResult(room.ID), nil
	}
	return s.reconcilePause(ctx, room.ID, req.UserID, transitionErr)
}

func (s *GameService) reconcilePause(
	ctx context.Context,
	roomID uint,
	userID uint,
	transitionErr error,
) (*PauseGameResult, error) {
	reconcileContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
	defer cancel()

	room, err := s.gameRepo.FindRoomByIDAndOwnerID(reconcileContext, roomID, userID)
	if err != nil || room == nil || room.ID != roomID || room.OwnerID != userID {
		return nil, fmt.Errorf(
			"%w: reconcile MySQL pause after transition error %v: %v",
			ErrInternal,
			transitionErr,
			err,
		)
	}
	switch room.Status {
	case model.RoomStatusPaused:
		return pausedGameResult(room.ID), nil
	case model.RoomStatusPlaying:
		updated, rollbackErr := s.runtimeRepo.TransitionSoloRoomStatus(
			reconcileContext,
			room.ID,
			userID,
			[]model.RoomStatus{model.RoomStatusPaused},
			model.RoomStatusPlaying,
		)
		if rollbackErr != nil || !updated {
			return nil, fmt.Errorf(
				"%w: MySQL pause failed (%v) and Redis rollback failed: %v",
				ErrInternal,
				transitionErr,
				rollbackErr,
			)
		}
		if transitionErr != nil {
			return nil, fmt.Errorf("%w: pause room in MySQL: %v", ErrInternal, transitionErr)
		}
		return nil, ErrGameRoomNotPausable
	default:
		// Redis 保持 paused 是最安全的失败状态，避免房间结束等竞态后继续接受行动。
		return nil, ErrGameRoomNotPausable
	}
}

func pausedGameResult(roomID uint) *PauseGameResult {
	return &PauseGameResult{RoomID: roomID, Status: model.RoomStatusPaused}
}

func mapPauseRuntimeError(err error) error {
	switch {
	case errors.Is(err, repo.ErrGameRuntimeStatusConflict):
		return ErrGameRoomNotPausable
	case errors.Is(err, repo.ErrGameRuntimeUnavailable),
		errors.Is(err, repo.ErrInvalidGameRuntimeState):
		return ErrGameRuntimeUnavailable
	case err == nil:
		return ErrGameRuntimeUnavailable
	default:
		return fmt.Errorf("%w: pause game runtime: %v", ErrInternal, err)
	}
}
