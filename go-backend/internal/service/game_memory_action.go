package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type memoryActionCache interface {
	FindActionResult(context.Context, uint, string, string) (*model.ActionCommitResult, bool, error)
}

func findGameActionResult(ctx context.Context, runtime memoryActionCache, roomID uint, requestID, fingerprint string, enabled bool, expected model.GameArchiveExpectation) (*model.ActionCommitResult, bool, error) {
	if !enabled {
		return runtime.FindActionResult(ctx, roomID, requestID, fingerprint)
	}
	reader, ok := runtime.(interface {
		FindMemoryActionResult(context.Context, uint, string, string, model.GameArchiveExpectation) (*model.ActionCommitResult, bool, error)
	})
	if !ok {
		return nil, false, repo.ErrGameRuntimeUnavailable
	}
	return reader.FindMemoryActionResult(ctx, roomID, requestID, fingerprint, expected)
}

// Capture the source before inference. Redis validates this exact branch,
// generation, revision and position again when it commits the result/outbox.
func (s *GameService) memoryActionRecord(ctx context.Context, room *model.GameRoom, enabled bool, actor uint, turn, nextRound int, requestID, fingerprint, kind string, expected model.GameArchiveExpectation) (*model.GameActionRecord, error) {
	if !enabled {
		return nil, nil
	}
	if s.memoryLifecycle == nil {
		return nil, repo.ErrGameRuntimeUnavailable
	}
	source, err := s.memoryLifecycle.runtime.GetMemoryControlSource(ctx, room.ID, room.OwnerID, memoryMode(room))
	if err != nil {
		return nil, err
	}
	if source == nil || source.Memory == nil || source.Memory.TimelineID != expected.TimelineID || source.Generation != expected.Generation || source.Turn != turn {
		return nil, repo.ErrMemoryConflict
	}
	meta := source.Memory
	if meta.ArchiveState != "ready" || meta.ControlOperationID != "" || meta.HeadPosition != meta.DurablePosition {
		return nil, repo.ErrGameArchiveNotReady
	}
	namespace := "client"
	if kind == "skip_timeout" {
		namespace = "timeout"
	}
	return &model.GameActionRecord{CommitID: uuid.NewString(), RoomID: room.ID, TimelineID: meta.TimelineID,
		Position: meta.HeadPosition + 1, Kind: kind, ActorID: &actor, RequestNamespace: namespace,
		RequestID: requestID, Fingerprint: fingerprint, SourceGeneration: source.Generation, SourceRevision: meta.Revision,
		TurnBefore: turn, TurnAfter: turn + 1, RoundBefore: source.Round, RoundAfter: nextRound,
		PayloadVersion: model.GameMemoryPayloadVersion, CreatedAt: time.Now().UTC()}, nil
}
