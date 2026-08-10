package service

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

// ResumeGameRequest 是恢复单人游戏的服务层请求。
type ResumeGameRequest struct {
	UserID uint
	RoomID uint
}

// ResumeGameResult 是恢复后的持久化房间状态。
type ResumeGameResult struct {
	RoomID uint             `json:"room_id"`
	Status model.RoomStatus `json:"status"`
}

// ResumeGame 先确认 MySQL 为 playing，再恢复 Redis，避免持久状态暂停时接受行动。
func (s *GameService) ResumeGame(
	ctx context.Context,
	req *ResumeGameRequest,
) (*ResumeGameResult, error) {
	if req == nil || req.UserID == 0 || req.RoomID == 0 {
		return nil, ErrInvalidGameResume
	}
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, req.RoomID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find room for resume: %v", ErrInternal, err)
	}
	if room == nil || room.ID != req.RoomID || room.OwnerID != req.UserID {
		return nil, fmt.Errorf("%w: invalid room repository result", ErrInternal)
	}
	if !room.IsSolo || (room.Status != model.RoomStatusPaused && room.Status != model.RoomStatusPlaying) {
		return nil, ErrGameRoomNotResumable
	}

	changedFromPaused := room.Status == model.RoomStatusPaused
	if changedFromPaused {
		updated, transitionErr := s.gameRepo.TransitionRoomStatus(
			ctx,
			room.ID,
			req.UserID,
			[]model.RoomStatus{model.RoomStatusPaused},
			model.RoomStatusPlaying,
		)
		if transitionErr != nil || !updated {
			confirmed, err := s.reconcileMySQLResume(ctx, room.ID, req.UserID, transitionErr)
			if err != nil {
				return nil, err
			}
			if !confirmed {
				return nil, ErrGameRoomNotResumable
			}
		}
	}

	updated, runtimeErr := s.runtimeRepo.TransitionSoloRoomStatus(
		ctx,
		room.ID,
		req.UserID,
		[]model.RoomStatus{model.RoomStatusPaused, model.RoomStatusPlaying},
		model.RoomStatusPlaying,
	)
	if runtimeErr == nil && updated {
		return resumedGameResult(room.ID), nil
	}
	return s.reconcileRuntimeResume(ctx, room.ID, req.UserID, changedFromPaused, runtimeErr)
}

func (s *GameService) reconcileMySQLResume(
	ctx context.Context,
	roomID uint,
	userID uint,
	transitionErr error,
) (bool, error) {
	reconcileContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
	defer cancel()
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(reconcileContext, roomID, userID)
	if err != nil || room == nil || room.ID != roomID || room.OwnerID != userID {
		return false, fmt.Errorf(
			"%w: reconcile MySQL resume after transition error %v: %v",
			ErrInternal,
			transitionErr,
			err,
		)
	}
	switch room.Status {
	case model.RoomStatusPlaying:
		return true, nil
	case model.RoomStatusPaused:
		if transitionErr != nil {
			return false, fmt.Errorf("%w: resume room in MySQL: %v", ErrInternal, transitionErr)
		}
		return false, nil
	default:
		return false, ErrGameRoomNotResumable
	}
}

func (s *GameService) reconcileRuntimeResume(
	ctx context.Context,
	roomID uint,
	userID uint,
	rollbackMySQL bool,
	runtimeErr error,
) (*ResumeGameResult, error) {
	reconcileContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
	defer cancel()
	snapshot, captureErr := s.runtimeRepo.CaptureSoloRoom(reconcileContext, roomID, userID)
	if captureErr != nil || snapshot == nil || snapshot.RoomID != roomID || snapshot.UserID != userID {
		// Redis 状态未知时保留 MySQL=playing：Redis 若仍 paused 会拒绝行动，若已 playing 则恢复已完成。
		return nil, ErrGameRuntimeUnavailable
	}
	switch snapshot.Status {
	case model.RoomStatusPlaying:
		return resumedGameResult(roomID), nil
	case model.RoomStatusPaused:
		if rollbackMySQL {
			if err := s.rollbackMySQLResume(reconcileContext, roomID, userID); err != nil {
				return nil, err
			}
		}
		return nil, mapResumeRuntimeError(runtimeErr)
	default:
		return nil, ErrGameRuntimeUnavailable
	}
}

func (s *GameService) rollbackMySQLResume(
	ctx context.Context,
	roomID uint,
	userID uint,
) error {
	updated, transitionErr := s.gameRepo.TransitionRoomStatus(
		ctx,
		roomID,
		userID,
		[]model.RoomStatus{model.RoomStatusPlaying},
		model.RoomStatusPaused,
	)
	if transitionErr == nil && updated {
		return nil
	}
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, roomID, userID)
	if err == nil && room != nil && room.ID == roomID && room.OwnerID == userID &&
		room.Status == model.RoomStatusPaused {
		return nil
	}
	return fmt.Errorf(
		"%w: Redis resume failed and MySQL rollback failed: transition=%v reconcile=%v",
		ErrInternal,
		transitionErr,
		err,
	)
}

func resumedGameResult(roomID uint) *ResumeGameResult {
	return &ResumeGameResult{RoomID: roomID, Status: model.RoomStatusPlaying}
}

func mapResumeRuntimeError(err error) error {
	switch {
	case errors.Is(err, repo.ErrGameRuntimeStatusConflict):
		return ErrGameRoomNotResumable
	case errors.Is(err, repo.ErrGameRuntimeUnavailable),
		errors.Is(err, repo.ErrInvalidGameRuntimeState),
		err == nil:
		return ErrGameRuntimeUnavailable
	default:
		return fmt.Errorf("%w: resume game runtime: %v", ErrInternal, err)
	}
}
