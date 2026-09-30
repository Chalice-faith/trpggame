package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"trpggame/internal/config"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type GameArchiveRuntimeRepository interface {
	ListPendingArchiveRooms(context.Context, time.Time, int) ([]uint, error)
	ClaimGameArchive(context.Context, uint, string, time.Duration) (*model.PendingGameArchive, error)
	RetryGameArchive(context.Context, model.GameArchiveACK, time.Time) error
	BlockGameArchive(context.Context, model.GameArchiveACK) error
	AcknowledgeGameArchive(context.Context, model.GameArchiveACK) (*model.GameArchiveACKResult, error)
	GetGameArchive(context.Context, uint) (*model.GameArchiveRuntime, error)
}

type GameArchivePersistence interface {
	ArchiveAndAdvance(context.Context, *model.GameActionRecord) (bool, error)
}

// GameArchiveService never generates narrative or reapplies runtime effects.
// Both synchronous confirmation and recovery use the same outbox/lease protocol.
type GameArchiveService struct {
	runtime  GameArchiveRuntimeRepository
	store    GameArchivePersistence
	options  config.GameArchiveConfig
	now      func() time.Time
	notifier interface {
		PublishGameArchiveStatus(uint, *model.GameArchiveRuntime)
	}
}

func (s *GameArchiveService) ConfigureNotifier(notifier interface {
	PublishGameArchiveStatus(uint, *model.GameArchiveRuntime)
}) {
	s.notifier = notifier
}

func (s *GameArchiveService) publishStatus(roomID uint, archive *model.GameArchiveRuntime) {
	if s.notifier != nil && archive != nil {
		s.notifier.PublishGameArchiveStatus(roomID, archive)
	}
}

func NewGameArchiveService(runtime GameArchiveRuntimeRepository, store GameArchivePersistence, options config.GameArchiveConfig) (*GameArchiveService, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if runtime == nil || store == nil {
		return nil, errors.New("missing archive repositories")
	}
	return &GameArchiveService{runtime: runtime, store: store, options: options, now: time.Now}, nil
}

// ArchivePending uses a fresh budget even if the submitting connection closed:
// a committed result must remain recoverable and must not become an AI retry.
func (s *GameArchiveService) ArchivePending(parent context.Context, roomID uint) (*model.GameArchiveACKResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Duration(s.options.OperationTimeoutMS)*time.Millisecond)
	defer cancel()
	return s.process(ctx, roomID)
}

func (s *GameArchiveService) process(parent context.Context, roomID uint) (*model.GameArchiveACKResult, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(s.options.OperationTimeoutMS)*time.Millisecond)
	defer cancel()
	owner := uuid.NewString()
	lease := time.Duration(s.options.LeaseMS) * time.Millisecond
	pending, err := s.runtime.ClaimGameArchive(ctx, roomID, owner, lease)
	if err != nil {
		return nil, err
	}
	if pending == nil {
		return nil, nil
	}
	r := pending.Record
	if r == nil || r.RoomID != roomID || pending.OwnerToken != owner || r.Seal() != nil {
		return nil, repo.ErrGameArchiveCorrupt
	}
	ack := model.GameArchiveACK{RoomID: roomID, TimelineID: r.TimelineID, Position: r.Position, CommitID: r.CommitID, PayloadHash: r.PayloadHash, OwnerToken: owner}
	// Renewal also verifies that the same complete record remains authoritative.
	stopRenew := make(chan struct{})
	var renew sync.WaitGroup
	renew.Add(1)
	go func() {
		defer renew.Done()
		ticker := time.NewTicker(lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stopRenew:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, issue := s.runtime.ClaimGameArchive(ctx, roomID, owner, lease)
				if issue != nil || current == nil || current.Record == nil || current.Record.CommitID != r.CommitID || current.Record.PayloadHash != r.PayloadHash {
					cancel()
					return
				}
			}
		}
	}()
	_, err = s.store.ArchiveAndAdvance(ctx, r)
	close(stopRenew)
	renew.Wait()
	if err == nil {
		// Exact ownership must still hold after MySQL committed. Lost ownership is
		// safe: another worker replays the same immutable record before its ACK.
		current, issue := s.runtime.ClaimGameArchive(ctx, roomID, owner, lease)
		if issue != nil {
			err = issue
		} else if current == nil || current.Record == nil || current.Record.CommitID != r.CommitID || current.Record.PayloadHash != r.PayloadHash {
			err = repo.ErrGameArchiveLeaseConflict
		}
	}
	if err == nil {
		var result *model.GameArchiveACKResult
		result, err = s.runtime.AcknowledgeGameArchive(ctx, ack)
		if err == nil {
			if result != nil {
				s.publishStatus(roomID, result.Memory)
			}
			return result, nil
		}
		// Lost ACK response: retry the identical ACK, never mint a new owner.
		if errors.Is(err, repo.ErrGameArchiveCommitUnknown) {
			result, issue := s.runtime.AcknowledgeGameArchive(ctx, ack)
			if issue == nil {
				if result != nil {
					s.publishStatus(roomID, result.Memory)
				}
				return result, nil
			}
		}
	}
	cleanup, finish := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer finish()
	if permanentArchiveError(err) {
		_ = s.runtime.BlockGameArchive(cleanup, ack)
		if current, probe := s.runtime.GetGameArchive(cleanup, roomID); probe == nil {
			s.publishStatus(roomID, current)
		}
	} else {
		_ = s.runtime.RetryGameArchive(cleanup, ack, s.now().UTC().Add(s.retryDelay(pending.Attempts)))
	}
	// A failed cleanup leaves a durable outbox and an expiring lease; a future
	// process can rediscover it. No failure removes pending payloads.
	return nil, err
}

func permanentArchiveError(err error) bool {
	return errors.Is(err, repo.ErrMemoryConflict) || errors.Is(err, repo.ErrMemoryGap) || errors.Is(err, repo.ErrMemoryBranch) ||
		errors.Is(err, repo.ErrGameArchiveCorrupt) || errors.Is(err, model.ErrInvalidMemoryData) || errors.Is(err, gorm.ErrRecordNotFound)
}

func (s *GameArchiveService) retryDelay(attempts uint) time.Duration {
	delay := time.Duration(s.options.RetryBaseMS) * time.Millisecond
	cap := time.Duration(s.options.RetryMaxMS) * time.Millisecond
	for attempts > 0 && delay < cap {
		delay *= 2
		attempts--
	}
	if delay > cap {
		delay = cap
	}
	return delay
}
