package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

const autoSaveTimeout = 5 * time.Second

func (s *GameService) createAutomaticGameSave(
	ctx context.Context,
	snapshot *model.SoloRuntimeSnapshot,
) error {
	if snapshot == nil || snapshot.Turn <= 0 || snapshot.Turn%10 != 0 ||
		snapshot.Status != model.RoomStatusPlaying {
		return fmt.Errorf("%w: invalid automatic save snapshot", ErrInternal)
	}
	normalized, err := repo.NormalizeSoloRuntimeSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("%w: normalize automatic save snapshot: %v", ErrInternal, err)
	}
	redisSnapshot, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("%w: encode automatic Redis snapshot: %v", ErrInternal, err)
	}
	recentMessages, err := json.Marshal(normalized.RecentMessages)
	if err != nil {
		return fmt.Errorf("%w: encode automatic recent messages: %v", ErrInternal, err)
	}

	save := &model.GameSave{
		RoomID:         normalized.RoomID,
		SaveName:       fmt.Sprintf("自动存档-%d", normalized.Turn),
		RoundNumber:    normalized.Turn,
		SummaryMemory:  normalized.Summary,
		RedisSnapshot:  append(json.RawMessage(nil), redisSnapshot...),
		RecentMessages: append(json.RawMessage(nil), recentMessages...),
		IsAuto:         true,
	}
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoSaveTimeout)
	defer cancel()
	created, err := s.gameRepo.CreateAutoSave(persistContext, save)
	if err != nil {
		return fmt.Errorf("%w: create automatic game save: %v", ErrInternal, err)
	}
	if created && save.ID == 0 {
		return fmt.Errorf("%w: automatic save repository returned an empty ID", ErrInternal)
	}
	return nil
}

func (s *GameService) flushPendingAutomaticGameSaves(
	ctx context.Context,
	roomID uint,
	userID uint,
) error {
	flushContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoSaveTimeout)
	defer cancel()
	pending, err := s.runtimeRepo.ListPendingAutoSaves(flushContext, roomID, userID)
	if err != nil {
		return fmt.Errorf("%w: list pending automatic saves: %v", ErrInternal, err)
	}
	for _, item := range pending {
		if item.Snapshot == nil {
			return fmt.Errorf("%w: pending automatic save has no snapshot", ErrInternal)
		}
		if err := s.createAutomaticGameSave(flushContext, item.Snapshot); err != nil {
			return err
		}
		if err := s.runtimeRepo.AcknowledgeAutoSave(
			flushContext,
			roomID,
			item.Snapshot.Turn,
			item.Generation,
		); err != nil {
			return fmt.Errorf("%w: acknowledge automatic save: %v", ErrInternal, err)
		}
	}
	return nil
}
