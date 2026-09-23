package service

import (
	"context"
	"log"
	"sync"
	"time"

	"trpggame/internal/model"
)

const (
	defaultDeadlinePollInterval = 500 * time.Millisecond
	defaultDeadlineBatchSize    = 64
	deadlineOperationTimeout    = 5 * time.Second
)

type multiplayerDeadlineSource interface {
	ListDueMultiplayerDeadlines(context.Context, time.Time, int) ([]model.MultiplayerDeadlineTask, error)
}

type multiplayerAutoSaveSource interface {
	ListPendingMultiplayerAutoSaveRooms(context.Context, int) ([]uint, error)
}

type multiplayerDeadlineProcessor interface {
	ProcessMultiplayerDeadline(context.Context, model.MultiplayerDeadlineTask, time.Time) error
}

type multiplayerAutoSaveProcessor interface {
	FlushPendingMultiplayerAutoSaves(context.Context, uint) error
}

type MultiplayerDeadlineWorker struct {
	source    multiplayerDeadlineSource
	processor multiplayerDeadlineProcessor
	interval  time.Duration
	now       func() time.Time
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
}

func NewMultiplayerDeadlineWorker(source multiplayerDeadlineSource, processor multiplayerDeadlineProcessor) *MultiplayerDeadlineWorker {
	return &MultiplayerDeadlineWorker{
		source: source, processor: processor, interval: defaultDeadlinePollInterval,
		now: time.Now, stop: make(chan struct{}), done: make(chan struct{}),
	}
}

func (w *MultiplayerDeadlineWorker) Run() {
	if w == nil {
		return
	}
	defer close(w.done)
	if w.source == nil || w.processor == nil {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.processDue()
		case <-w.stop:
			return
		}
	}
}

func (w *MultiplayerDeadlineWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

func (w *MultiplayerDeadlineWorker) processDue() {
	now := w.now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), deadlineOperationTimeout)
	tasks, err := w.source.ListDueMultiplayerDeadlines(ctx, now, defaultDeadlineBatchSize)
	cancel()
	if err != nil {
		log.Printf("multiplayer deadline list: %v", err)
	} else {
		for _, task := range tasks {
			ctx, cancel = context.WithTimeout(context.Background(), deadlineOperationTimeout)
			err = w.processor.ProcessMultiplayerDeadline(ctx, task, now)
			cancel()
			if err != nil {
				log.Printf("multiplayer deadline room=%d turn=%d: %v", task.RoomID, task.Turn, err)
			}
		}
	}
	if source, ok := w.source.(multiplayerAutoSaveSource); ok {
		ctx, cancel = context.WithTimeout(context.Background(), deadlineOperationTimeout)
		rooms, listErr := source.ListPendingMultiplayerAutoSaveRooms(ctx, defaultDeadlineBatchSize)
		cancel()
		if listErr != nil {
			log.Printf("multiplayer auto-save room list: %v", listErr)
			return
		}
		if processor, ok := w.processor.(multiplayerAutoSaveProcessor); ok {
			for _, roomID := range rooms {
				ctx, cancel = context.WithTimeout(context.Background(), deadlineOperationTimeout)
				err = processor.FlushPendingMultiplayerAutoSaves(ctx, roomID)
				cancel()
				if err != nil {
					log.Printf("multiplayer auto-save room=%d: %v", roomID, err)
				}
			}
		}
	}
}
