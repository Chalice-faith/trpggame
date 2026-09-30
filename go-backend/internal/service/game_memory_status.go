package service

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

// GameMemoryStatus is the small, authoritative recovery contract. It contains
// no archive body and can also describe a room using the legacy runtime.
type GameMemoryStatus struct {
	RoomID          uint   `json:"room_id"`
	Enabled         bool   `json:"enabled"`
	Status          string `json:"status"`
	TimelineID      string `json:"timeline_id,omitempty"`
	Generation      string `json:"generation,omitempty"`
	HeadPosition    uint64 `json:"head_position,omitempty"`
	DurablePosition uint64 `json:"durable_position,omitempty"`
	Revision        uint64 `json:"revision,omitempty"`
}

func (s *GameService) GetGameMemoryStatus(ctx context.Context, userID, roomID uint) (*GameMemoryStatus, error) {
	if userID == 0 || roomID == 0 {
		return nil, ErrInvalidGameRequest
	}
	// Authorize before looking at the memory journal or Redis. A multiplayer
	// participant can inspect recovery, but only an active frozen member.
	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, roomID, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		rooms, ok := s.gameRepo.(MultiplayerGameRepository)
		if !ok {
			return nil, ErrGameRoomNotFound
		}
		room, err = rooms.FindRoomByID(ctx, roomID)
		if err == nil && room != nil && !room.IsSolo {
			player, lookup := s.gameRepo.FindPlayer(ctx, roomID, userID)
			if errors.Is(lookup, gorm.ErrRecordNotFound) || (lookup == nil && (player == nil || player.Status != model.RoomPlayerStatusActive)) {
				return nil, ErrGameRoomNotFound
			}
			err = lookup
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || room == nil || room.ID != roomID || (room.IsSolo && room.OwnerID != userID) {
		return nil, ErrGameRoomNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: authorize memory status: %v", ErrInternal, err)
	}
	result := &GameMemoryStatus{RoomID: roomID, Status: "disabled"}
	if s.memoryLifecycle == nil {
		return result, nil
	}
	state, err := s.memoryLifecycle.State(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return result, nil
	}
	result.Enabled = true
	result.Revision = state.Revision
	result.Status = "recovering"
	if state.ActiveTimelineID != nil {
		result.TimelineID = *state.ActiveTimelineID
	}
	if state.Status == "blocked" {
		result.Status = "blocked"
		return result, nil
	}
	if state.Status == "ended" {
		result.Status = "ended"
		return result, nil
	}
	meta, err := s.memoryLifecycle.runtime.GetGameArchive(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return result, nil
	}
	result.HeadPosition = meta.HeadPosition
	result.DurablePosition = meta.DurablePosition
	if meta.TimelineID != result.TimelineID || meta.Revision != state.Revision {
		result.Status = "blocked"
		return result, nil
	}
	source, err := s.memoryLifecycle.runtime.GetMemoryControlSource(ctx, roomID, room.OwnerID, memoryMode(room))
	if err != nil && !errors.Is(err, repo.ErrGameRuntimeUnavailable) {
		return nil, err
	}
	if source == nil || source.Memory == nil {
		return result, nil
	}
	if source.Memory.TimelineID != meta.TimelineID || source.Memory.Revision != meta.Revision {
		result.Status = "blocked"
		return result, nil
	}
	result.Generation = source.Generation
	if !model.ValidMemoryUUID(result.Generation) {
		return result, nil
	}
	if meta.ArchiveState == "blocked" {
		result.Status = "blocked"
	} else if state.ActiveOperationID != nil || meta.ControlOperationID != "" || state.Status != "ready" {
		result.Status = "recovering"
	} else if meta.HeadPosition > meta.DurablePosition || meta.ArchiveState != "ready" {
		result.Status = "pending"
	} else {
		result.Status = "ready"
	}
	return result, nil
}

// Check the client branch before any AI call. A duplicate committed request may
// still be read while its archive is pending; new writes require readiness.
func (s *GameService) verifyMemoryExpectation(ctx context.Context, room *model.GameRoom, timelineID, generation string) (bool, error) {
	if s.memoryLifecycle == nil {
		if timelineID != "" || generation != "" {
			return false, repo.ErrMemoryConflict
		}
		return false, nil
	}
	state, err := s.memoryLifecycle.State(ctx, room.ID)
	if err != nil {
		return false, err
	}
	if state == nil {
		if timelineID != "" || generation != "" {
			return false, repo.ErrMemoryConflict
		}
		return false, nil
	}
	if !model.ValidMemoryUUID(timelineID) || !model.ValidMemoryUUID(generation) || state.ActiveTimelineID == nil || *state.ActiveTimelineID != timelineID {
		return true, repo.ErrMemoryConflict
	}
	source, err := s.memoryLifecycle.runtime.GetMemoryControlSource(ctx, room.ID, room.OwnerID, memoryMode(room))
	if err != nil {
		return true, err
	}
	if source == nil || source.Memory == nil || source.Generation != generation || source.Memory.TimelineID != timelineID || source.Memory.Revision != state.Revision {
		return true, repo.ErrMemoryConflict
	}
	return true, nil
}

func (s *GameService) requireMemoryReady(ctx context.Context, room *model.GameRoom) error {
	if s.memoryLifecycle == nil {
		return nil
	}
	state, err := s.memoryLifecycle.State(ctx, room.ID)
	if err != nil || state == nil {
		return err
	}
	if state.Status != "ready" || state.ActiveOperationID != nil {
		return repo.ErrMemoryBusy
	}
	meta, err := s.memoryLifecycle.runtime.GetGameArchive(ctx, room.ID)
	if err != nil {
		return err
	}
	if meta == nil || meta.ControlOperationID != "" || meta.ArchiveState != "ready" || meta.HeadPosition != meta.DurablePosition {
		return repo.ErrGameArchiveNotReady
	}
	return nil
}
