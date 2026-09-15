package model

import "time"

type GroupRole string
type GroupMemberStatus string

const (
	GroupRoleMember GroupRole = "member"
	GroupRoleAdmin  GroupRole = "admin"
	GroupRoleOwner  GroupRole = "owner"

	GroupMemberStatusActive GroupMemberStatus = "active"
	GroupMemberStatusLeft   GroupMemberStatus = "left"
)

type Group struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:80;not null" json:"name"`
	AvatarURL string    `gorm:"size:2048;not null" json:"avatar_url"`
	OwnerID   uint      `gorm:"not null" json:"owner_id"`
	Version   uint64    `gorm:"not null;default:1" json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Group) TableName() string { return "groups" }

type GroupMember struct {
	ID        uint              `gorm:"primaryKey" json:"id"`
	GroupID   uint              `gorm:"not null" json:"group_id"`
	UserID    uint              `gorm:"not null" json:"user_id"`
	Role      GroupRole         `gorm:"size:16;not null" json:"role"`
	Status    GroupMemberStatus `gorm:"size:16;not null" json:"status"`
	JoinedAt  time.Time         `json:"joined_at"`
	LeftAt    *time.Time        `json:"left_at,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func (GroupMember) TableName() string { return "group_members" }

func (m GroupMember) Active() bool { return m.Status == GroupMemberStatusActive }
