package repo

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"trpggame/internal/model"
)

func TestMemoryVisiblePathNestedAndSiblingForks(t *testing.T) {
	rootID, firstID, secondID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	branches := map[string]*model.GameTimeline{
		rootID:   {ID: rootID, RoomID: 41, DurablePosition: 30, HistoryComplete: true, Status: "active"},
		firstID:  {ID: firstID, RoomID: 41, ParentID: &rootID, ForkPosition: 20, DurablePosition: 23, HistoryComplete: true, Status: "active"},
		secondID: {ID: secondID, RoomID: 41, ParentID: &firstID, ForkPosition: 23, DurablePosition: 25, HistoryComplete: true, Status: "active"},
	}
	load := func(id string) (*model.GameTimeline, error) { return branches[id], nil }
	path, err := resolveVisiblePath(41, secondID, 25, load)
	want := []visibleSegment{{rootID, 0, 20, false}, {firstID, 20, 23, false}, {secondID, 23, 25, false}}
	if err != nil || !reflect.DeepEqual(path, want) {
		t.Fatalf("nested path %v, %v", path, err)
	}
	path, err = resolveVisiblePath(41, rootID, 10, load)
	if err != nil || !reflect.DeepEqual(path, []visibleSegment{{rootID, 0, 10, false}}) {
		t.Fatalf("ancestor save path %v %v", path, err)
	}
	if _, err := resolveVisiblePath(42, secondID, 25, load); !errors.Is(err, ErrMemoryBranch) {
		t.Fatalf("cross-room path: %v", err)
	}
	branches[rootID].ParentID = &secondID
	if _, err := resolveVisiblePath(41, secondID, 25, load); !errors.Is(err, ErrMemoryBranch) {
		t.Fatalf("cycle: %v", err)
	}
}

func TestMemoryVisiblePathDepthAndIllegalWatermarks(t *testing.T) {
	ids := make([]string, model.GameMemoryMaxDepth+1)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	branches := make(map[string]*model.GameTimeline)
	for i, id := range ids {
		branch := &model.GameTimeline{ID: id, RoomID: 41, DurablePosition: uint64(i + 1), Status: "active", HistoryComplete: true}
		if i > 0 {
			branch.ParentID = &ids[i-1]
			branch.ForkPosition = uint64(i)
		}
		branches[id] = branch
	}
	load := func(id string) (*model.GameTimeline, error) { return branches[id], nil }
	if _, err := resolveVisiblePath(41, ids[255], 256, load); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveVisiblePath(41, ids[256], 257, load); !errors.Is(err, ErrMemoryBranch) {
		t.Fatalf("depth limit %v", err)
	}
	for _, position := range []uint64{0, 300} {
		if _, err := resolveVisiblePath(41, ids[1], position, load); !errors.Is(err, ErrMemoryBranch) {
			t.Fatalf("illegal watermark %v", err)
		}
	}
	branches[ids[0]].HistoryComplete = false
	if _, err := resolveVisiblePath(41, ids[1], 2, load); !errors.Is(err, ErrMemoryBranch) {
		t.Fatalf("inconsistent history completeness %v", err)
	}
	path, err := resolveVisiblePath(41, ids[0], 1, load)
	if err != nil || !path[0].IncludeZero {
		t.Fatal("legacy baseline must be visible")
	}
}

func TestMemorySaveMetadataAndContentHashes(t *testing.T) {
	id := uuid.NewString()
	position := uint64(20)
	snapshot, _ := json.Marshal(map[string]any{"schema_version": 3, "mode": "solo", "runtime": map[string]any{"version": 1},
		"memory": map[string]any{"timeline_id": id, "position": position, "history_complete": true}})
	save := &model.GameSave{RoomID: 41, RoundNumber: 10, TimelineID: &id, MemoryPosition: &position, RedisSnapshot: snapshot, RecentMessages: []byte(`[]`)}
	if memory, err := validateMemorySave(save); !memory || err != nil {
		t.Fatal(err)
	}
	hash, err := memorySaveHash(save)
	if err != nil {
		t.Fatal(err)
	}
	save.ID, save.SaveName = 123, "renamed"
	if got, _ := memorySaveHash(save); got != hash {
		t.Fatal("name or ID changed gameplay hash")
	}
	save.SummaryMemory = "changed"
	if got, _ := memorySaveHash(save); got == hash {
		t.Fatal("summary excluded from hash")
	}
	position++
	if _, err := validateMemorySave(save); err == nil {
		t.Fatal("position mismatch accepted")
	}
	save.TimelineID, save.MemoryPosition = nil, nil
	if _, err := validateMemorySave(save); err == nil {
		t.Fatal("V3 without SQL metadata accepted")
	}
}
