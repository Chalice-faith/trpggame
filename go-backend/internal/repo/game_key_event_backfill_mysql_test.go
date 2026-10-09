//go:build integration

package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"trpggame/internal/model"
)

func archiveBackfillCandidate(t *testing.T, f *memoryFixture, timeline string, position uint64, multiplayer bool, names ...string) *model.GameActionRecord {
	t.Helper()
	record := f.record(timeline, position)
	effects := "effects"
	if multiplayer {
		effects = "multiplayer_effects"
	}
	events := make([]map[string]string, 0, len(names))
	for _, name := range names {
		events = append(events, map[string]string{"name": name, "description": "已确认的来源事实"})
	}
	record.Payload, _ = json.Marshal(map[string]any{"response": map[string]any{"narrative": "角色死亡只是叙事文字", effects: map[string]any{"events": events}}})
	if _, err := f.repo.Archive(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	// Emulate an archive written before migration 021 / event extraction.
	if err := f.db.Where("source_commit_id = ?", record.CommitID).Delete(&model.KeyEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	return record
}

func TestGameKeyEventBackfillCommandPreviewAndCheckpoint(t *testing.T) {
	f := newMemoryFixture(t)
	f.archive(t, f.rootID, 1, 1)
	archiveBackfillCandidate(t, f, f.rootID, 2, false, "共同事件")
	archiveBackfillCandidate(t, f, f.rootID, 3, true, "后续事件")
	database, err := mysql.ParseDSN(os.Getenv("TRPG_TEST_MYSQL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	if database.Net != "tcp" {
		t.Skip("CLI fixture requires a TCP MySQL address")
	}
	host, port, err := net.SplitHostPort(database.Addr)
	if err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) *KeyEventBackfillResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		args := []string{"run", "../../cmd/backfill-key-events", "--room", fmt.Sprint(f.roomID), "--timeline", f.rootID, "--batch", "2"}
		cmd := exec.CommandContext(ctx, "go", append(args, extra...)...)
		cmd.Env = append(os.Environ(), "TRPG_SERVER_MODE=test", "TRPG_DATABASE_HOST="+host, "TRPG_DATABASE_PORT="+port,
			"TRPG_DATABASE_USER="+database.User, "TRPG_DATABASE_PASSWORD="+database.Passwd, "TRPG_DATABASE_DBNAME="+database.DBName)
		output, err := cmd.Output()
		if err != nil {
			// Do not print captured SQL or configuration output.
			t.Fatalf("CLI failed: %v", err)
		}
		var result KeyEventBackfillResult
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("CLI did not output exactly one JSON checkpoint: %v", err)
		}
		return &result
	}
	preview := run()
	if preview.Applied || preview.Missing != 1 || preview.NextPosition != 2 || preview.ThroughPosition != 3 || keyEventCount(t, f) != 0 {
		t.Fatalf("wrong CLI preview: %+v", preview)
	}
	first := run("--apply", "--through", "3")
	if first.Inserted != 1 || first.Done {
		t.Fatalf("wrong CLI first batch: %+v", first)
	}
	last := run("--apply", "--after", fmt.Sprint(first.NextPosition), "--through", fmt.Sprint(first.ThroughPosition))
	if !last.Done || last.Inserted != 1 || last.NextPosition != 3 || keyEventCount(t, f) != 2 {
		t.Fatalf("wrong CLI resumed batch: %+v", last)
	}
}

func keyEventCount(t *testing.T, f *memoryFixture) int64 {
	t.Helper()
	var count int64
	if err := f.db.Model(&model.KeyEvent{}).Where("room_id = ?", f.roomID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func TestGameKeyEventBackfillPreviewResumeAndConcurrentReplay(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 1)
	first := archiveBackfillCandidate(t, f, f.rootID, 2, false, "打开石门", "获得钥匙")
	archiveBackfillCandidate(t, f, f.rootID, 3, true, "缔结盟约")
	archiveBackfillCandidate(t, f, f.rootID, 4, false) // no narrative inference
	request := KeyEventBackfillRequest{RoomID: f.roomID, TimelineID: f.rootID, Limit: 2}
	preview, err := f.repo.BackfillKeyEvents(ctx, request)
	if err != nil || preview.Scanned != 2 || preview.Missing != 2 || preview.Inserted != 0 || preview.NextPosition != 2 || preview.ThroughPosition != 4 || preview.Done || keyEventCount(t, f) != 0 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	request.Apply = true
	request.ThroughPosition = &preview.ThroughPosition
	// New archival must not expand this run's frozen range.
	f.archive(t, f.rootID, 5, 5)
	var inserted atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := f.repo.BackfillKeyEvents(ctx, request)
			if err != nil {
				t.Error(err)
				return
			}
			inserted.Add(int32(result.Inserted))
		}()
	}
	workers.Wait()
	if inserted.Load() != 2 || keyEventCount(t, f) != 2 {
		t.Fatal("concurrent replay duplicated events")
	}
	request.AfterPosition = preview.NextPosition
	restarted := NewGameMemoryRepo(f.db)
	last, err := restarted.BackfillKeyEvents(ctx, request)
	if err != nil || !last.Done || last.NextPosition != 4 || last.Inserted != 1 || keyEventCount(t, f) != 3 {
		t.Fatalf("resume=%+v err=%v", last, err)
	}
	var source model.GameActionRecord
	var event model.KeyEvent
	if err := f.db.First(&source, "commit_id = ?", first.CommitID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&event, "source_commit_id = ?", first.CommitID).Error; err != nil {
		t.Fatal(err)
	}
	if !event.CreatedAt.Equal(source.CreatedAt) || source.Seal() != nil {
		t.Fatal("repair changed immutable source or original event timestamp")
	}
}

func TestGameKeyEventBackfillOldBranchDoesNotLeakFuture(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 1)
	archiveBackfillCandidate(t, f, f.rootID, 2, false, "共同历史")
	archiveBackfillCandidate(t, f, f.rootID, 3, false, "旧分支未来")
	fork := f.fork(t, f.rootID, f.rootID, 2)
	archiveBackfillCandidate(t, f, fork, 3, true, "新分支结果")
	for _, timeline := range []string{f.rootID, fork} {
		result, err := f.repo.BackfillKeyEvents(ctx, KeyEventBackfillRequest{RoomID: f.roomID, TimelineID: timeline, Limit: 100, Apply: true})
		if err != nil || !result.Done {
			t.Fatalf("backfill=%+v err=%v", result, err)
		}
	}
	visible, more, err := f.repo.ListVisibleKeyEvents(ctx, f.roomID, fork, 0, 0, 100)
	if err != nil || more || len(visible) != 2 || visible[0].Name != "共同历史" || visible[1].Name != "新分支结果" {
		t.Fatalf("visible=%+v err=%v", visible, err)
	}
	other := newMemoryFixture(t)
	if _, err := f.repo.BackfillKeyEvents(ctx, KeyEventBackfillRequest{RoomID: other.roomID, TimelineID: f.rootID, Limit: 100, Apply: true}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-room request accepted: %v", err)
	}
}

func TestGameKeyEventBackfillConflictRollsBackWholeBatch(t *testing.T) {
	for _, fault := range []string{"empty hash", "tampered payload", "gap", "conflicting event", "extra event"} {
		t.Run(fault, func(t *testing.T) {
			f := newMemoryFixture(t)
			ctx := context.Background()
			f.archive(t, f.rootID, 1, 1)
			archiveBackfillCandidate(t, f, f.rootID, 2, false, "前一条有效候选")
			last := archiveBackfillCandidate(t, f, f.rootID, 3, false, "后一条候选")
			var issue error
			want := ErrMemoryConflict
			switch fault {
			case "empty hash":
				issue = f.db.Model(last).Update("payload_hash", "").Error
			case "tampered payload":
				issue = f.db.Model(last).Update("payload", []byte(`{"response":{"narrative":"篡改"}}`)).Error
			case "gap":
				issue = f.db.Delete(last).Error
				want = ErrMemoryGap
			default:
				event := model.KeyEvent{RoomID: f.roomID, TimelineID: f.rootID, Position: 3, SourceCommitID: last.CommitID, EventType: "trigger_event", Importance: "major", Name: "冲突文本", Description: "来源不符"}
				if fault == "extra event" {
					event.EventIndex = 8
				}
				issue = f.db.Create(&event).Error
			}
			if issue != nil {
				t.Fatal(issue)
			}
			before := keyEventCount(t, f)
			result, err := f.repo.BackfillKeyEvents(ctx, KeyEventBackfillRequest{RoomID: f.roomID, TimelineID: f.rootID, Limit: 100, Apply: true})
			if !errors.Is(err, want) || result != nil || keyEventCount(t, f) != before {
				t.Fatalf("failed batch advanced or partially wrote: result=%+v err=%v", result, err)
			}
		})
	}
}
