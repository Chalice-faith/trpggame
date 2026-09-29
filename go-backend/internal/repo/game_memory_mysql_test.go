//go:build integration

package repo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"trpggame/internal/model"
)

type memoryFixture struct {
	db                 *gorm.DB
	repo               *GameMemoryRepo
	roomID             uint
	rootID, generation string
}

func newMemoryFixture(t *testing.T) *memoryFixture {
	t.Helper()
	dsn := os.Getenv("TRPG_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TRPG_TEST_MYSQL_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(20)
	room := &model.GameRoom{Name: "memory integration", ScriptID: 1, OwnerID: 7, IsSolo: true, Status: model.RoomStatusPaused, MaxPlayers: 1, TurnOrder: []byte(`[]`)}
	if err := db.Create(room).Error; err != nil {
		t.Fatal(err)
	}
	f := &memoryFixture{db: db, repo: NewGameMemoryRepo(db), roomID: room.ID, rootID: uuid.NewString(), generation: uuid.NewString()}
	t.Cleanup(func() {
		for _, table := range []string{"game_action_records", "game_memory_operations", "game_memory_states", "game_timelines", "game_saves"} {
			if err := db.Table(table).Where("room_id = ?", room.ID).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
		}
		db.Delete(room)
		connection.Close()
	})
	op := f.operation("start", nil, &f.rootID, nil)
	root := &model.GameTimeline{ID: f.rootID, RoomID: f.roomID, HistoryComplete: true}
	if _, err := f.repo.PrepareOperation(context.Background(), op, root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.PrepareOperation(context.Background(), op, root); err != nil {
		t.Fatalf("start replay: %v", err)
	}
	f.complete(t, op)
	return f
}

func (f *memoryFixture) operation(kind string, source, target *string, saveID *uint) *model.GameMemoryOperation {
	return &model.GameMemoryOperation{OperationID: uuid.NewString(), RoomID: f.roomID, Kind: kind,
		Fingerprint: strings.Repeat("a", 64), SourceGeneration: f.generation, SourceTimelineID: source,
		TargetTimelineID: target, TargetSaveID: saveID, TargetSnapshot: []byte(`{"state":"fixture"}`)}
}
func (f *memoryFixture) complete(t *testing.T, op *model.GameMemoryOperation) {
	t.Helper()
	for _, pair := range [][2]string{{"prepared", "redis_applied"}, {"redis_applied", "completed"}, {"redis_applied", "completed"}} {
		if err := f.repo.AdvanceOperation(context.Background(), f.roomID, op.OperationID, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *memoryFixture) record(timeline string, position uint64) *model.GameActionRecord {
	actor := uint(7)
	r := &model.GameActionRecord{CommitID: uuid.NewString(), RoomID: f.roomID, TimelineID: timeline, Position: position,
		Kind: "action", ActorID: &actor, RequestNamespace: "client", RequestID: uuid.NewString(),
		Fingerprint: strings.Repeat("a", 64), SourceGeneration: f.generation, SourceRevision: 1,
		TurnBefore: int(position - 1), TurnAfter: int(position), RoundBefore: int(position - 1), RoundAfter: int(position),
		PayloadVersion: 1, Payload: []byte(`{"narrative":"正式提交的事实","effects":[]}`)}
	if position == 1 && timeline == f.rootID {
		r.Kind, r.RequestNamespace, r.ActorID = "opening", "opening", nil
	}
	return r
}
func (f *memoryFixture) archive(t *testing.T, timeline string, from, through uint64) {
	t.Helper()
	for p := from; p <= through; p++ {
		if inserted, err := f.repo.Archive(context.Background(), f.record(timeline, p)); err != nil || !inserted {
			t.Fatalf("archive %d: %v %v", p, inserted, err)
		}
	}
}
func (f *memoryFixture) save(t *testing.T, timeline string, position uint64, auto bool) *model.GameSave {
	t.Helper()
	snapshot, _ := json.Marshal(map[string]any{"schema_version": 3, "mode": "solo", "runtime": map[string]any{"version": 1, "turn": position},
		"memory": map[string]any{"timeline_id": timeline, "position": position, "history_complete": true}})
	save := &model.GameSave{RoomID: f.roomID, RoundNumber: 10, IsAuto: auto, TimelineID: &timeline, MemoryPosition: &position, RedisSnapshot: snapshot, RecentMessages: []byte(`[]`), SummaryMemory: "摘要"}
	gameRepo := NewGameRepo(f.db)
	if auto {
		if inserted, err := gameRepo.CreateAutoSave(context.Background(), save); !inserted || err != nil {
			t.Fatalf("auto save %v %v", inserted, err)
		}
	} else if err := gameRepo.CreateSave(context.Background(), save); err != nil {
		t.Fatal(err)
	}
	return save
}
func (f *memoryFixture) fork(t *testing.T, current, parent string, at uint64) string {
	t.Helper()
	save := f.save(t, parent, at, false)
	id := uuid.NewString()
	op := f.operation("load", &current, &id, &save.ID)
	target := &model.GameTimeline{ID: id, RoomID: f.roomID, ParentID: &parent, ForkPosition: at, DurablePosition: at, OriginSaveID: &save.ID, HistoryComplete: true}
	if _, err := f.repo.PrepareOperation(context.Background(), op, target); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.PrepareOperation(context.Background(), op, target); err != nil {
		t.Fatalf("load replay: %v", err)
	}
	f.complete(t, op)
	return id
}

func TestGameMemoryMySQL84ConcurrentArchiveAndConflicts(t *testing.T) {
	f := newMemoryFixture(t)
	record := f.record(f.rootID, 1)
	var inserted atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ok, err := f.repo.Archive(context.Background(), record)
			if err != nil {
				t.Error(err)
			}
			if ok {
				inserted.Add(1)
			}
		}()
	}
	workers.Wait()
	if inserted.Load() != 1 {
		t.Fatalf("inserted %d copies", inserted.Load())
	}
	changed := *record
	changed.Payload = []byte(`{"narrative":"changed"}`)
	if _, err := f.repo.Archive(context.Background(), &changed); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("hash conflict: %v", err)
	}
	if _, err := f.repo.Archive(context.Background(), f.record(f.rootID, 3)); !errors.Is(err, ErrMemoryGap) {
		t.Fatalf("gap: %v", err)
	}
	next := f.record(f.rootID, 2)
	if _, err := f.repo.Archive(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	reused := *next
	reused.CommitID = uuid.NewString()
	reused.Position = 3
	if _, err := f.repo.Archive(context.Background(), &reused); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("request collision: %v", err)
	}
	other := f.record(f.rootID, 2)
	if _, err := f.repo.Archive(context.Background(), other); !errors.Is(err, ErrMemoryGap) {
		t.Fatalf("occupied position: %v", err)
	}
	var branch model.GameTimeline
	f.db.First(&branch, "id = ?", f.rootID)
	if branch.DurablePosition != 2 {
		t.Fatalf("watermark jumped: %d", branch.DurablePosition)
	}
	if err := f.db.Model(&model.GameActionRecord{}).Where("commit_id = ?", record.CommitID).Update("payload_hash", "").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repo.ListVisibleRecords(context.Background(), f.roomID, f.rootID, nil, 100); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("missing hash accepted: %v", err)
	}
}

func TestGameMemoryMySQL84NestedVisibilityAndAutosaves(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 100)
	_, stale, err := f.repo.ListVisibleRecords(ctx, f.roomID, f.rootID, nil, 5)
	if err != nil || stale == nil {
		t.Fatal(err)
	}
	firstSave := f.save(t, f.rootID, 20, true)
	undurable := *firstSave
	undurable.ID = 0
	undurable.IsAuto = false
	futurePosition := uint64(101)
	undurable.MemoryPosition = &futurePosition
	var futureEnvelope map[string]any
	json.Unmarshal(undurable.RedisSnapshot, &futureEnvelope)
	futureEnvelope["memory"].(map[string]any)["position"] = futurePosition
	undurable.RedisSnapshot, _ = json.Marshal(futureEnvelope)
	if err := NewGameRepo(f.db).CreateSave(ctx, &undurable); !errors.Is(err, ErrMemoryGap) {
		t.Fatalf("undurable save: %v", err)
	}
	duplicate := *firstSave
	duplicate.ID = 0
	if inserted, err := NewGameRepo(f.db).CreateAutoSave(ctx, &duplicate); err != nil || inserted || duplicate.ID != firstSave.ID {
		t.Fatalf("auto replay: %v %v", inserted, err)
	}
	conflict := *firstSave
	conflict.ID = 0
	conflict.SummaryMemory = "changed"
	if _, err := NewGameRepo(f.db).CreateAutoSave(ctx, &conflict); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("auto hash conflict: %v", err)
	}
	first := f.fork(t, f.rootID, f.rootID, 20)
	f.archive(t, first, 21, 23)
	f.save(t, first, 23, true)
	second := f.fork(t, first, first, 23)
	f.archive(t, second, 24, 25)
	if _, _, err := f.repo.ListVisibleRecords(ctx, f.roomID, second, stale, 5); !errors.Is(err, ErrMemoryCursor) {
		t.Fatalf("stale cursor %v", err)
	}
	if _, _, err := f.repo.ListVisibleRecords(ctx, f.roomID+10000, second, nil, 5); err == nil {
		t.Fatal("cross-room read succeeded")
	}
	var all []model.GameActionRecord
	var cursor *VisibleRecordCursor
	for {
		page, next, err := f.repo.ListVisibleRecords(ctx, f.roomID, second, cursor, 5)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		cursor = next
		if cursor == nil {
			break
		}
	}
	if len(all) != 25 {
		t.Fatalf("visible records %d", len(all))
	}
	for i, r := range all {
		if r.Position != uint64(i+1) {
			t.Fatal("position gap")
		}
		want := f.rootID
		if r.Position > 20 {
			want = first
		}
		if r.Position > 23 {
			want = second
		}
		if r.TimelineID != want {
			t.Fatalf("future/branch leak at %d", r.Position)
		}
	}
	sibling := f.fork(t, second, f.rootID, 10)
	f.archive(t, sibling, 11, 11)
	page, _, err := f.repo.ListVisibleRecords(ctx, f.roomID, sibling, nil, 100)
	if err != nil || len(page) != 11 {
		t.Fatalf("ancestor load %d %v", len(page), err)
	}
	if _, _, err := f.repo.ListVisibleRecords(ctx, f.roomID, second, nil, 100); !errors.Is(err, ErrMemoryCursor) {
		t.Fatalf("inactive branch queried: %v", err)
	}
	// Deleting the origin save must not delete the immutable branch or its records.
	if _, err := NewGameRepo(f.db).DeleteSave(ctx, f.roomID, firstSave.ID); err != nil {
		t.Fatal(err)
	}
	page, _, err = f.repo.ListVisibleRecords(ctx, f.roomID, sibling, nil, 100)
	if err != nil || len(page) != 11 {
		t.Fatal("save deletion removed history")
	}
}

func TestGameMemoryMySQL84ControlCASAndLegacyBaseline(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 1)
	legacy := &model.GameSave{RoomID: f.roomID, RedisSnapshot: []byte(`{"version":1}`), RecentMessages: []byte(`[]`)}
	if err := NewGameRepo(f.db).CreateSave(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	op := f.operation("load", &f.rootID, &id, &legacy.ID)
	branch := &model.GameTimeline{ID: id, RoomID: f.roomID, OriginSaveID: &legacy.ID, HistoryComplete: false}
	if _, err := f.repo.PrepareOperation(ctx, op, branch); err != nil {
		t.Fatal(err)
	}
	otherID := uuid.NewString()
	competing := f.operation("load", &f.rootID, &otherID, &legacy.ID)
	otherBranch := *branch
	otherBranch.ID = otherID
	if _, err := f.repo.PrepareOperation(ctx, competing, &otherBranch); !errors.Is(err, ErrMemoryBusy) {
		t.Fatalf("competing control %v", err)
	}
	changed := *op
	changed.TargetSnapshot = []byte(`{"state":"other"}`)
	if _, err := f.repo.PrepareOperation(ctx, &changed, branch); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("operation payload conflict %v", err)
	}
	operations, err := f.repo.ListRecoverableOperations(ctx, time.Now().Add(time.Second), 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range operations {
		if r.OperationID == op.OperationID {
			found = true
		}
	}
	if !found {
		t.Fatal("prepared load missing from recovery scan")
	}
	if updated, err := f.repo.ScheduleOperationRetry(ctx, f.roomID, op.OperationID, "prepared", 0, time.Now().Add(time.Hour), "storage"); !updated || err != nil {
		t.Fatalf("retry schedule %v %v", updated, err)
	}
	if updated, err := f.repo.ScheduleOperationRetry(ctx, f.roomID, op.OperationID, "prepared", 0, time.Now(), "redis"); updated || err != nil {
		t.Fatalf("stale schedule %v %v", updated, err)
	}
	operations, err = f.repo.ListRecoverableOperations(ctx, time.Now().Add(time.Second), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range operations {
		if r.OperationID == op.OperationID {
			t.Fatal("retry delay ignored")
		}
	}
	if err := f.repo.AdvanceOperation(ctx, f.roomID, op.OperationID, "redis_applied", "completed"); !errors.Is(err, ErrMemoryBusy) {
		t.Fatalf("phase skip: %v", err)
	}
	f.complete(t, op)
	if _, err := f.repo.Archive(ctx, f.record(id, 1)); !errors.Is(err, ErrMemoryGap) {
		t.Fatalf("missing legacy baseline %v", err)
	}
	baseline := f.record(id, 1)
	baseline.Position, baseline.Kind, baseline.RequestNamespace, baseline.ActorID = 0, "legacy_baseline", "legacy", nil
	baseline.TurnBefore, baseline.TurnAfter, baseline.RoundBefore, baseline.RoundAfter = 0, 0, 0, 0
	if _, err := f.repo.Archive(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	f.archive(t, id, 1, 1)
	page, _, err := f.repo.ListVisibleRecords(ctx, f.roomID, id, nil, 100)
	if err != nil || len(page) != 2 || page[0].Kind != "legacy_baseline" {
		t.Fatalf("legacy visibility %v %v", page, err)
	}
	end := f.operation("end", &id, nil, nil)
	if _, err := f.repo.PrepareOperation(ctx, end, nil); err != nil {
		t.Fatal(err)
	}
	f.complete(t, end)
	page, _, err = f.repo.ListVisibleRecords(ctx, f.roomID, id, nil, 100)
	if err != nil || len(page) != 2 {
		t.Fatal("ending removed history")
	}
}

func TestGameMemoryMySQL84ConcurrentControlsAbortAndCorruption(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 3)
	save := f.save(t, f.rootID, 2, false)
	var winner *model.GameMemoryOperation
	var mu sync.Mutex
	var workers sync.WaitGroup
	var success, busy atomic.Int32
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id := uuid.NewString()
			op := f.operation("load", &f.rootID, &id, &save.ID)
			target := &model.GameTimeline{ID: id, RoomID: f.roomID, ParentID: &f.rootID, ForkPosition: 2, DurablePosition: 2, OriginSaveID: &save.ID, HistoryComplete: true}
			_, err := f.repo.PrepareOperation(ctx, op, target)
			if err == nil {
				success.Add(1)
				mu.Lock()
				winner = op
				mu.Unlock()
			} else if errors.Is(err, ErrMemoryBusy) {
				busy.Add(1)
			} else {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if success.Load() != 1 || busy.Load() != 3 {
		t.Fatalf("controls success=%d busy=%d", success.Load(), busy.Load())
	}
	if err := f.repo.AdvanceOperation(ctx, f.roomID, winner.OperationID, "prepared", "aborted"); err != nil {
		t.Fatal(err)
	}
	state, err := f.repo.FindState(ctx, f.roomID)
	if err != nil || state.Status != "ready" || *state.ActiveTimelineID != f.rootID {
		t.Fatalf("abort state %v %v", state, err)
	}
	if _, err := f.repo.Archive(ctx, f.record(*winner.TargetTimelineID, 3)); !errors.Is(err, ErrMemoryBranch) {
		t.Fatalf("aborted branch accepted archive %v", err)
	}
	if err := f.repo.AdvanceOperation(ctx, f.roomID, winner.OperationID, "prepared", "redis_applied"); !errors.Is(err, ErrMemoryBusy) {
		t.Fatalf("aborted operation revived %v", err)
	}
	// A corrupt source cannot silently omit a fact from the advertised durable range.
	if err := f.db.Where("room_id = ? AND timeline_id = ? AND position = ?", f.roomID, f.rootID, 2).Delete(&model.GameActionRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repo.ListVisibleRecords(ctx, f.roomID, f.rootID, nil, 100); !errors.Is(err, ErrMemoryGap) {
		t.Fatalf("missing durable record accepted: %v", err)
	}
}
