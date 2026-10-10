package model

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validMemoryRecord() GameActionRecord {
	actor := uint(7)
	return GameActionRecord{CommitID: uuid.NewString(), RoomID: 41, TimelineID: uuid.NewString(), Position: 2,
		Kind: "action", ActorID: &actor, RequestNamespace: "client", RequestID: uuid.NewString(),
		Fingerprint: strings.Repeat("a", 64), SourceGeneration: uuid.NewString(), SourceRevision: 1,
		TurnBefore: 0, TurnAfter: 1, PayloadVersion: 1, Payload: json.RawMessage(`{"n":9007199254740993,"text":"行动"}`)}
}

func TestMemoryRecordHashPreservesPrecisionAndIgnoresTimestamp(t *testing.T) {
	record := validMemoryRecord()
	if err := record.Seal(); err != nil {
		t.Fatal(err)
	}
	hash := record.PayloadHash
	record.RequestID = strings.ToUpper(record.RequestID)
	record.Payload = json.RawMessage(`{ "text": "行动", "n": 9007199254740993 }`)
	record.CreatedAt = time.Now()
	if err := record.Seal(); err != nil || record.PayloadHash != hash {
		t.Fatalf("canonical replay: %v", err)
	}
	if !bytes.Contains(record.Payload, []byte("9007199254740993")) {
		t.Fatal("integer precision was lost")
	}
	record.TurnAfter++
	if record.Seal() == nil {
		t.Fatal("changed metadata accepted with old hash")
	}
}

func TestMemoryRecordRejectsInvalidInput(t *testing.T) {
	mutations := []func(*GameActionRecord){
		func(r *GameActionRecord) { r.Payload = []byte(`[]`) },
		func(r *GameActionRecord) { r.Payload = []byte(`{} {}`) },
		func(r *GameActionRecord) { r.Payload = []byte(`null`) },
		func(r *GameActionRecord) {
			r.Payload = []byte(`{"text":"` + strings.Repeat("x", GameMemoryMaxPayloadBytes) + `"}`)
		},
		func(r *GameActionRecord) { r.Kind = "narrative_chunk" },
		func(r *GameActionRecord) { r.Position = 0 },
		func(r *GameActionRecord) { r.ActorID = nil },
		func(r *GameActionRecord) { r.RequestNamespace = "timeout" },
		func(r *GameActionRecord) { r.CommitID = uuid.Nil.String() },
		func(r *GameActionRecord) { r.SourceRevision = 0 },
		func(r *GameActionRecord) { r.RoundAfter = -1 },
		func(r *GameActionRecord) { r.PayloadHash = strings.Repeat("b", 64) },
	}
	for i, mutate := range mutations {
		r := validMemoryRecord()
		mutate(&r)
		if r.Seal() == nil {
			t.Fatalf("invalid mutation %d accepted", i)
		}
	}
}
