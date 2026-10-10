package model

import "time"

// KeyEvent keeps a validated event tied to its immutable action source.
type KeyEvent struct {
	ID              uint64    `gorm:"primaryKey" json:"id"`
	RoomID          uint      `json:"-"`
	TimelineID      string    `json:"timeline_id"`
	Position        uint64    `json:"position"`
	SourceCommitID  string    `json:"source_commit_id"`
	EventIndex      uint16    `json:"event_index"`
	EventType       string    `json:"event_type"`
	Importance      string    `json:"importance"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	CreatedAt       time.Time `json:"created_at"`
	SourceAction    string    `gorm:"-" json:"source_action,omitempty"`
	SourceNarrative string    `gorm:"-" json:"source_narrative,omitempty"`
}

func (KeyEvent) TableName() string { return "key_events" }
