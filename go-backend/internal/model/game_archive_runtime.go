package model

import (
	"encoding/json"
	"time"
)

// Runtime versions remain V1/V2; these fields describe the independent memory ledger.
type GameArchiveRuntime struct {
	TimelineID         string `json:"timeline_id"`
	HeadPosition       uint64 `json:"head_position"`
	DurablePosition    uint64 `json:"durable_position"`
	ArchiveState       string `json:"archive_state"`
	ControlOperationID string `json:"control_operation_id,omitempty"`
	Revision           uint64 `json:"revision"`
}

type GameArchiveExpectation struct{ TimelineID, Generation string }

// Bind is an internal capability initialization, with no application enablement entry point yet.
type GameArchiveBinding struct {
	RoomID                       uint
	TimelineID, Generation, Mode string
	Position, Revision           uint64
	ExpectedTurn                 int
	TurnTimeout                  time.Duration
}

type PendingGameArchive struct {
	Attempts         uint
	Record           *GameActionRecord
	ResponseJSON     json.RawMessage
	BoundarySnapshot json.RawMessage
	OwnerToken       string
}

type GameArchiveACK struct {
	RoomID                                        uint
	TimelineID, CommitID, PayloadHash, OwnerToken string
	Position                                      uint64
}

type GameArchiveACKResult struct {
	Duplicate        bool
	RuntimeAvailable bool
	DeadlineAt       *time.Time
	Memory           *GameArchiveRuntime
}

type PendingMemoryAutoSave struct {
	TimelineID  string
	Position    uint64
	RoundNumber int
	Snapshot    json.RawMessage
}
