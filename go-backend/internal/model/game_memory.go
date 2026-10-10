package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
)

const (
	GameMemoryMaxPayloadBytes = 1 << 20
	GameMemoryMaxDepth        = 256
	GameMemoryPayloadVersion  = 1
)

var ErrInvalidMemoryData = errors.New("invalid game memory data")

type GameMemoryState struct {
	RoomID            uint `gorm:"primaryKey"`
	ActiveTimelineID  *string
	Status            string
	Revision          uint64
	ActiveOperationID *string
	UpdatedAt         time.Time
}

func (GameMemoryState) TableName() string { return "game_memory_states" }

type GameTimeline struct {
	ID              string `gorm:"primaryKey;size:36"`
	RoomID          uint
	ParentID        *string
	ForkPosition    uint64
	DurablePosition uint64
	OriginSaveID    *uint
	HistoryComplete bool
	Status          string
	CreatedAt       time.Time
}

func (GameTimeline) TableName() string { return "game_timelines" }

// Hash covers all immutable metadata and canonical payload, excluding timestamps.
type GameActionRecord struct {
	CommitID         string `gorm:"primaryKey;size:36"`
	RoomID           uint
	TimelineID       string
	Position         uint64
	Kind             string
	ActorID          *uint
	RequestNamespace string
	RequestID        string
	Fingerprint      string
	SourceGeneration string
	SourceRevision   uint64
	TurnBefore       int
	TurnAfter        int
	RoundBefore      int
	RoundAfter       int
	PayloadVersion   int
	Payload          json.RawMessage `gorm:"type:json"`
	PayloadHash      string
	CreatedAt        time.Time
}

func (GameActionRecord) TableName() string { return "game_action_records" }

type GameMemoryOperation struct {
	OperationID      string `gorm:"primaryKey;size:36"`
	RoomID           uint
	Kind             string
	Fingerprint      string
	SourceTimelineID *string
	SourceGeneration string
	TargetTimelineID *string
	TargetSaveID     *uint
	Phase            string
	TargetSnapshot   json.RawMessage `gorm:"type:json"`
	SnapshotHash     string
	Attempts         uint
	NextRetryAt      time.Time
	ErrorClass       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (GameMemoryOperation) TableName() string { return "game_memory_operations" }

// CanonicalMemoryJSON preserves integer precision and rejects trailing JSON and non-objects.
func CanonicalMemoryJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > GameMemoryMaxPayloadBytes {
		return nil, ErrInvalidMemoryData
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, ErrInvalidMemoryData
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, ErrInvalidMemoryData
	}
	return json.Marshal(value)
}

func MemoryHash(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:]), nil
}

func ValidMemoryUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func ValidMemoryHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

// Seal normalizes the payload and computes its hash. Supplied hashes must match.
func (record *GameActionRecord) Seal() error {
	if record != nil {
		for _, field := range []*string{&record.CommitID, &record.TimelineID, &record.RequestID, &record.SourceGeneration} {
			if id, err := uuid.Parse(*field); err == nil {
				*field = id.String()
			}
		}
	}
	if record == nil || record.RoomID == 0 || !ValidMemoryUUID(record.CommitID) ||
		!ValidMemoryUUID(record.TimelineID) || !ValidMemoryUUID(record.RequestID) ||
		!ValidMemoryUUID(record.SourceGeneration) || !ValidMemoryHash(record.Fingerprint) ||
		record.SourceRevision == 0 || record.PayloadVersion != GameMemoryPayloadVersion ||
		record.TurnBefore < 0 || record.TurnAfter < record.TurnBefore ||
		record.RoundBefore < 0 || record.RoundAfter < record.RoundBefore ||
		(record.ActorID != nil && *record.ActorID == 0) {
		return ErrInvalidMemoryData
	}
	expectedNamespace := "client"
	switch record.Kind {
	case "opening":
		expectedNamespace = "opening"
	case "skip_timeout":
		expectedNamespace = "timeout"
	case "legacy_baseline":
		expectedNamespace = "legacy"
	case "action", "skip_manual":
		if record.ActorID == nil {
			return ErrInvalidMemoryData
		}
	default:
		return ErrInvalidMemoryData
	}
	if record.RequestNamespace != expectedNamespace || (record.Kind == "legacy_baseline") != (record.Position == 0) {
		return ErrInvalidMemoryData
	}
	payload, err := CanonicalMemoryJSON(record.Payload)
	if err != nil {
		return err
	}
	copy := *record
	copy.Payload = payload
	copy.PayloadHash = ""
	copy.CreatedAt = time.Time{}
	hash, err := MemoryHash(copy)
	if err != nil {
		return err
	}
	if record.PayloadHash != "" && record.PayloadHash != hash {
		return ErrInvalidMemoryData
	}
	record.Payload, record.PayloadHash = payload, hash
	return nil
}

func (operation *GameMemoryOperation) Seal() error {
	if operation != nil {
		for _, field := range []*string{&operation.OperationID, &operation.SourceGeneration} {
			if id, err := uuid.Parse(*field); err == nil {
				*field = id.String()
			}
		}
		for _, field := range []**string{&operation.SourceTimelineID, &operation.TargetTimelineID} {
			if *field != nil {
				if id, err := uuid.Parse(**field); err == nil {
					canonical := id.String()
					*field = &canonical
				}
			}
		}
	}
	if operation == nil || operation.RoomID == 0 || !ValidMemoryUUID(operation.OperationID) ||
		!ValidMemoryUUID(operation.SourceGeneration) || !ValidMemoryHash(operation.Fingerprint) ||
		(operation.SourceTimelineID != nil && !ValidMemoryUUID(*operation.SourceTimelineID)) ||
		(operation.TargetTimelineID != nil && !ValidMemoryUUID(*operation.TargetTimelineID)) ||
		(operation.TargetSaveID != nil && *operation.TargetSaveID == 0) {
		return ErrInvalidMemoryData
	}
	switch operation.Kind {
	case "start":
		if operation.SourceTimelineID != nil || operation.TargetTimelineID == nil || operation.TargetSaveID != nil {
			return ErrInvalidMemoryData
		}
	case "load":
		if operation.SourceTimelineID == nil || operation.TargetTimelineID == nil || operation.TargetSaveID == nil {
			return ErrInvalidMemoryData
		}
	case "end":
		if operation.SourceTimelineID == nil || operation.TargetTimelineID != nil || operation.TargetSaveID != nil {
			return ErrInvalidMemoryData
		}
	default:
		return ErrInvalidMemoryData
	}
	payload, err := CanonicalMemoryJSON(operation.TargetSnapshot)
	if err != nil {
		return err
	}
	hash, err := MemoryHash(json.RawMessage(payload))
	if err != nil {
		return err
	}
	if operation.SnapshotHash != "" && operation.SnapshotHash != hash {
		return ErrInvalidMemoryData
	}
	operation.TargetSnapshot, operation.SnapshotHash = payload, hash
	return nil
}
