package service

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
	"trpggame/internal/repo"
)

type GameArchiveWorker struct {
	service  *GameArchiveService
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	runOnce  sync.Once
	stopOnce sync.Once
}

func NewGameArchiveWorker(service *GameArchiveService) *GameArchiveWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &GameArchiveWorker{service: service, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (w *GameArchiveWorker) Run() {
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

// Stop cancels polling and in-flight work, with a bounded wait. Redis must remain
// open until this returns; unsuccessful cleanup relies on lease expiry.
func (w *GameArchiveWorker) Stop() {
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

func (w *GameArchiveWorker) processDue() {
	s := w.service
	ctx, cancel := context.WithTimeout(w.ctx, time.Duration(s.options.OperationTimeoutMS)*time.Millisecond)
	rooms, err := s.runtime.ListPendingArchiveRooms(ctx, s.now().UTC(), s.options.BatchSize)
	cancel()
	if err != nil {
		if w.ctx.Err() == nil {
			log.Print("game archive list failed class=redis")
		}
		return
	}
	for _, room := range rooms {
		if w.ctx.Err() != nil {
			return
		}
		_, err := s.process(w.ctx, room)
		if err != nil && !errors.Is(err, repo.ErrGameArchiveLeaseConflict) && !errors.Is(err, repo.ErrGameArchiveNotReady) {
			class := "retry"
			if permanentArchiveError(err) {
				class = "blocked"
			}
			log.Printf("game archive room=%d class=%s", room, class)
		}
	}
}
