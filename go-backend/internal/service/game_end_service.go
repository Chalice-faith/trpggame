package service

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

// EndGameRequest 是结束单人游戏的服务层请求。
type EndGameRequest struct {
	UserID uint
	RoomID uint
}

// EndGameResult 是结束后的持久化房间状态。
type EndGameResult struct {
	RoomID uint             `json:"room_id"`
	Status model.RoomStatus `json:"status"`
}

// EndGame 先暂停 Redis 运行态阻断行动提交，再持久化 ended 并清理运行态。
func (s *GameService) EndGame(
	ctx context.Context,
	req *EndGameRequest,
) (*EndGameResult, error) {
	if req == nil || req.UserID == 0 || req.RoomID == 0 {
		return nil, ErrInvalidGameEnd
	}
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, req.RoomID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find room for end: %v", ErrInternal, err)
	}
	if room == nil || room.ID != req.RoomID || room.OwnerID != req.UserID {
		return nil, fmt.Errorf("%w: invalid room repository result", ErrInternal)
	}
	if !room.IsSolo {
		return nil, ErrGameRoomNotEndable
	}
	if room.Status == model.RoomStatusEnded {
		return s.finishEndedGameCleanup(ctx, room.ID, req.UserID)
	}
	if room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused {
		return nil, ErrGameRoomNotEndable
	}

	updated, runtimeErr := s.runtimeRepo.TransitionSoloRoomStatus(
		ctx,
		room.ID,
		req.UserID,
		[]model.RoomStatus{model.RoomStatusPlaying, model.RoomStatusPaused},
		model.RoomStatusPaused,
	)
	if runtimeErr != nil || !updated {
		return nil, mapEndRuntimeError(runtimeErr)
	}

	updated, transitionErr := s.gameRepo.TransitionRoomStatus(
		ctx,
		room.ID,
		req.UserID,
		[]model.RoomStatus{model.RoomStatusPlaying, model.RoomStatusPaused},
		model.RoomStatusEnded,
	)
	if transitionErr == nil && updated {
		return s.finishEndedGameCleanup(ctx, room.ID, req.UserID)
	}
	return s.reconcileGameEnd(ctx, room.ID, req.UserID, room.Status, transitionErr)
}

func (s *GameService) reconcileGameEnd(
	ctx context.Context,
	roomID uint,
	userID uint,
	originalStatus model.RoomStatus,
	transitionErr error,
) (*EndGameResult, error) {
	reconcileContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
	defer cancel()
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(reconcileContext, roomID, userID)
	if err != nil || room == nil || room.ID != roomID || room.OwnerID != userID {
		return nil, fmt.Errorf(
			"%w: reconcile MySQL end after transition error %v: %v",
			ErrInternal, transitionErr, err,
		)
	}
	if room.Status == model.RoomStatusEnded {
		return s.finishEndedGameCleanup(reconcileContext, roomID, userID)
	}
	if room.Status != originalStatus {
		return nil, ErrGameRoomNotEndable
	}

	updated, rollbackErr := s.runtimeRepo.TransitionSoloRoomStatus(
		reconcileContext,
		roomID,
		userID,
		[]model.RoomStatus{model.RoomStatusPaused},
		originalStatus,
	)
	if rollbackErr != nil || !updated {
		return nil, fmt.Errorf(
			"%w: MySQL end failed (%v) and Redis rollback failed: %v",
			ErrInternal, transitionErr, rollbackErr,
		)
	}
	if transitionErr != nil {
		return nil, fmt.Errorf("%w: end room in MySQL: %v", ErrInternal, transitionErr)
	}
	return nil, ErrGameRoomNotEndable
}

func (s *GameService) finishEndedGameCleanup(
	ctx context.Context,
	roomID uint,
	userID uint,
) (*EndGameResult, error) {
	if err := s.runtimeRepo.DeleteSoloRoom(ctx, roomID, userID); err == nil {
		return endedGameResult(roomID), nil
	} else if !errors.Is(err, repo.ErrGameRuntimeUnavailable) {
		return nil, fmt.Errorf("%w: delete ended game runtime: %v", ErrInternal, err)
	}

	retryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
	defer cancel()
	if err := s.runtimeRepo.DeleteSoloRoom(retryContext, roomID, userID); err != nil {
		if errors.Is(err, repo.ErrGameRuntimeUnavailable) || errors.Is(err, repo.ErrInvalidGameRuntimeState) {
			return nil, ErrGameRuntimeUnavailable
		}
		return nil, fmt.Errorf("%w: retry ended game runtime cleanup: %v", ErrInternal, err)
	}
	return endedGameResult(roomID), nil
}

func endedGameResult(roomID uint) *EndGameResult {
	return &EndGameResult{RoomID: roomID, Status: model.RoomStatusEnded}
}

func mapEndRuntimeError(err error) error {
	switch {
	case errors.Is(err, repo.ErrGameRuntimeStatusConflict):
		return ErrGameRoomNotEndable
	case errors.Is(err, repo.ErrGameRuntimeUnavailable),
		errors.Is(err, repo.ErrInvalidGameRuntimeState),
		err == nil:
		return ErrGameRuntimeUnavailable
	default:
		return fmt.Errorf("%w: pause runtime for game end: %v", ErrInternal, err)
	}
}
