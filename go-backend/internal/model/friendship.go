package model

import "time"

// FriendshipStatus 表示一对用户的关系状态。
type FriendshipStatus string

const (
	FriendshipStatusPending  FriendshipStatus = "pending"
	FriendshipStatusAccepted FriendshipStatus = "accepted"
	FriendshipStatusRejected FriendshipStatus = "rejected"
	FriendshipStatusRemoved  FriendshipStatus = "removed"
)

// Friendship 用规范化的 low/high 用户 ID 唯一表示一对用户。
type Friendship struct {
	ID          uint             `gorm:"primaryKey" json:"id"`
	UserLowID   uint             `gorm:"not null;uniqueIndex:uk_friendships_pair" json:"user_low_id"`
	UserHighID  uint             `gorm:"not null;uniqueIndex:uk_friendships_pair" json:"user_high_id"`
	RequestedBy uint             `gorm:"not null" json:"requested_by"`
	Status      FriendshipStatus `gorm:"size:20;not null" json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	RespondedAt *time.Time       `json:"responded_at,omitempty"`
	RemovedAt   *time.Time       `json:"removed_at,omitempty"`
}

func (Friendship) TableName() string { return "friendships" }

// Includes 判断用户是否属于该关系。
func (f Friendship) Includes(userID uint) bool {
	return userID > 0 && (f.UserLowID == userID || f.UserHighID == userID)
}

// PeerID 返回关系中另一方的用户 ID。
func (f Friendship) PeerID(userID uint) uint {
	if f.UserLowID == userID {
		return f.UserHighID
	}
	if f.UserHighID == userID {
		return f.UserLowID
	}
	return 0
}
