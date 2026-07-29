package model

import (
	"time"

	"gorm.io/gorm"
)

// RoomStatus 房间状态
type RoomStatus string

const (
	RoomStatusWaiting  RoomStatus = "waiting"
	RoomStatusPlaying  RoomStatus = "playing"
	RoomStatusPaused   RoomStatus = "paused"
	RoomStatusFinished RoomStatus = "finished"
)

// GameRoom 游戏房间
type GameRoom struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	ScriptID    uint           `gorm:"index;not null" json:"script_id"`
	HostUserID  uint           `gorm:"index;not null" json:"host_user_id"`
	Title       string         `gorm:"size:200;not null" json:"title"`
	Status      RoomStatus     `gorm:"size:20;default:waiting" json:"status"`
	MaxPlayers  int            `gorm:"default:6" json:"max_players"`
	CurrentTurn int            `gorm:"default:0" json:"current_turn"`
	RoundCount  int            `gorm:"default:0" json:"round_count"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// TableName 自定义表名
func (GameRoom) TableName() string {
	return "game_rooms"
}

// RoomPlayer 房间玩家关联
type RoomPlayer struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	RoomID     uint      `gorm:"index;not null" json:"room_id"`
	UserID     uint      `gorm:"index;not null" json:"user_id"`
	CharacterID uint     `json:"character_id"`
	IsReady    bool      `gorm:"default:false" json:"is_ready"`
	TurnOrder  int       `gorm:"default:0" json:"turn_order"`
	JoinedAt   time.Time `json:"joined_at"`
}

// TableName 自定义表名
func (RoomPlayer) TableName() string {
	return "room_players"
}

// GameSave 游戏存档
type GameSave struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	RoomID    uint      `gorm:"index;not null" json:"room_id"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	Name      string    `gorm:"size:100" json:"name"`
	RoundNum  int       `json:"round_num"`
	SaveData  string    `gorm:"type:json" json:"save_data"` // JSON 格式完整快照
	CreatedAt time.Time `json:"created_at"`
}

// TableName 自定义表名
func (GameSave) TableName() string {
	return "game_saves"
}
