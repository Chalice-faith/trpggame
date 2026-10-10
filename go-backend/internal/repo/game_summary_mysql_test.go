//go:build integration

package repo

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
	"trpggame/internal/model"
)

func TestGameSummaryMySQL84RetryLeaseAndBranchFence(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 6)
	work, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || work == nil {
		t.Fatalf("claim: %v %v", work, err)
	}
	duplicate, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || duplicate != nil {
		t.Fatalf("double claim: %v %v", duplicate, err)
	}
	if err := f.repo.RetrySummary(ctx, work, "model"); err != nil {
		t.Fatal(err)
	}
	f.db.Model(&model.GameSummaryWork{}).Where("id = ?", work.ID).Update("next_retry_at", time.Now().Add(-time.Second))
	restarted := NewGameMemoryRepo(f.db)
	reclaimed, err := restarted.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || reclaimed == nil || reclaimed.ID != work.ID || reclaimed.OwnerToken == work.OwnerToken {
		t.Fatalf("retry: %v %v", reclaimed, err)
	}
	candidate := strings.Repeat("已归档事实。", 40)
	if err := f.repo.CompleteSummary(ctx, work, candidate); !errors.Is(err, ErrMemoryBusy) {
		t.Fatalf("stale lease: %v", err)
	}
	if err := restarted.CompleteSummary(ctx, reclaimed, candidate); err != nil {
		t.Fatal(err)
	}
	memory, err := f.repo.LoadMemoryContext(ctx, f.roomID, f.rootID)
	if err != nil || memory.SummaryVersion != 1 || memory.SummaryThroughPosition != 6 || len(memory.Messages) != 6 {
		t.Fatalf("memory: %+v %v", memory, err)
	}
	f.archive(t, f.rootID, 7, 11)
	old, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || old == nil {
		t.Fatalf("second claim: %v %v", old, err)
	}
	branch := f.fork(t, f.rootID, f.rootID, 6)
	if err := f.repo.CompleteSummary(ctx, old, candidate); !errors.Is(err, ErrMemoryCursor) {
		t.Fatalf("late reply: %v", err)
	}
	memory, err = f.repo.LoadMemoryContext(ctx, f.roomID, branch)
	if err != nil || memory.SummaryThroughPosition != 6 || len(memory.Messages) != 6 {
		t.Fatalf("fork: %+v %v", memory, err)
	}
	if _, err = f.repo.LoadMemoryContext(ctx, f.roomID, f.rootID); !errors.Is(err, ErrMemoryCursor) {
		t.Fatalf("old context: %v", err)
	}
}

func TestGameSummaryMySQL84SkipsDoNotTriggerAndCorruptionRejected(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 4)
	for position := uint64(5); position <= 9; position++ {
		record := f.record(f.rootID, position)
		record.Kind = "skip_manual"
		if _, err := f.repo.Archive(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if work, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute); err != nil || work != nil {
		t.Fatalf("skips triggered: %v %v", work, err)
	}
	f.archive(t, f.rootID, 10, 11)
	work, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || work == nil {
		t.Fatalf("five actions: %v %v", work, err)
	}
	f.db.Model(&model.GameActionRecord{}).Where("room_id = ? AND position = ?", f.roomID, 10).Update("payload_hash", strings.Repeat("a", 64))
	if err := f.repo.CompleteSummary(ctx, work, strings.Repeat("正式事实。", 40)); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("corrupt source: %v", err)
	}
	var count int64
	f.db.Model(&model.GameSummary{}).Where("room_id = ?", f.roomID).Count(&count)
	if count != 0 {
		t.Fatal("corrupt source published")
	}
}

func TestGameSummaryMySQL84ForkExcludesFutureSummary(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 11)
	for i := 0; i < 2; i++ {
		work, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
		if err != nil || work == nil {
			t.Fatalf("claim: %v %v", work, err)
		}
		if err := f.repo.CompleteSummary(ctx, work, strings.Repeat("正式事实。", 40)); err != nil {
			t.Fatal(err)
		}
	}
	branch := f.fork(t, f.rootID, f.rootID, 6)
	memory, err := f.repo.LoadMemoryContext(ctx, f.roomID, branch)
	if err != nil || memory.SummaryThroughPosition != 6 || memory.SummaryVersion != 1 {
		t.Fatalf("future summary leaked: %+v %v", memory, err)
	}
	f.archive(t, branch, 7, 11)
	work, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || work == nil || work.ExpectedVersion != 1 {
		t.Fatalf("inherited version: %v %v", work, err)
	}
	if err := f.repo.CompleteSummary(ctx, work, strings.Repeat("新分支事实。", 40)); err != nil {
		t.Fatal(err)
	}
	memory, err = f.repo.LoadMemoryContext(ctx, f.roomID, branch)
	if err != nil || memory.SummaryVersion != 2 || memory.SummaryThroughPosition != 11 {
		t.Fatalf("branch version regressed: %+v %v", memory, err)
	}
}

func TestGameSummaryMySQL84LoadFreezesSavedSummary(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 6)
	save := f.save(t, f.rootID, 6, false)
	work, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || work == nil {
		t.Fatalf("claim: %v %v", work, err)
	}
	if err := f.repo.CompleteSummary(ctx, work, strings.Repeat("后生成的摘要。", 30)); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	op := f.operation("load", &f.rootID, &id, &save.ID)
	target := &model.GameTimeline{ID: id, RoomID: f.roomID, ParentID: &f.rootID, ForkPosition: 6, DurablePosition: 6, OriginSaveID: &save.ID, HistoryComplete: true}
	if _, err := f.repo.PrepareOperation(ctx, op, target); err != nil {
		t.Fatal(err)
	}
	f.complete(t, op)
	memory, err := f.repo.LoadMemoryContext(ctx, f.roomID, id)
	if err != nil || memory.Summary != "" || memory.SummaryThroughPosition != 0 || len(memory.Messages) != 6 {
		t.Fatalf("save baseline changed retroactively: %+v %v", memory, err)
	}
}

func TestGameSummaryMySQL84ExcludedBacklogMakesProgress(t *testing.T) {
	f := newMemoryFixture(t)
	ctx := context.Background()
	f.archive(t, f.rootID, 1, 6)
	initial, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || initial == nil {
		t.Fatalf("initial claim: %v %v", initial, err)
	}
	if err := f.repo.CompleteSummary(ctx, initial, strings.Repeat("正式事实。", 40)); err != nil {
		t.Fatal(err)
	}
	for position := uint64(7); position <= 111; position++ {
		record := f.record(f.rootID, position)
		record.Kind = "skip_manual"
		if _, err := f.repo.Archive(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	work, err := f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || work != nil {
		t.Fatalf("excluded backlog invoked model: %v %v", work, err)
	}
	memory, err := f.repo.LoadMemoryContext(ctx, f.roomID, f.rootID)
	if err != nil || memory.SummaryThroughPosition != 106 || memory.Summary != strings.Repeat("正式事实。", 40) {
		t.Fatalf("excluded backlog stalled: %+v %v", memory, err)
	}
	f.archive(t, f.rootID, 112, 116)
	work, err = f.repo.ClaimSummary(ctx, f.roomID, 5, time.Minute)
	if err != nil || work == nil || work.FromPosition != 106 || work.ThroughPosition != 116 {
		t.Fatalf("actions behind backlog stalled: %v %v", work, err)
	}
}
