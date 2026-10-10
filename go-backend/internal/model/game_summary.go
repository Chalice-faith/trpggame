package model

import (
	"encoding/json"
	"time"
)

type GameSummary struct {
	TimelineID      string `gorm:"primaryKey"`
	Version         uint64 `gorm:"primaryKey"`
	RoomID          uint
	ThroughPosition uint64
	Content         string
	SourceHash      string
	CreatedAt       time.Time
}

func (GameSummary) TableName() string { return "game_summaries" }

type GameSummaryWork struct {
	ID              string `gorm:"primaryKey"`
	RoomID          uint
	TimelineID      string
	StateRevision   uint64
	ExpectedVersion uint64
	FromPosition    uint64
	ThroughPosition uint64
	SourceHash      string
	PreviousSummary string
	Messages        json.RawMessage `gorm:"type:json"`
	Status          string
	OwnerToken      string
	LeaseUntil      time.Time
	NextRetryAt     time.Time
	Attempts        uint
	ErrorClass      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (GameSummaryWork) TableName() string { return "game_summary_work" }

// MemoryContext is a Go-authorized, branch-scoped input; Python only consumes it.
type MemoryContext struct {
	TimelineID             string           `json:"timeline_id"`
	ThroughPosition        uint64           `json:"through_position"`
	SummaryThroughPosition uint64           `json:"summary_through_position"`
	SummaryVersion         uint64           `json:"summary_version"`
	Summary                string           `json:"summary"`
	Messages               []RuntimeMessage `json:"messages"`
	KeyEvents              []string         `json:"key_events"`
	Degraded               bool             `json:"degraded"`
}
