package model

type MemoryControlSource struct {
	Memory      *GameArchiveRuntime
	Generation  string
	Status      RoomStatus
	Turn, Round int
}

type MemoryControlProgress struct {
	Phase        string `json:"phase"`
	SnapshotHash string `json:"snapshot_hash"`
	SourceTurn   int    `json:"source_turn"`
	SourceRound  int    `json:"source_round"`
}

// MemoryRuntimeReplacement is built from a validated, journaled target image.
type MemoryRuntimeReplacement struct {
	RoomID, OwnerID                       uint
	Mode, Kind, OperationID, SnapshotHash string
	SourceTimeline, SourceGeneration      string
	SourceRevision                        uint64
	TimelineID, Generation                string
	Revision, Position                    uint64
	TurnTimeoutMS                         int64
	Solo                                  *SoloRuntimeSnapshot
	Multiplayer                           *MultiplayerRuntimeSnapshot
	Roster                                []uint
	Opening                               *GameActionRecord
	Recovery                              bool
}
