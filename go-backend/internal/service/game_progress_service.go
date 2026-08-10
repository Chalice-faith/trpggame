package service

import (
	"context"
	"fmt"

	"trpggame/internal/model"
)

func (s *GameService) advancePersistentGameProgress(
	ctx context.Context,
	roomID uint,
	userID uint,
	turn int,
) error {
	syncContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
	defer cancel()
	updated, updateErr := s.gameRepo.AdvanceRoomProgress(syncContext, roomID, userID, turn)
	if updateErr == nil && updated {
		return nil
	}
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(syncContext, roomID, userID)
	if err == nil && room != nil && room.ID == roomID && room.OwnerID == userID &&
		(room.Status == model.RoomStatusPlaying || room.Status == model.RoomStatusPaused) &&
		room.CurrentTurn >= turn && room.RoundNumber >= turn {
		return nil
	}
	return fmt.Errorf(
		"%w: synchronize game progress to turn %d: update=%v reconcile=%v",
		ErrInternal, turn, updateErr, err,
	)
}

func (s *GameService) replaceLoadedGameProgress(
	ctx context.Context,
	roomID uint,
	userID uint,
	turn int,
) error {
	syncContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
	defer cancel()
	updated, updateErr := s.gameRepo.ReplacePausedRoomProgress(syncContext, roomID, userID, turn)
	if updateErr == nil && updated {
		return nil
	}
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(syncContext, roomID, userID)
	if err == nil && room != nil && room.ID == roomID && room.OwnerID == userID &&
		room.Status == model.RoomStatusPaused && room.CurrentTurn == turn && room.RoundNumber == turn {
		return nil
	}
	return fmt.Errorf(
		"%w: replace loaded game progress with turn %d: update=%v reconcile=%v",
		ErrInternal, turn, updateErr, err,
	)
}
