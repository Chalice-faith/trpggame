package service

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
	"trpggame/internal/repo"
)

// Runs even when creation of new memory rooms is disabled. Recovery never calls AI.
type GameMemoryLifecycleWorker struct {
	service           *GameMemoryLifecycleService
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	runOnce, stopOnce sync.Once
}

func NewGameMemoryLifecycleWorker(service *GameMemoryLifecycleService) *GameMemoryLifecycleWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &GameMemoryLifecycleWorker{service: service, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (w *GameMemoryLifecycleWorker) Run() {
	w.runOnce.Do(func() {
		defer close(w.done)
		if w.service == nil {
			return
		}
		ticker := time.NewTicker(time.Duration(w.service.options.PollIntervalMS) * time.Millisecond)
		defer ticker.Stop()
		for {
			if w.ctx.Err() != nil {
				return
			}
			w.processDue()
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (w *GameMemoryLifecycleWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(w.cancel)
	budget := time.Second
	if w.service != nil {
		budget = time.Duration(w.service.options.ShutdownTimeoutMS) * time.Millisecond
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-w.done:
	case <-timer.C:
	}
}

func (w *GameMemoryLifecycleWorker) processDue() {
	s := w.service
	ctx, cancel := context.WithTimeout(w.ctx, time.Duration(s.options.OperationTimeoutMS)*time.Millisecond)
	ops, err := s.journal.ListRecoverableOperations(ctx, time.Now().UTC(), s.options.BatchSize)
	cancel()
	if err != nil {
		if w.ctx.Err() == nil {
			log.Print("game memory recovery list failed class=storage")
		}
		return
	}
	for _, op := range ops {
		if w.ctx.Err() != nil {
			return
		}
		if err := s.ProcessOperation(w.ctx, &op, true); err != nil {
			cleanup, finish := context.WithTimeout(w.ctx, time.Second)
			// Refresh after a phase may have committed despite a lost response.
			current, probe := s.journal.FindOperation(cleanup, op.RoomID, op.OperationID)
			if probe == nil && (current.Phase == "prepared" || current.Phase == "redis_applied" || current.Phase == "completed") {
				if permanentMemoryControlError(err) && current.Phase != "completed" {
					_ = s.journal.AdvanceOperation(cleanup, current.RoomID, current.OperationID, current.Phase, "blocked")
				} else {
					class := "unknown"
					if errors.Is(err, context.DeadlineExceeded) {
						class = "timeout"
					}
					_, _ = s.journal.ScheduleOperationRetry(cleanup, current.RoomID, current.OperationID, current.Phase, current.Attempts, time.Now().UTC().Add(s.archive.retryDelay(current.Attempts)), class)
				}
			}
			finish()
			if w.ctx.Err() == nil {
				log.Printf("game memory operation=%s room=%d recovery deferred", op.OperationID, op.RoomID)
			}
		}
	}
	ctx, cancel = context.WithTimeout(w.ctx, time.Duration(s.options.OperationTimeoutMS)*time.Millisecond)
	rooms, err := s.runtime.ListPendingMemoryAutoSaveRooms(ctx, time.Now().UTC(), s.options.BatchSize)
	cancel()
	if err != nil {
		return
	}
	for _, room := range rooms {
		if w.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(w.ctx, time.Duration(s.options.OperationTimeoutMS)*time.Millisecond)
		err := s.FlushAutoSaves(ctx, room)
		cancel()
		if err != nil && w.ctx.Err() == nil && !errors.Is(err, repo.ErrMemoryBusy) {
			log.Printf("game memory autosave room=%d deferred", room)
		}
	}
}

func permanentMemoryControlError(err error) bool {
	return permanentArchiveError(err) || errors.Is(err, repo.ErrGameArchiveBranchChanged) || errors.Is(err, repo.ErrActionIdempotencyConflict) || errors.Is(err, ErrGameSaveCorrupt) || errors.Is(err, ErrMultiplayerSaveIncompatible)
}
