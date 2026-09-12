package model

import "time"

type ConversationType string

const (
	ConversationTypeDirect ConversationType = "direct"
	ConversationTypeGroup  ConversationType = "group"
)

type Conversation struct {
	ID            uint             `gorm:"primaryKey" json:"id"`
	Type          ConversationType `gorm:"size:16;not null" json:"type"`
	DirectLowID   *uint            `json:"direct_low_id,omitempty"`
	DirectHighID  *uint            `json:"direct_high_id,omitempty"`
	GroupID       *uint            `json:"group_id,omitempty"`
	LastSeq       uint64           `gorm:"not null;default:0" json:"last_seq"`
	LastMessageAt *time.Time       `json:"last_message_at,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

func (Conversation) TableName() string { return "conversations" }

func (c Conversation) IncludesDirectUser(userID uint) bool {
	return userID > 0 && c.Type == ConversationTypeDirect &&
		((c.DirectLowID != nil && *c.DirectLowID == userID) ||
			(c.DirectHighID != nil && *c.DirectHighID == userID))
}

func (c Conversation) DirectPeerID(userID uint) uint {
	if c.DirectLowID != nil && *c.DirectLowID == userID && c.DirectHighID != nil {
		return *c.DirectHighID
	}
	if c.DirectHighID != nil && *c.DirectHighID == userID && c.DirectLowID != nil {
		return *c.DirectLowID
	}
	return 0
}

type ConversationMemberRole string
type ConversationMemberStatus string

const (
	ConversationMemberRoleMember ConversationMemberRole = "member"

	ConversationMemberStatusActive ConversationMemberStatus = "active"
	ConversationMemberStatusLeft   ConversationMemberStatus = "left"
)

type ConversationMember struct {
	ID             uint                     `gorm:"primaryKey" json:"id"`
	ConversationID uint                     `gorm:"not null" json:"conversation_id"`
	UserID         uint                     `gorm:"not null" json:"user_id"`
	Role           ConversationMemberRole   `gorm:"size:16;not null" json:"role"`
	Status         ConversationMemberStatus `gorm:"size:16;not null" json:"status"`
	LastReadSeq    uint64                   `gorm:"not null;default:0" json:"last_read_seq"`
	JoinedAt       time.Time                `json:"joined_at"`
	LeftAt         *time.Time               `json:"left_at,omitempty"`
}

func (ConversationMember) TableName() string { return "conversation_members" }
