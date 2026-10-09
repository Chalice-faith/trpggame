package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"trpggame/internal/ai_client"
	"trpggame/internal/config"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type GameSummaryRepository interface {
	ListSummaryRooms(context.Context, uint) ([]uint, error)
	ClaimSummary(context.Context, uint, int, time.Duration) (*model.GameSummaryWork, error)
	CompleteSummary(context.Context, *model.GameSummaryWork, string) error
	RetrySummary(context.Context, *model.GameSummaryWork, string) error
	LoadMemoryContext(context.Context, uint, string) (*model.MemoryContext, error)
	FindState(context.Context, uint) (*model.GameMemoryState, error)
}
type GameSummaryClient interface {
	GenerateSummary(context.Context, string, []model.RuntimeMessage) (string, error)
}
type GameSummaryProjection interface {
	PublishSummary(context.Context, uint, string, uint64, uint64, uint64, string) error
}

type GameSummaryWorker struct {
	repo       GameSummaryRepository
	client     GameSummaryClient
	projection GameSummaryProjection
	options    config.GameSummaryConfig
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	once       sync.Once
	cursor     uint
}

func NewGameSummaryWorker(repository GameSummaryRepository, client GameSummaryClient, projection GameSummaryProjection, options config.GameSummaryConfig) *GameSummaryWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &GameSummaryWorker{repo: repository, client: client, projection: projection, options: options, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}
func (w *GameSummaryWorker) Run() {
	w.once.Do(func() {
		defer close(w.done)
		if !w.options.Enabled {
			return
		}
		ticker := time.NewTicker(time.Duration(w.options.PollIntervalMS) * time.Millisecond)
		defer ticker.Stop()
		for {
			w.processDue()
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
func (w *GameSummaryWorker) Stop() {
	w.cancel()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
	}
}
func (w *GameSummaryWorker) processDue() {
	if w.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	rooms, err := w.repo.ListSummaryRooms(ctx, w.cursor)
	cancel()
	if err != nil {
		log.Print("summary_list class=storage")
		return
	}
	if len(rooms) == 0 {
		w.cursor = 0
		return
	}
	for _, room := range rooms {
		if w.ctx.Err() != nil {
			return
		}
		w.cursor = room
		w.processRoom(room)
	}
}
func (w *GameSummaryWorker) processRoom(room uint) {
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	work, err := w.repo.ClaimSummary(ctx, room, w.options.TriggerActions, time.Duration(w.options.LeaseSeconds)*time.Second)
	cancel()
	if err != nil {
		log.Printf("summary_claim room=%d class=storage", room)
		return
	}
	if work != nil {
		var messages []model.RuntimeMessage
		err = json.Unmarshal(work.Messages, &messages)
		var candidate string
		started := time.Now()
		if err == nil {
			ctx, cancel = context.WithTimeout(w.ctx, time.Duration(w.options.TimeoutSeconds)*time.Second)
			candidate, err = w.client.GenerateSummary(ctx, work.PreviousSummary, messages)
			cancel()
		}
		if err == nil {
			ctx, cancel = context.WithTimeout(w.ctx, 5*time.Second)
			err = w.repo.CompleteSummary(ctx, work, candidate)
			cancel()
		}
		if err != nil {
			category := "model"
			if errors.Is(err, repo.ErrMemoryCursor) || errors.Is(err, repo.ErrMemoryConflict) {
				category = "source"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				category = "timeout"
			}
			ctx, cancel = context.WithTimeout(context.WithoutCancel(w.ctx), time.Second)
			_ = w.repo.RetrySummary(ctx, work, category)
			cancel()
			log.Printf("summary_job room=%d job=%s class=%s duration_ms=%d", room, work.ID, category, time.Since(started).Milliseconds())
			return
		}
		log.Printf("summary_job room=%d job=%s version=%d through=%d class=completed duration_ms=%d", room, work.ID, work.ExpectedVersion+1, work.ThroughPosition, time.Since(started).Milliseconds())
	}
	// A projection failure never rolls back the durable summary. Later polling
	// republishes it, including after a restart; lifecycle fencing is in Lua.
	if w.projection != nil {
		ctx, cancel = context.WithTimeout(w.ctx, 5*time.Second)
		defer cancel()
		state, err := w.repo.FindState(ctx, room)
		if err != nil || state.ActiveTimelineID == nil || state.Status != "ready" {
			return
		}
		memory, err := w.repo.LoadMemoryContext(ctx, room, *state.ActiveTimelineID)
		if err == nil && memory.SummaryVersion > 0 {
			_ = w.projection.PublishSummary(ctx, room, memory.TimelineID, state.Revision, memory.SummaryVersion, memory.SummaryThroughPosition, memory.Summary)
		}
	}
}

type InferenceMemoryRepository interface {
	LoadMemoryContext(context.Context, uint, string) (*model.MemoryContext, error)
}

func (s *GameService) ConfigureInferenceMemory(repository InferenceMemoryRepository) {
	s.inferenceMemory = repository
}
func (s *GameService) prepareInferenceMemory(ctx context.Context, request *ai_client.GameActionRequest) error {
	if s.inferenceMemory == nil {
		return nil
	}
	runtime, ok := s.runtimeRepo.(interface {
		GetGameArchive(context.Context, uint) (*model.GameArchiveRuntime, error)
	})
	if !ok {
		return nil
	}
	meta, err := runtime.GetGameArchive(ctx, request.RoomID)
	if err != nil {
		return err
	}
	if meta == nil {
		return nil
	}
	if meta.ArchiveState != "ready" || meta.ControlOperationID != "" {
		return repo.ErrGameArchiveNotReady
	}
	memory, err := s.inferenceMemory.LoadMemoryContext(ctx, request.RoomID, meta.TimelineID)
	if err != nil {
		return err
	}
	if memory.ThroughPosition != meta.DurablePosition {
		return repo.ErrMemoryConflict
	}
	request.MemoryContext = memory
	return nil
}
