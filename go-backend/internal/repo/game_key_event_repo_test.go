package repo

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"trpggame/internal/model"
)

func TestKeyEventsFromCommittedAction(t *testing.T) {
	base := model.GameActionRecord{CommitID: uuid.NewString(), RoomID: 9, TimelineID: uuid.NewString(), Position: 12, Kind: "action"}
	tests := []struct {
		name, payload string
		want          int
		invalid       bool
	}{
		{"solo", `{"response":{"effects":{"events":[{"name":"开启石门","description":"机关已解除"}]}}}`, 1, false},
		{"multiplayer", `{"response":{"multiplayer_effects":{"events":[{"name":"缔结盟约","description":"双方确认"}]}}}`, 1, false},
		{"narrative keyword", `{"response":{"narrative":"角色死亡"}}`, 0, false},
		{"skip response", `{"response":{"effects":{"events":[]}}}`, 0, false},
		{"blank event", `{"response":{"effects":{"events":[{"name":" ","description":"x"}]}}}`, 0, true},
		{"overlong event", `{"response":{"effects":{"events":[{"name":"` + strings.Repeat("甲", 201) + `","description":"x"}]}}}`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := base
			record.Payload = []byte(tt.payload)
			events, err := keyEventsFromRecord(&record)
			if (err != nil) != tt.invalid || len(events) != tt.want {
				t.Fatalf("events=%+v err=%v", events, err)
			}
			if tt.want > 0 && (events[0].SourceCommitID != record.CommitID || events[0].Position != record.Position || events[0].EventType != "trigger_event") {
				t.Fatalf("wrong provenance: %+v", events[0])
			}
		})
	}
}
