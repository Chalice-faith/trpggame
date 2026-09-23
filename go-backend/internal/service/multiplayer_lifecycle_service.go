package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type MultiplayerLifecycleRuntime interface {
	GetMultiplayerRoom(context.Context, uint) (*model.MultiplayerRuntimeSnapshot, error)
	TransitionMultiplayerRoom(context.Context, uint, string, string, model.RoomStatus, model.RoomStatus, time.Time) (int, error)
	RestoreMultiplayerRoom(context.Context, string, *model.MultiplayerRuntimeSnapshot) error
	ListPendingMultiplayerAutoSaveRooms(context.Context, int) ([]uint, error)
	ListPendingMultiplayerAutoSaves(context.Context, uint) ([]model.PendingMultiplayerAutoSave, error)
	AcknowledgeMultiplayerAutoSave(context.Context, uint, int, string) error
}

type MultiplayerProgressRepository interface {
	ReplacePausedMultiplayerRoomProgress(context.Context, uint, uint, int, int) (bool, error)
	EndMultiplayerRoom(context.Context, uint, uint, []model.RoomStatus, int, int) (bool, error)
}

type MultiplayerLifecyclePublisher interface {
	PublishMultiplayerLifecycle(*model.MultiplayerRuntimeSnapshot)
	PublishMultiplayerEnded(uint, string, int, int)
}

func (s *GameService) pauseMultiplayerGame(ctx context.Context, req *PauseGameRequest, room *model.GameRoom) (*PauseGameResult, error) {
	if room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused {
		return nil, ErrGameRoomNotPausable
	}
	gameRepo, runtime, _, err := s.multiplayerLifecycleDependencies()
	if err != nil {
		return nil, err
	}
	snapshot, err := runtime.GetMultiplayerRoom(ctx, room.ID)
	if err != nil {
		return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotPausable)
	}
	if err := validateMultiplayerLiveSnapshot(ctx, gameRepo, room, snapshot); err != nil {
		return nil, err
	}
	changed := false
	if snapshot.Status == model.RoomStatusPlaying {
		pausedGeneration := uuid.NewString()
		if _, err := runtime.TransitionMultiplayerRoom(ctx, room.ID, snapshot.Generation, pausedGeneration,
			model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{}); err != nil {
			return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotPausable)
		}
		snapshot, err = runtime.GetMultiplayerRoom(ctx, room.ID)
		if err != nil {
			return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotPausable)
		}
		changed = true
	}
	if room.Status == model.RoomStatusPlaying {
		updated, transitionErr := s.gameRepo.TransitionRoomStatus(ctx, room.ID, req.UserID,
			[]model.RoomStatus{model.RoomStatusPlaying}, model.RoomStatusPaused)
		if transitionErr != nil || !updated {
			confirmed, readErr := s.gameRepo.FindRoomByIDAndOwnerID(context.WithoutCancel(ctx), room.ID, req.UserID)
			if readErr == nil && confirmed != nil && confirmed.Status == model.RoomStatusPaused {
				changed = true
			} else if readErr == nil && confirmed != nil && confirmed.Status == model.RoomStatusPlaying {
				rollbackDeadline := s.now().UTC().Add(time.Duration(room.TurnTimeoutSeconds) * time.Second)
				rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
				defer cancel()
				if _, rollbackErr := runtime.TransitionMultiplayerRoom(rollbackContext, room.ID,
					snapshot.Generation, uuid.NewString(), model.RoomStatusPaused, model.RoomStatusPlaying, rollbackDeadline); rollbackErr != nil {
					return nil, fmt.Errorf("%w: multiplayer pause persistence failed (%v) and Redis rollback failed: %v", ErrInternal, transitionErr, rollbackErr)
				}
				if transitionErr == nil {
					return nil, ErrGameRoomNotPausable
				}
				return nil, fmt.Errorf("%w: persist multiplayer pause: transition=%v confirm=%v", ErrInternal, transitionErr, readErr)
			} else {
				return nil, fmt.Errorf("%w: persist multiplayer pause: transition=%v confirm=%v", ErrInternal, transitionErr, readErr)
			}
		}
		changed = true
	}
	if changed {
		s.publishMultiplayerLifecycle(snapshot)
		s.notifyMultiplayerPresence(ctx, room.ID)
	}
	return pausedGameResult(room.ID), nil
}

func (s *GameService) resumeMultiplayerGame(ctx context.Context, req *ResumeGameRequest, room *model.GameRoom) (*ResumeGameResult, error) {
	if room.Status != model.RoomStatusPaused && room.Status != model.RoomStatusPlaying {
		return nil, ErrGameRoomNotResumable
	}
	gameRepo, runtime, _, err := s.multiplayerLifecycleDependencies()
	if err != nil {
		return nil, err
	}
	snapshot, err := runtime.GetMultiplayerRoom(ctx, room.ID)
	if err != nil {
		return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotResumable)
	}
	if err := validateMultiplayerLiveSnapshot(ctx, gameRepo, room, snapshot); err != nil {
		return nil, err
	}
	changed := false
	if snapshot.Status == model.RoomStatusPaused {
		generation := uuid.NewString()
		deadline := s.now().UTC().Add(time.Duration(room.TurnTimeoutSeconds) * time.Second)
		if _, err := runtime.TransitionMultiplayerRoom(ctx, room.ID, snapshot.Generation, generation,
			model.RoomStatusPaused, model.RoomStatusPlaying, deadline); err != nil {
			return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotResumable)
		}
		snapshot, err = runtime.GetMultiplayerRoom(ctx, room.ID)
		if err != nil {
			return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotResumable)
		}
		changed = true
	}
	if room.Status == model.RoomStatusPaused {
		updated, transitionErr := s.gameRepo.TransitionRoomStatus(ctx, room.ID, req.UserID,
			[]model.RoomStatus{model.RoomStatusPaused}, model.RoomStatusPlaying)
		if transitionErr != nil || !updated {
			confirmed, readErr := s.gameRepo.FindRoomByIDAndOwnerID(context.WithoutCancel(ctx), room.ID, req.UserID)
			if readErr != nil || confirmed == nil || confirmed.Status != model.RoomStatusPlaying {
				rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
				defer cancel()
				_, rollbackErr := runtime.TransitionMultiplayerRoom(rollbackContext, room.ID, snapshot.Generation, uuid.NewString(),
					model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{})
				if rollbackErr != nil {
					return nil, fmt.Errorf("%w: MySQL multiplayer resume failed (%v), Redis rollback failed: %v", ErrInternal, transitionErr, rollbackErr)
				}
				if transitionErr == nil && readErr == nil && confirmed != nil && confirmed.Status == model.RoomStatusPaused {
					return nil, ErrGameRoomNotResumable
				}
				return nil, fmt.Errorf("%w: persist multiplayer resume: transition=%v confirm=%v", ErrInternal, transitionErr, readErr)
			}
		}
		changed = true
	}
	if changed {
		s.publishMultiplayerLifecycle(snapshot)
		s.notifyMultiplayerPresence(ctx, room.ID)
	}
	return resumedGameResult(room.ID), nil
}

func (s *GameService) createMultiplayerManualSave(ctx context.Context, req *CreateManualSaveRequest, saveName string, room *model.GameRoom) (*CreateManualSaveResult, error) {
	if room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused {
		return nil, ErrGameRoomNotSavable
	}
	gameRepo, runtime, _, err := s.multiplayerLifecycleDependencies()
	if err != nil {
		return nil, err
	}
	if room.Status == model.RoomStatusPlaying {
		if _, err := s.pauseMultiplayerGame(ctx, &PauseGameRequest{UserID: req.UserID, RoomID: room.ID}, room); err != nil {
			return nil, err
		}
		room.Status = model.RoomStatusPaused
	}
	snapshot, err := runtime.GetMultiplayerRoom(ctx, room.ID)
	if err != nil {
		return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotSavable)
	}
	if err := validateMultiplayerLiveSnapshot(ctx, gameRepo, room, snapshot); err != nil || snapshot.Status != model.RoomStatusPaused {
		if err != nil {
			return nil, err
		}
		return nil, ErrGameRuntimeUnavailable
	}
	save, err := multiplayerSaveFromSnapshot(room.ID, saveName, snapshot, false)
	if err != nil {
		return nil, fmt.Errorf("%w: encode multiplayer manual save: %v", ErrInternal, err)
	}
	if err := s.gameRepo.CreateSave(ctx, save); err != nil {
		return nil, fmt.Errorf("%w: create multiplayer manual save: %v", ErrInternal, err)
	}
	if save.ID == 0 {
		return nil, fmt.Errorf("%w: game save repository returned an empty ID", ErrInternal)
	}
	return &CreateManualSaveResult{Save: save}, nil
}

func (s *GameService) loadMultiplayerSave(ctx context.Context, req *LoadGameRequest, room *model.GameRoom, save *model.GameSave) (*LoadGameResult, error) {
	if room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused {
		return nil, ErrGameRoomNotLoadable
	}
	gameRepo, runtime, progress, err := s.multiplayerLifecycleDependencies()
	if err != nil {
		return nil, err
	}
	snapshot, err := decodeMultiplayerSaveSnapshot(save, room.ID, req.SaveID)
	if err != nil {
		return nil, err
	}
	if err := validateMultiplayerLiveSnapshot(ctx, gameRepo, room, snapshot); err != nil {
		if errors.Is(err, ErrMultiplayerSaveIncompatible) {
			return nil, fmt.Errorf("%w: save roster does not match the frozen room", ErrMultiplayerSaveIncompatible)
		}
		return nil, err
	}
	if room.Status == model.RoomStatusPlaying {
		if _, err := s.pauseMultiplayerGame(ctx, &PauseGameRequest{UserID: req.UserID, RoomID: room.ID}, room); err != nil {
			if errors.Is(err, ErrGameRoomNotPausable) {
				return nil, ErrGameRoomNotLoadable
			}
			return nil, err
		}
		room.Status = model.RoomStatusPaused
	}
	// Restoring replaces the Redis timeline, including its pending auto-saves.
	// Persist committed boundary snapshots before that atomic replacement.
	if err := s.FlushPendingMultiplayerAutoSaves(ctx, room.ID); err != nil {
		return nil, err
	}
	current, err := runtime.GetMultiplayerRoom(ctx, room.ID)
	if err != nil || current == nil || current.Status != model.RoomStatusPaused {
		return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotLoadable)
	}
	snapshot.Status = model.RoomStatusPaused
	snapshot.Generation = uuid.NewString()
	snapshot.DeadlineAt = nil
	snapshot.ActionLease = nil
	if err := runtime.RestoreMultiplayerRoom(ctx, current.Generation, snapshot); err != nil {
		if errors.Is(err, repo.ErrMultiplayerRuntimeConflict) {
			return nil, ErrMultiplayerTurnConflict
		}
		return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotLoadable)
	}
	updated, updateErr := progress.ReplacePausedMultiplayerRoomProgress(ctx, room.ID, req.UserID, snapshot.CurrentTurn, snapshot.RoundNumber)
	if updateErr != nil || !updated {
		confirmed, readErr := s.gameRepo.FindRoomByIDAndOwnerID(context.WithoutCancel(ctx), room.ID, req.UserID)
		if readErr != nil || confirmed == nil || confirmed.Status != model.RoomStatusPaused ||
			confirmed.CurrentTurn != snapshot.CurrentTurn || confirmed.RoundNumber != snapshot.RoundNumber {
			return nil, fmt.Errorf("%w: replace multiplayer save progress: update=%v confirm=%v", ErrInternal, updateErr, readErr)
		}
	}
	restored, err := runtime.GetMultiplayerRoom(ctx, room.ID)
	if err == nil {
		s.publishMultiplayerLifecycle(restored)
	}
	return loadedGameResult(room.ID, save.ID, snapshot.CurrentTurn), nil
}

func (s *GameService) endMultiplayerGame(ctx context.Context, req *EndGameRequest, room *model.GameRoom) (*EndGameResult, error) {
	if room.Status != model.RoomStatusEnded && room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused {
		return nil, ErrGameRoomNotEndable
	}
	gameRepo, runtime, progress, err := s.multiplayerLifecycleDependencies()
	if err != nil {
		return nil, err
	}
	if room.Status == model.RoomStatusEnded {
		if snapshot, readErr := runtime.GetMultiplayerRoom(ctx, room.ID); readErr == nil {
			if snapshot.Status == model.RoomStatusPlaying {
				pausedGeneration := uuid.NewString()
				if _, transitionErr := runtime.TransitionMultiplayerRoom(ctx, room.ID, snapshot.Generation, pausedGeneration,
					model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{}); transitionErr != nil {
					return nil, multiplayerLifecycleRuntimeError(transitionErr, ErrGameRoomNotEndable)
				}
				snapshot, readErr = runtime.GetMultiplayerRoom(ctx, room.ID)
				if readErr != nil {
					return nil, multiplayerLifecycleRuntimeError(readErr, ErrGameRoomNotEndable)
				}
			}
			if snapshot.Status != model.RoomStatusPaused {
				return nil, ErrGameRoomNotEndable
			}
			generation, err := s.finishMultiplayerEnd(ctx, room, snapshot)
			if err != nil {
				return nil, err
			}
			s.publishMultiplayerEnded(room.ID, generation, snapshot.CurrentTurn, snapshot.RoundNumber)
		}
		s.notifyMultiplayerPresence(ctx, room.ID)
		return endedGameResult(room.ID), nil
	}
	snapshot, err := runtime.GetMultiplayerRoom(ctx, room.ID)
	if err != nil {
		return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotEndable)
	}
	if err := validateMultiplayerLiveSnapshot(ctx, gameRepo, room, snapshot); err != nil {
		return nil, err
	}
	if snapshot.Status == model.RoomStatusPlaying {
		generation := uuid.NewString()
		if _, err := runtime.TransitionMultiplayerRoom(ctx, room.ID, snapshot.Generation, generation,
			model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{}); err != nil {
			return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotEndable)
		}
		snapshot, err = runtime.GetMultiplayerRoom(ctx, room.ID)
		if err != nil {
			return nil, multiplayerLifecycleRuntimeError(err, ErrGameRoomNotEndable)
		}
	}
	if room.Status != model.RoomStatusEnded {
		updated, transitionErr := progress.EndMultiplayerRoom(ctx, room.ID, req.UserID,
			[]model.RoomStatus{room.Status}, snapshot.CurrentTurn, snapshot.RoundNumber)
		if transitionErr != nil || !updated {
			confirmed, readErr := s.gameRepo.FindRoomByIDAndOwnerID(context.WithoutCancel(ctx), room.ID, req.UserID)
			if readErr != nil || confirmed == nil || confirmed.Status != model.RoomStatusEnded ||
				confirmed.CurrentTurn != snapshot.CurrentTurn || confirmed.RoundNumber != snapshot.RoundNumber {
				if readErr == nil && confirmed != nil && confirmed.Status == model.RoomStatusPlaying && room.Status == model.RoomStatusPlaying {
					rollbackDeadline := s.now().UTC().Add(time.Duration(room.TurnTimeoutSeconds) * time.Second)
					rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), pauseReconcileTimeout)
					defer cancel()
					if _, rollbackErr := runtime.TransitionMultiplayerRoom(rollbackContext, room.ID,
						snapshot.Generation, uuid.NewString(), model.RoomStatusPaused, model.RoomStatusPlaying, rollbackDeadline); rollbackErr != nil {
						return nil, fmt.Errorf("%w: multiplayer end persistence failed (%v) and Redis rollback failed: %v", ErrInternal, transitionErr, rollbackErr)
					}
					if restored, restoreErr := runtime.GetMultiplayerRoom(rollbackContext, room.ID); restoreErr == nil {
						s.publishMultiplayerLifecycle(restored)
					}
				}
				return nil, fmt.Errorf("%w: persist multiplayer end: transition=%v confirm=%v", ErrInternal, transitionErr, readErr)
			}
		}
	}
	endedGeneration, err := s.finishMultiplayerEnd(ctx, room, snapshot)
	if err != nil {
		return nil, err
	}
	s.publishMultiplayerEnded(room.ID, endedGeneration, snapshot.CurrentTurn, snapshot.RoundNumber)
	s.notifyMultiplayerPresence(ctx, room.ID)
	return endedGameResult(room.ID), nil
}

func (s *GameService) finishMultiplayerEnd(ctx context.Context, room *model.GameRoom, snapshot *model.MultiplayerRuntimeSnapshot) (string, error) {
	endedGeneration := uuid.NewString()
	if snapshot.Status == model.RoomStatusPaused {
		if _, err := s.lifecycleRuntime().TransitionMultiplayerRoom(ctx, room.ID, snapshot.Generation, endedGeneration,
			model.RoomStatusPaused, model.RoomStatusEnded, time.Time{}); err != nil {
			return "", multiplayerLifecycleRuntimeError(err, ErrGameRoomNotEndable)
		}
	}
	return endedGeneration, nil
}

// FlushPendingMultiplayerAutoSaves persists Redis-atomic boundary snapshots idempotently.
func (s *GameService) FlushPendingMultiplayerAutoSaves(ctx context.Context, roomID uint) error {
	if roomID == 0 {
		return ErrInvalidGameAction
	}
	runtime := s.lifecycleRuntime()
	gameRepo, ok := s.gameRepo.(MultiplayerGameRepository)
	if runtime == nil || !ok {
		return ErrGameRuntimeUnavailable
	}
	room, err := gameRepo.FindRoomByID(ctx, roomID)
	if err != nil {
		return fmt.Errorf("%w: find multiplayer auto-save room: %v", ErrInternal, err)
	}
	if room == nil || room.ID != roomID || room.IsSolo {
		return ErrGameRoomNotFound
	}
	pending, err := runtime.ListPendingMultiplayerAutoSaves(ctx, roomID)
	if err != nil {
		return multiplayerLifecycleRuntimeError(err, ErrGameRuntimeUnavailable)
	}
	for _, item := range pending {
		if item.Snapshot == nil || item.Generation != item.Snapshot.Generation || item.Snapshot.RoomID != roomID ||
			validateMultiplayerSnapshot(item.Snapshot, model.RoomStatusPlaying) != nil {
			return ErrGameRuntimeUnavailable
		}
		if err := validateMultiplayerRoomRoster(ctx, gameRepo, room, item.Snapshot); err != nil {
			return err
		}
		save, err := multiplayerSaveFromSnapshot(roomID, fmt.Sprintf("自动存档-%d轮", item.Snapshot.RoundNumber), item.Snapshot, true)
		if err != nil {
			return fmt.Errorf("%w: encode multiplayer auto-save: %v", ErrInternal, err)
		}
		if _, err := s.gameRepo.CreateAutoSave(ctx, save); err != nil {
			return fmt.Errorf("%w: persist multiplayer auto-save: %v", ErrInternal, err)
		}
		if err := runtime.AcknowledgeMultiplayerAutoSave(ctx, roomID, item.Snapshot.RoundNumber, item.Generation); err != nil {
			return multiplayerLifecycleRuntimeError(err, ErrGameRuntimeUnavailable)
		}
	}
	return nil
}

func (s *GameService) multiplayerLifecycleDependencies() (MultiplayerGameRepository, MultiplayerLifecycleRuntime, MultiplayerProgressRepository, error) {
	gameRepo, gameOK := s.gameRepo.(MultiplayerGameRepository)
	runtime := s.lifecycleRuntime()
	progress, progressOK := s.gameRepo.(MultiplayerProgressRepository)
	if !gameOK || runtime == nil || !progressOK {
		return nil, nil, nil, ErrGameRuntimeUnavailable
	}
	return gameRepo, runtime, progress, nil
}

func (s *GameService) lifecycleRuntime() MultiplayerLifecycleRuntime {
	runtime, _ := s.runtimeRepo.(MultiplayerLifecycleRuntime)
	return runtime
}

func (s *GameService) publishMultiplayerLifecycle(snapshot *model.MultiplayerRuntimeSnapshot) {
	if snapshot == nil {
		return
	}
	if publisher, ok := s.multiplayerPublisher.(MultiplayerLifecyclePublisher); ok {
		publisher.PublishMultiplayerLifecycle(snapshot)
	}
}

func (s *GameService) publishMultiplayerEnded(roomID uint, generation string, turn, round int) {
	if publisher, ok := s.multiplayerPublisher.(MultiplayerLifecyclePublisher); ok {
		publisher.PublishMultiplayerEnded(roomID, generation, turn, round)
	}
}

func (s *GameService) notifyMultiplayerPresence(ctx context.Context, roomID uint) {
	if s.presenceNotifier == nil {
		return
	}
	gameRepo, ok := s.gameRepo.(MultiplayerGameRepository)
	if !ok {
		return
	}
	players, err := gameRepo.FindPlayersByRoom(ctx, roomID)
	if err != nil {
		return
	}
	ids := make([]uint, 0, len(players))
	for _, player := range players {
		if player.Status == model.RoomPlayerStatusActive {
			ids = append(ids, player.UserID)
		}
	}
	s.presenceNotifier.NotifyGameStatusChanged(ctx, ids)
}

func validateMultiplayerLiveSnapshot(ctx context.Context, gameRepo MultiplayerGameRepository, room *model.GameRoom, snapshot *model.MultiplayerRuntimeSnapshot) error {
	if room == nil || snapshot == nil || snapshot.Version != model.MultiplayerRuntimeSnapshotVersion ||
		snapshot.RoomID != room.ID || (snapshot.Status != model.RoomStatusPlaying && snapshot.Status != model.RoomStatusPaused) ||
		uuid.Validate(snapshot.Generation) != nil || snapshot.CurrentTurn < 0 || len(snapshot.TurnOrder) < 2 ||
		snapshot.RoundNumber != snapshot.CurrentTurn/len(snapshot.TurnOrder) ||
		snapshot.CurrentActorID != snapshot.TurnOrder[snapshot.CurrentTurn%len(snapshot.TurnOrder)] ||
		snapshot.SummaryMemory == "" && len(snapshot.RecentMessages) == 0 {
		return ErrGameRuntimeUnavailable
	}
	if snapshot.Status == model.RoomStatusPaused && (snapshot.DeadlineAt != nil || snapshot.ActionLease != nil) {
		return ErrGameRuntimeUnavailable
	}
	if snapshot.Status == model.RoomStatusPlaying && ((snapshot.DeadlineAt == nil) == (snapshot.ActionLease == nil)) {
		return ErrGameRuntimeUnavailable
	}
	return validateMultiplayerRoomRoster(ctx, gameRepo, room, snapshot)
}

func validateMultiplayerRoomRoster(ctx context.Context, gameRepo MultiplayerGameRepository, room *model.GameRoom, snapshot *model.MultiplayerRuntimeSnapshot) error {
	var frozenOrder []uint
	decoder := json.NewDecoder(bytes.NewReader(room.TurnOrder))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&frozenOrder) != nil || decoder.Decode(new(any)) != io.EOF || len(frozenOrder) < 2 || len(frozenOrder) != len(snapshot.TurnOrder) {
		return ErrMultiplayerSaveIncompatible
	}
	players, err := gameRepo.FindPlayersByRoom(ctx, room.ID)
	if err != nil {
		return fmt.Errorf("%w: list frozen multiplayer room members: %v", ErrInternal, err)
	}
	active := make([]model.RoomPlayer, 0, len(players))
	for _, player := range players {
		if player.Status == model.RoomPlayerStatusActive {
			active = append(active, player)
		}
	}
	if len(active) != len(frozenOrder) || len(snapshot.Players) != len(frozenOrder) {
		return ErrMultiplayerSaveIncompatible
	}
	runtimePlayers := make(map[uint]model.MultiplayerRuntimePlayer, len(snapshot.Players))
	for _, player := range snapshot.Players {
		if _, duplicate := runtimePlayers[player.UserID]; duplicate {
			return ErrMultiplayerSaveIncompatible
		}
		runtimePlayers[player.UserID] = player
	}
	seen := make(map[uint]struct{}, len(frozenOrder))
	for index, userID := range frozenOrder {
		if userID == 0 || snapshot.TurnOrder[index] != userID || active[index].UserID != userID || active[index].CharacterID == nil {
			return ErrMultiplayerSaveIncompatible
		}
		if _, duplicate := seen[userID]; duplicate {
			return ErrMultiplayerSaveIncompatible
		}
		seen[userID] = struct{}{}
		player, exists := runtimePlayers[userID]
		if !exists || player.CharacterID != *active[index].CharacterID || player.PlayerState == nil || player.Items == nil || player.Buffs == nil {
			return ErrMultiplayerSaveIncompatible
		}
	}
	return nil
}

func validateMultiplayerSnapshot(snapshot *model.MultiplayerRuntimeSnapshot, expectedStatus model.RoomStatus) error {
	if snapshot == nil || snapshot.Version != model.MultiplayerRuntimeSnapshotVersion || snapshot.RoomID == 0 ||
		uuid.Validate(snapshot.Generation) != nil || snapshot.Status != expectedStatus || snapshot.ActionLease != nil ||
		snapshot.CurrentTurn < 0 || len(snapshot.TurnOrder) < 2 ||
		snapshot.RoundNumber != snapshot.CurrentTurn/len(snapshot.TurnOrder) ||
		snapshot.CurrentActorID != snapshot.TurnOrder[snapshot.CurrentTurn%len(snapshot.TurnOrder)] ||
		len(snapshot.Players) != len(snapshot.TurnOrder) || len(snapshot.RecentMessages) == 0 ||
		len(snapshot.RecentMessages) > 10 || len(snapshot.SummaryMemory) > 65535 {
		return ErrGameSaveCorrupt
	}
	if expectedStatus == model.RoomStatusPaused && snapshot.DeadlineAt != nil ||
		expectedStatus == model.RoomStatusPlaying && (snapshot.DeadlineAt == nil || snapshot.DeadlineAt.IsZero()) {
		return ErrGameSaveCorrupt
	}
	seen := make(map[uint]struct{}, len(snapshot.TurnOrder))
	for _, userID := range snapshot.TurnOrder {
		if userID == 0 {
			return ErrGameSaveCorrupt
		}
		if _, duplicate := seen[userID]; duplicate {
			return ErrGameSaveCorrupt
		}
		seen[userID] = struct{}{}
	}
	players := make(map[uint]struct{}, len(snapshot.Players))
	for _, player := range snapshot.Players {
		if player.UserID == 0 || player.CharacterID == 0 || player.PlayerState == nil || player.Items == nil || player.Buffs == nil {
			return ErrGameSaveCorrupt
		}
		if _, duplicate := players[player.UserID]; duplicate {
			return ErrGameSaveCorrupt
		}
		players[player.UserID] = struct{}{}
	}
	for _, userID := range snapshot.TurnOrder {
		if _, exists := players[userID]; !exists {
			return ErrGameSaveCorrupt
		}
	}
	for _, message := range snapshot.RecentMessages {
		if (message.Role != "user" && message.Role != "assistant" && message.Role != "system") || strings.TrimSpace(message.Content) == "" {
			return ErrGameSaveCorrupt
		}
	}
	for _, player := range snapshot.Players {
		for _, item := range player.Items {
			if strings.TrimSpace(item.Name) == "" || item.Quantity <= 0 {
				return ErrGameSaveCorrupt
			}
		}
		for _, buff := range player.Buffs {
			if strings.TrimSpace(buff.Name) == "" || buff.Duration <= 0 {
				return ErrGameSaveCorrupt
			}
		}
	}
	return nil
}

func decodeMultiplayerSaveSnapshot(save *model.GameSave, roomID, saveID uint) (*model.MultiplayerRuntimeSnapshot, error) {
	if save == nil || save.ID != saveID || save.RoomID != roomID || save.RoundNumber < 0 {
		return nil, fmt.Errorf("%w: invalid multiplayer save repository result", ErrInternal)
	}
	var snapshot model.MultiplayerRuntimeSnapshot
	if decodeStrictJSON(save.RedisSnapshot, &snapshot) != nil || snapshot.Version != model.MultiplayerRuntimeSnapshotVersion ||
		snapshot.RoomID != roomID || snapshot.RoundNumber != save.RoundNumber ||
		(snapshot.Status != model.RoomStatusPlaying && snapshot.Status != model.RoomStatusPaused) {
		return nil, ErrGameSaveCorrupt
	}
	var recent []model.RuntimeMessage
	if decodeStrictJSON(save.RecentMessages, &recent) != nil || recent == nil {
		return nil, ErrGameSaveCorrupt
	}
	snapshot.SummaryMemory = save.SummaryMemory
	snapshot.RecentMessages = recent
	if validateMultiplayerSnapshot(&snapshot, snapshot.Status) != nil {
		return nil, ErrGameSaveCorrupt
	}
	return &snapshot, nil
}

type multiplayerPersistedSnapshot struct {
	Version        int                              `json:"version"`
	RoomID         uint                             `json:"room_id"`
	Status         model.RoomStatus                 `json:"status"`
	Generation     string                           `json:"generation"`
	CurrentTurn    int                              `json:"current_turn"`
	RoundNumber    int                              `json:"round_number"`
	TurnOrder      []uint                           `json:"turn_order"`
	CurrentActorID uint                             `json:"current_actor_id"`
	DeadlineAt     *time.Time                       `json:"deadline_at"`
	Players        []model.MultiplayerRuntimePlayer `json:"players"`
}

func multiplayerSaveFromSnapshot(roomID uint, name string, snapshot *model.MultiplayerRuntimeSnapshot, automatic bool) (*model.GameSave, error) {
	persisted := multiplayerPersistedSnapshot{
		Version: snapshot.Version, RoomID: snapshot.RoomID, Status: snapshot.Status, Generation: snapshot.Generation,
		CurrentTurn: snapshot.CurrentTurn, RoundNumber: snapshot.RoundNumber, TurnOrder: snapshot.TurnOrder,
		CurrentActorID: snapshot.CurrentActorID, DeadlineAt: snapshot.DeadlineAt, Players: snapshot.Players,
	}
	runtimeSnapshot, err := json.Marshal(persisted)
	if err != nil || !json.Valid(runtimeSnapshot) || len(runtimeSnapshot) == 0 || runtimeSnapshot[0] != '{' {
		return nil, ErrGameSaveCorrupt
	}
	recent, err := json.Marshal(snapshot.RecentMessages)
	if err != nil || len(recent) == 0 || recent[0] != '[' {
		return nil, ErrGameSaveCorrupt
	}
	return &model.GameSave{
		RoomID: roomID, SaveName: name, RoundNumber: snapshot.RoundNumber, SummaryMemory: snapshot.SummaryMemory,
		RedisSnapshot: runtimeSnapshot, RecentMessages: recent, IsAuto: automatic,
	}, nil
}

func multiplayerLifecycleRuntimeError(err error, statusError error) error {
	switch {
	case errors.Is(err, repo.ErrMultiplayerRuntimeConflict), errors.Is(err, repo.ErrGameRuntimeStatusConflict),
		errors.Is(err, repo.ErrGameRuntimeGenerationConflict):
		return statusError
	case errors.Is(err, repo.ErrGameRuntimeUnavailable), errors.Is(err, repo.ErrInvalidGameRuntimeState):
		return ErrGameRuntimeUnavailable
	case err == nil:
		return ErrGameRuntimeUnavailable
	default:
		return fmt.Errorf("%w: multiplayer lifecycle runtime: %v", ErrInternal, err)
	}
}
