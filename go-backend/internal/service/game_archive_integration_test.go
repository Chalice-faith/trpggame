//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"trpggame/internal/config"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type archiveIntegrationFixture struct {
	db                   *gorm.DB
	client               *redis.Client
	runtime              *repo.RedisGameStateRepo
	store                *repo.GameMemoryRepo
	service              *GameArchiveService
	room                 *model.GameRoom
	timeline, generation string
}

type archiveOutageStore struct {
	GameArchivePersistence
	fail atomic.Bool
}

func (s *archiveOutageStore) ArchiveAndAdvance(ctx context.Context, record *model.GameActionRecord) (bool, error) {
	if s.fail.CompareAndSwap(true, false) {
		return false, errors.New("injected MySQL transport failure")
	}
	return s.GameArchivePersistence.ArchiveAndAdvance(ctx, record)
}

func TestGameArchiveIntegrationMySQLFailureThenRecovery(t *testing.T) {
	f := newArchiveIntegrationFixture(t, "solo")
	outage := &archiveOutageStore{GameArchivePersistence: f.store}
	outage.fail.Store(true)
	f.service.store = outage
	if _, err := f.service.ArchivePending(context.Background(), f.room.ID); err == nil {
		t.Fatal("MySQL failure hidden")
	}
	var count int64
	f.db.Model(&model.GameActionRecord{}).Where("room_id = ?", f.room.ID).Count(&count)
	memory, _ := f.runtime.GetGameArchive(context.Background(), f.room.ID)
	if count != 1 || memory.ArchiveState != "pending" || memory.DurablePosition != 1 {
		t.Fatalf("lost pending: %#v records=%d", memory, count)
	}
	time.Sleep(1100 * time.Millisecond)
	NewGameArchiveWorker(f.service).processDue()
	f.assertConfirmed(t, 1)
}

func TestGameArchiveIntegrationPauseBeforeACK(t *testing.T) {
	f := newArchiveIntegrationFixture(t, "multiplayer")
	ctx := context.Background()
	_, err := f.runtime.TransitionMemoryMultiplayerRoom(ctx, f.room.ID, model.GameArchiveExpectation{TimelineID: f.timeline, Generation: f.generation}, uuid.NewString(), model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Model(&model.GameRoom{}).Where("id = ?", f.room.ID).Update("status", model.RoomStatusPaused).Error; err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ArchivePending(ctx, f.room.ID)
	if err != nil || result == nil || result.DeadlineAt != nil {
		t.Fatalf("paused ACK: %#v %v", result, err)
	}
	f.assertConfirmed(t, 0)
	snapshot, err := f.runtime.GetMultiplayerRoom(ctx, f.room.ID)
	if err != nil || snapshot.Status != model.RoomStatusPaused || snapshot.DeadlineAt != nil {
		t.Fatalf("pause timer: %#v %v", snapshot, err)
	}
}

func TestGameArchiveIntegrationControlFencedDrain(t *testing.T) {
	f := newArchiveIntegrationFixture(t, "solo")
	ctx := context.Background()
	pending, err := f.runtime.ClaimGameArchive(ctx, f.room.ID, uuid.NewString(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	op := &model.GameMemoryOperation{OperationID: uuid.NewString(), RoomID: f.room.ID, Kind: "end", SourceTimelineID: &f.timeline,
		SourceGeneration: f.generation, Fingerprint: strings.Repeat("c", 64), TargetSnapshot: []byte(`{}`)}
	if _, err = f.store.PrepareOperation(ctx, op, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ArchiveAndAdvance(ctx, pending.Record); err != nil {
		t.Fatalf("control drain: %v", err)
	}
	var room model.GameRoom
	f.db.First(&room, f.room.ID)
	if room.CurrentTurn != 0 || room.RoundNumber != 0 {
		t.Fatal("control-fenced record repaired progress")
	}
	// A4 owns the Redis control token and operation completion; do not ACK it here.
}

func newArchiveIntegrationFixture(t *testing.T, mode string) *archiveIntegrationFixture {
	t.Helper()
	dsn, addr := os.Getenv("TRPG_TEST_MYSQL_DSN"), os.Getenv("TRPG_TEST_REDIS_ADDR")
	if dsn == "" || addr == "" {
		t.Skip("dedicated MySQL and Redis are required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := db.DB()
	client := redis.NewClient(&redis.Options{Addr: addr})
	f := &archiveIntegrationFixture{db: db, client: client, store: repo.NewGameMemoryRepo(db), timeline: uuid.NewString(), generation: uuid.NewString()}
	f.room = &model.GameRoom{Name: "A3 recovery fixture", ScriptID: 1, OwnerID: 7, IsSolo: mode == "solo", Status: model.RoomStatusPlaying, MaxPlayers: 2, TurnOrder: []byte(`[7,8]`)}
	if mode == "multiplayer" {
		code := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
		f.room.RoomCode = &code
	}
	if err = db.Create(f.room).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, pattern := range []string{fmt.Sprintf("room:%d:*", f.room.ID), fmt.Sprintf("game:archive:room:%d:*", f.room.ID)} {
			var cursor uint64
			for {
				keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
				if err != nil {
					t.Error(err)
					break
				}
				if len(keys) > 0 {
					client.Del(ctx, keys...)
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
		member := strconv.FormatUint(uint64(f.room.ID), 10)
		client.ZRem(ctx, "game:archive:pending_rooms", member)
		client.ZRem(ctx, "game:archive:auto_save_rooms", member)
		for _, key := range []string{"game:turn_deadlines", "game:pending_multiplayer_auto_save_rooms"} {
			members, _ := client.ZRange(ctx, key, 0, -1).Result()
			for _, entry := range members {
				if entry == member || strings.HasPrefix(entry, member+":") {
					client.ZRem(ctx, key, entry)
				}
			}
		}
		for _, table := range []string{"game_summary_work", "game_summaries", "key_events", "game_action_records", "game_memory_operations", "game_memory_states", "game_timelines", "game_saves"} {
			db.Table(table).Where("room_id = ?", f.room.ID).Delete(map[string]any{})
		}
		db.Delete(f.room)
		client.Close()
		connection.Close()
	})
	state := &model.GameMemoryState{RoomID: f.room.ID, ActiveTimelineID: &f.timeline, Status: "ready", Revision: 2}
	timeline := &model.GameTimeline{ID: f.timeline, RoomID: f.room.ID, Status: "active", HistoryComplete: true}
	if err = db.Create(timeline).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(state).Error; err != nil {
		t.Fatal(err)
	}
	opening := &model.GameActionRecord{CommitID: uuid.NewString(), RoomID: f.room.ID, TimelineID: f.timeline, Position: 1, Kind: "opening", RequestNamespace: "opening", RequestID: uuid.NewString(), Fingerprint: strings.Repeat("b", 64), SourceGeneration: f.generation, SourceRevision: 2, PayloadVersion: 1, Payload: []byte(`{"narrative":"opening"}`)}
	if _, err = f.store.Archive(context.Background(), opening); err != nil {
		t.Fatal(err)
	}
	f.runtime = archiveRuntimeFixture(t, client, f.room.ID, f.timeline, f.generation, 2, 1, mode)
	commitArchiveFixture(t, f.runtime, f.room.ID, f.timeline, f.generation, 2, 1, mode)
	f.service, err = NewGameArchiveService(f.runtime, f.store, config.DefaultGameArchiveConfig())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *archiveIntegrationFixture) assertConfirmed(t *testing.T, round int) {
	t.Helper()
	memory, err := f.runtime.GetGameArchive(context.Background(), f.room.ID)
	var records int64
	f.db.Model(&model.GameActionRecord{}).Where("room_id = ?", f.room.ID).Count(&records)
	var room model.GameRoom
	f.db.First(&room, f.room.ID)
	if err != nil || memory.DurablePosition != 2 || memory.ArchiveState != "ready" || records != 2 || room.CurrentTurn != 1 || room.RoundNumber != round {
		t.Fatalf("confirmation: %#v %v records=%d room=%#v", memory, err, records, room)
	}
}

func TestGameArchiveIntegrationSharedWorkers(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			f := newArchiveIntegrationFixture(t, mode)
			client2 := redis.NewClient(&redis.Options{Addr: os.Getenv("TRPG_TEST_REDIS_ADDR")})
			defer client2.Close()
			runtime2, _ := repo.NewRedisGameStateRepo(client2, time.Hour)
			svc2, _ := NewGameArchiveService(runtime2, repo.NewGameMemoryRepo(f.db), f.service.options)
			var workers sync.WaitGroup
			for _, svc := range []*GameArchiveService{f.service, svc2} {
				workers.Add(1)
				go func(s *GameArchiveService) {
					defer workers.Done()
					_, err := s.ArchivePending(context.Background(), f.room.ID)
					if err != nil && !errors.Is(err, repo.ErrGameArchiveLeaseConflict) {
						t.Error(err)
					}
				}(svc)
			}
			workers.Wait()
			round := 1
			if mode == "multiplayer" {
				round = 0
			}
			f.assertConfirmed(t, round)
			if mode == "multiplayer" {
				snapshot, err := f.runtime.GetMultiplayerRoom(context.Background(), f.room.ID)
				if err != nil || snapshot.DeadlineAt == nil || time.Until(*snapshot.DeadlineAt) < 110*time.Second {
					t.Fatalf("timer: %#v %v", snapshot, err)
				}
			}
		})
	}
}

func TestGameArchiveIntegrationLostACKAndRestart(t *testing.T) {
	f := newArchiveIntegrationFixture(t, "solo")
	fault := &archiveFaultRuntime{GameArchiveRuntimeRepository: f.runtime, lostACK: true}
	f.service.runtime = fault
	result, err := f.service.ArchivePending(context.Background(), f.room.ID)
	if err != nil || result == nil || !result.Duplicate {
		t.Fatalf("lost ACK: %#v %v", result, err)
	}
	f.assertConfirmed(t, 1)
	restarted, _ := NewGameArchiveService(f.runtime, repo.NewGameMemoryRepo(f.db), f.service.options)
	NewGameArchiveWorker(restarted).processDue()
	f.assertConfirmed(t, 1)
}

func TestGameArchiveIntegrationProcessExitAndExpiredRuntime(t *testing.T) {
	f := newArchiveIntegrationFixture(t, "solo")
	if _, err := f.runtime.ClaimGameArchive(context.Background(), f.room.ID, uuid.NewString(), time.Second); err != nil {
		t.Fatal(err)
	}
	// The original process exits before MySQL. Runtime expiry leaves outbox intact.
	keys, _ := f.client.Keys(context.Background(), fmt.Sprintf("room:%d:*", f.room.ID)).Result()
	f.client.Del(context.Background(), keys...)
	time.Sleep(1100 * time.Millisecond)
	NewGameArchiveWorker(f.service).processDue()
	f.assertConfirmed(t, 1)
	if _, err := f.runtime.CaptureMemorySoloRoom(context.Background(), f.room.ID, 7, model.GameArchiveExpectation{TimelineID: f.timeline, Generation: f.generation}); err == nil {
		t.Fatal("runtime was fabricated")
	}
}

func TestGameArchiveIntegrationACKUnavailableThenWorker(t *testing.T) {
	f := newArchiveIntegrationFixture(t, "multiplayer")
	f.service.runtime = &archiveFaultRuntime{GameArchiveRuntimeRepository: f.runtime, ackUnavailable: true}
	if _, err := f.service.ArchivePending(context.Background(), f.room.ID); err == nil {
		t.Fatal("ACK outage hidden")
	}
	var count int64
	f.db.Model(&model.GameActionRecord{}).Where("room_id = ?", f.room.ID).Count(&count)
	if count != 2 {
		t.Fatal("MySQL fact missing")
	}
	f.service.runtime = f.runtime
	// A new process finds the persisted retry after backoff.
	time.Sleep(1100 * time.Millisecond)
	NewGameArchiveWorker(f.service).processDue()
	f.assertConfirmed(t, 0)
}

func TestGameArchiveIntegrationProgressFenceAndRollback(t *testing.T) {
	f := newArchiveIntegrationFixture(t, "solo")
	ctx := context.Background()
	pending, err := f.runtime.ClaimGameArchive(ctx, f.room.ID, uuid.NewString(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	record := *pending.Record
	// A gap must roll back the progress repair as well as the failed insert.
	gap := record
	gap.CommitID = uuid.NewString()
	gap.RequestID = uuid.NewString()
	gap.Position = 3
	gap.PayloadHash = ""
	if _, err = f.store.ArchiveAndAdvance(ctx, &gap); !errors.Is(err, repo.ErrMemoryGap) {
		t.Fatalf("gap=%v", err)
	}
	var room model.GameRoom
	f.db.First(&room, f.room.ID)
	if room.CurrentTurn != 0 {
		t.Fatal("failed insert advanced progress")
	}
	if _, err = f.store.ArchiveAndAdvance(ctx, &record); err != nil {
		t.Fatal(err)
	}
	newTimeline := uuid.NewString()
	f.db.Model(&model.GameMemoryState{}).Where("room_id = ?", f.room.ID).Updates(map[string]any{"active_timeline_id": newTimeline, "revision": 3})
	f.db.Model(&model.GameRoom{}).Where("id = ?", f.room.ID).Updates(map[string]any{"current_turn": 0, "round_number": 0})
	// A late replay can verify the old fact, never overwrite the loaded value.
	if _, err = f.store.ArchiveAndAdvance(ctx, &record); !errors.Is(err, repo.ErrMemoryBranch) {
		t.Fatalf("superseded branch accepted: %v", err)
	}
	gameRepo := repo.NewGameRepo(f.db)
	if updated, err := gameRepo.AdvanceRoomProgress(ctx, f.room.ID, 7, 99); err != nil || updated {
		t.Fatalf("legacy repair bypass: %v %v", updated, err)
	}
	f.db.First(&room, f.room.ID)
	if room.CurrentTurn != 0 || room.RoundNumber != 0 {
		t.Fatal("old timeline overwrote load")
	}
}
