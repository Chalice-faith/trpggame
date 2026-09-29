package service

import (
	"context"
	"time"
	"trpggame/internal/model"
)

func validArchiveResultMemory(memory *model.GameArchiveRuntime) bool {
	// The repository uses this explicit fallback after a known commit whose
	// delivery metadata cannot be read. It grants no permission to advance.
	if memory != nil && model.ValidMemoryUUID(memory.TimelineID) && memory.ArchiveState == "recovering" && memory.Revision == 0 && memory.HeadPosition == 0 && memory.DurablePosition == 0 && memory.ControlOperationID == "" {
		return true
	}
	if memory == nil || !model.ValidMemoryUUID(memory.TimelineID) || memory.Revision == 0 || memory.HeadPosition < memory.DurablePosition || memory.HeadPosition-memory.DurablePosition > 1 {
		return false
	}
	switch memory.ArchiveState {
	case "ready":
		return memory.HeadPosition == memory.DurablePosition
	case "pending":
		return memory.HeadPosition == memory.DurablePosition+1
	case "recovering", "blocked":
		return true
	default:
		return false
	}
}

// Legacy repair cannot run for enabled rooms. Failure after Redis commit keeps
// the result pending; the independent worker owns recovery, not another AI call.
func (s *GameService) confirmArchiveProgress(ctx context.Context, roomID uint) (bool, error) {
	if s.archiveService == nil {
		return false, nil
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(s.archiveService.options.OperationTimeoutMS)*time.Millisecond)
	defer cancel()
	memory, err := s.archiveService.runtime.GetGameArchive(probe, roomID)
	if err != nil {
		return true, ErrGameRuntimeUnavailable
	}
	if memory == nil {
		return false, nil
	}
	if memory.HeadPosition > memory.DurablePosition && memory.ArchiveState != "blocked" {
		_, _ = s.archiveService.ArchivePending(ctx, roomID)
	}
	return true, nil
}

func (s *GameService) advanceMultiplayerProgress(ctx context.Context, repository MultiplayerGameRepository, roomID uint, turn, round int, known ...*model.GameArchiveRuntime) error {
	if s.confirmKnownArchive(ctx, roomID, known) {
		return nil
	}
	if handled, err := s.confirmArchiveProgress(ctx, roomID); handled {
		return err
	}
	return advanceMultiplayerProgress(ctx, repository, roomID, turn, round)
}

func (s *GameService) refreshArchiveResult(ctx context.Context, roomID uint, result *SubmitGameActionResult) {
	if s.archiveService == nil || result == nil {
		return
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if result.Memory != nil {
		copy := *result.Memory
		copy.ArchiveState = "recovering"
		result.Memory = &copy
		result.DeadlineAt = nil
	}
	memory, err := s.archiveService.runtime.GetGameArchive(probe, roomID)
	if err == nil && memory != nil {
		result.Memory = memory
		result.DeadlineAt = nil
		if reader, ok := s.archiveService.runtime.(interface {
			GetMultiplayerRoom(context.Context, uint) (*model.MultiplayerRuntimeSnapshot, error)
		}); ok && memory.ArchiveState == "ready" && result.Generation != "" {
			if snapshot, issue := reader.GetMultiplayerRoom(probe, roomID); issue == nil && snapshot.Status == model.RoomStatusPlaying && snapshot.Generation == result.Generation && snapshot.CurrentTurn == result.CurrentTurn {
				result.DeadlineAt = snapshot.DeadlineAt
			}
		}
	}
}

// Keep this helper separate from V3 autosave encoding, which is introduced in A4.
func (s *GameService) refreshArchiveSkip(ctx context.Context, roomID uint, result *model.MultiplayerSkipResult) {
	if s.archiveService == nil || result == nil {
		return
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if result.Memory != nil {
		copy := *result.Memory
		copy.ArchiveState = "recovering"
		result.Memory = &copy
		result.DeadlineAt = time.Time{}
	}
	if memory, err := s.archiveService.runtime.GetGameArchive(probe, roomID); err == nil && memory != nil {
		result.Memory = memory
		result.DeadlineAt = time.Time{}
		if reader, ok := s.archiveService.runtime.(interface {
			GetMultiplayerRoom(context.Context, uint) (*model.MultiplayerRuntimeSnapshot, error)
		}); ok && memory.ArchiveState == "ready" {
			if snapshot, issue := reader.GetMultiplayerRoom(probe, roomID); issue == nil && snapshot.Status == model.RoomStatusPlaying && snapshot.Generation == result.Generation && snapshot.CurrentTurn == result.CurrentTurn && snapshot.DeadlineAt != nil {
				result.DeadlineAt = *snapshot.DeadlineAt
			}
		}
	}
}

func (s *GameService) confirmKnownArchive(ctx context.Context, roomID uint, known []*model.GameArchiveRuntime) bool {
	if len(known) == 0 || known[0] == nil {
		return false
	}
	if s.archiveService != nil {
		_, _ = s.archiveService.ArchivePending(ctx, roomID)
	}
	return true
}
