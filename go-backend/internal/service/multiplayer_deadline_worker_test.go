package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"trpggame/internal/model"
)

type deadlineAutoSaveHarness struct {
	deadlineErr   error
	autoSaveIDs   []uint
	deadlineCalls int
	flushed       []uint
}

func (h *deadlineAutoSaveHarness) ListDueMultiplayerDeadlines(context.Context, time.Time, int) ([]model.MultiplayerDeadlineTask, error) {
	h.deadlineCalls++
	return nil, h.deadlineErr
}

func (h *deadlineAutoSaveHarness) ListPendingMultiplayerAutoSaveRooms(context.Context, int) ([]uint, error) {
	return append([]uint(nil), h.autoSaveIDs...), nil
}

func (h *deadlineAutoSaveHarness) ProcessMultiplayerDeadline(context.Context, model.MultiplayerDeadlineTask, time.Time) error {
	return nil
}

func (h *deadlineAutoSaveHarness) FlushPendingMultiplayerAutoSaves(_ context.Context, roomID uint) error {
	h.flushed = append(h.flushed, roomID)
	return nil
}

func TestMultiplayerDeadlineWorkerRecoversPendingAutoSavesEvenWhenDeadlineScanFails(t *testing.T) {
	harness := &deadlineAutoSaveHarness{deadlineErr: errors.New("deadline index unavailable"), autoSaveIDs: []uint{41, 42}}
	worker := NewMultiplayerDeadlineWorker(harness, harness)
	worker.processDue()
	if harness.deadlineCalls != 1 || len(harness.flushed) != 2 || harness.flushed[0] != 41 || harness.flushed[1] != 42 {
		t.Fatalf("deadline calls=%d auto-save flushes=%#v", harness.deadlineCalls, harness.flushed)
	}
}
