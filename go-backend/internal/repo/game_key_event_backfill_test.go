package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"trpggame/internal/model"
)

func TestKeyEventBackfillRejectsInvalidScopeAndUnboundedBatch(t *testing.T) {
	for _, request := range []KeyEventBackfillRequest{
		{RoomID: 0, TimelineID: uuid.NewString(), Limit: 100},
		{RoomID: 1, TimelineID: "not-a-timeline", Limit: 100},
		{RoomID: 1, TimelineID: uuid.NewString(), Limit: 0},
		{RoomID: 1, TimelineID: uuid.NewString(), Limit: 101},
	} {
		if _, err := NewGameMemoryRepo(nil).BackfillKeyEvents(context.Background(), request); !errors.Is(err, model.ErrInvalidMemoryData) {
			t.Fatalf("invalid scope reached storage: %v", err)
		}
	}
}
