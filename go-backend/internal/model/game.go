package model

import (
	"encoding/json"
	"time"
)

// RoomStatus 房间状态
type RoomStatus string

const (
	RoomStatusWaiting RoomStatus = "waiting"
	RoomStatusPlaying RoomStatus = "playing"
	RoomStatusPaused  RoomStatus = "paused"
	RoomStatusEnded   RoomStatus = "ended"
)

// Valid 判断房间状态是否属于持久化契约允许的状态。
func (status RoomStatus) Valid() bool {
	switch status {
	case RoomStatusWaiting, RoomStatusPlaying, RoomStatusPaused, RoomStatusEnded:
		return true
	default:
		return false
	}
}

// GameRoom 游戏房间
type GameRoom struct {
	ID          uint            `gorm:"primaryKey" json:"id"`
	Name        string          `gorm:"size:128;not null" json:"name"`
	ScriptID    uint            `gorm:"index;not null" json:"script_id"`
	OwnerID     uint            `gorm:"index;not null" json:"owner_id"`
	Status      RoomStatus      `gorm:"size:20;not null;default:waiting" json:"status"`
	MaxPlayers  int             `gorm:"not null;default:1" json:"max_players"`
	CurrentTurn int             `gorm:"not null;default:0" json:"current_turn"`
	RoundNumber int             `gorm:"not null;default:0" json:"round_number"`
	TurnOrder   json.RawMessage `gorm:"type:json;not null;default:(JSON_ARRAY())" json:"turn_order"`
	IsSolo      bool            `gorm:"not null;default:true" json:"is_solo"`
	CreatedAt   time.Time       `json:"created_at"`
	EndedAt     *time.Time      `json:"ended_at,omitempty"`
}

// TableName 自定义表名
func (GameRoom) TableName() string {
	return "game_rooms"
}

// RoomPlayer 房间玩家关联
type RoomPlayer struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	RoomID      uint      `gorm:"index;not null" json:"room_id"`
	UserID      uint      `gorm:"index;not null" json:"user_id"`
	CharacterID *uint     `gorm:"index" json:"character_id,omitempty"`
	PlayerOrder int       `gorm:"not null;default:0" json:"player_order"`
	IsReady     bool      `gorm:"not null;default:false" json:"is_ready"`
	JoinedAt    time.Time `json:"joined_at"`
}

// RuntimeMessage 是 Redis 最近对话列表中的稳定 JSON 契约。
type RuntimeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SoloRuntimeState 描述单人房间首次进入 playing 状态时需要写入 Redis 的数据。
type SoloRuntimeState struct {
	RoomID      uint
	UserID      uint
	Status      RoomStatus
	Turn        int
	Summary     string
	PlayerState map[string]string
	Opening     RuntimeMessage
}

// ActionRuntimeMutation 是一次玩家行动在 Redis 中原子提交的内容。
type ActionRuntimeMutation struct {
	RoomID             uint
	UserID             uint
	ExpectedTurn       int
	RequestID          string
	RequestFingerprint string
	PlayerStateChanges map[string]string
	ItemMutations      []RuntimeItemMutation
	BuffMutations      []RuntimeBuffMutation
	Messages           []RuntimeMessage
	ResponseJSON       json.RawMessage
}

// RuntimeItemMutation 是 Redis 道具 SET 中的数量变化。
type RuntimeItemMutation struct {
	Name          string
	QuantityDelta int
	Description   string
}

// RuntimeBuffMutation 是 Redis Buff HASH 中的持续回合设置。
type RuntimeBuffMutation struct {
	Name     string
	Duration int
}

// RuntimeItem 是游戏运行态快照中的道具。
type RuntimeItem struct {
	Name        string `json:"name"`
	Quantity    int    `json:"quantity"`
	Description string `json:"description"`
}

// RuntimeBuff 是游戏运行态快照中的 Buff 或 Debuff。
type RuntimeBuff struct {
	Name     string `json:"name"`
	Duration int    `json:"duration"`
}

// SoloRuntimeSnapshotVersion 是当前可持久化运行态的结构版本。
const SoloRuntimeSnapshotVersion = 1

// SoloRuntimeSnapshot 是单人房间可持久化并原子恢复的 Redis 快照。
// Summary 和 RecentMessages 分别写入 game_saves 的独立字段，不重复编码进 redis_snapshot。
type SoloRuntimeSnapshot struct {
	Version        int               `json:"version"`
	RoomID         uint              `json:"-"`
	UserID         uint              `json:"-"`
	Status         RoomStatus        `json:"status"`
	Turn           int               `json:"turn"`
	TurnOrder      []uint            `json:"turn_order"`
	PlayerState    map[string]string `json:"player_state"`
	Items          []RuntimeItem     `json:"items"`
	Buffs          []RuntimeBuff     `json:"buffs"`
	Summary        string            `json:"-"`
	RecentMessages []RuntimeMessage  `json:"-"`
}

// ActionCommitResult 是 Redis 行动提交或幂等重放的结果。
type ActionCommitResult struct {
	Duplicate    bool
	CurrentTurn  int
	ResponseJSON json.RawMessage
}

// TableName 自定义表名
func (RoomPlayer) TableName() string {
	return "room_players"
}

// GameSave 游戏存档
type GameSave struct {
	ID             uint            `gorm:"primaryKey" json:"id"`
	RoomID         uint            `gorm:"index;not null" json:"room_id"`
	SaveName       string          `gorm:"size:256;not null" json:"save_name"`
	RoundNumber    int             `gorm:"not null;default:0" json:"round_number"`
	SummaryMemory  string          `gorm:"type:text;not null" json:"summary_memory"`
	RedisSnapshot  json.RawMessage `gorm:"type:json;not null" json:"redis_snapshot"`
	RecentMessages json.RawMessage `gorm:"type:json;not null" json:"recent_messages"`
	IsAuto         bool            `gorm:"not null;default:false" json:"is_auto"`
	CreatedAt      time.Time       `json:"created_at"`
}

// TableName 自定义表名
func (GameSave) TableName() string {
	return "game_saves"
}
