package model

import (
	"encoding/json"
	"time"
)

type MessageType string

const (
	MessageTypeText   MessageType = "text"
	MessageTypeSystem MessageType = "system"
)

type Message struct {
	ID              uint            `gorm:"primaryKey" json:"id"`
	ConversationID  uint            `gorm:"not null" json:"conversation_id"`
	Seq             uint64          `gorm:"not null" json:"seq"`
	SenderID        uint            `gorm:"not null" json:"sender_id"`
	ClientMessageID string          `gorm:"type:char(36);not null" json:"client_message_id"`
	MessageType     MessageType     `gorm:"size:20;not null" json:"message_type"`
	Content         string          `gorm:"type:text;not null" json:"content"`
	Metadata        json.RawMessage `gorm:"type:json;not null;default:(JSON_OBJECT())" json:"metadata"`
	CreatedAt       time.Time       `json:"created_at"`
}

func (Message) TableName() string { return "messages" }
