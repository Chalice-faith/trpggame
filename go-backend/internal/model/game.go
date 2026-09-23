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
	ID                 uint            `gorm:"primaryKey" json:"id"`
	Name               string          `gorm:"size:128;not null" json:"name"`
	ScriptID           uint            `gorm:"index;not null" json:"script_id"`
	OwnerID            uint            `gorm:"index;not null" json:"owner_id"`
	Status             RoomStatus      `gorm:"size:20;not null;default:waiting" json:"status"`
	MaxPlayers         int             `gorm:"not null;default:1" json:"max_players"`
	CurrentTurn        int             `gorm:"not null;default:0" json:"current_turn"`
	RoundNumber        int             `gorm:"not null;default:0" json:"round_number"`
	TurnOrder          json.RawMessage `gorm:"type:json;not null;default:(JSON_ARRAY())" json:"turn_order"`
	IsSolo             bool            `gorm:"not null" json:"is_solo"`
	RoomCode           *string         `gorm:"size:8;uniqueIndex" json:"room_code,omitempty"`
	Version            uint64          `gorm:"not null;default:1" json:"version"`
	TurnTimeoutSeconds int             `gorm:"not null;default:120" json:"turn_timeout_seconds"`
	CreatedAt          time.Time       `json:"created_at"`
	EndedAt            *time.Time      `json:"ended_at,omitempty"`
}

// TableName 自定义表名
func (GameRoom) TableName() string {
	return "game_rooms"
}

// RoomPlayer 房间玩家关联
type RoomPlayer struct {
	ID          uint             `gorm:"primaryKey" json:"id"`
	RoomID      uint             `gorm:"index;not null" json:"room_id"`
	UserID      uint             `gorm:"index;not null" json:"user_id"`
	CharacterID *uint            `gorm:"index" json:"character_id,omitempty"`
	PlayerOrder int              `gorm:"not null;default:0" json:"player_order"`
	IsReady     bool             `gorm:"not null;default:false" json:"is_ready"`
	Status      RoomPlayerStatus `gorm:"size:16;not null;default:active" json:"status"`
	JoinedAt    time.Time        `json:"joined_at"`
	LeftAt      *time.Time       `json:"left_at,omitempty"`
}

// RoomPlayerStatus 是多人房间成员关系的持久化状态。
type RoomPlayerStatus string

const (
	RoomPlayerStatusActive  RoomPlayerStatus = "active"
	RoomPlayerStatusLeft    RoomPlayerStatus = "left"
	RoomPlayerStatusRemoved RoomPlayerStatus = "removed"
)

// RuntimeMessage 是 Redis 最近对话列表中的稳定 JSON 契约。
type RuntimeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SoloRuntimeState 描述单人房间首次进入 playing 状态时需要写入 Redis 的数据。
type SoloRuntimeState struct {
	RoomID      uint
	UserID      uint
	Generation  string
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
	Generation         string
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
	Name          string `json:"name"`
	QuantityDelta int    `json:"quantity_delta"`
	Description   string `json:"description"`
}

// RuntimeBuffMutation 是 Redis Buff HASH 中的持续回合设置。
type RuntimeBuffMutation struct {
	Name     string `json:"name"`
	Duration int    `json:"duration"`
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

// MultiplayerRuntimeSnapshotVersion distinguishes the frozen-roster runtime from V1 solo saves.
const MultiplayerRuntimeSnapshotVersion = 2

// MultiplayerRuntimePlayer keeps each frozen member's character and mutable runtime state isolated.
type MultiplayerRuntimePlayer struct {
	UserID      uint              `json:"user_id"`
	CharacterID uint              `json:"character_id"`
	PlayerState map[string]string `json:"player_state"`
	Items       []RuntimeItem     `json:"items"`
	Buffs       []RuntimeBuff     `json:"buffs"`
}

// MultiplayerRuntimeState is the complete input for provisional V2 initialization.
type MultiplayerRuntimeState struct {
	RoomID        uint
	Generation    string
	TurnOrder     []uint
	Players       []MultiplayerRuntimePlayer
	Opening       RuntimeMessage
	SummaryMemory string
	TurnTimeout   time.Duration
}

// MultiplayerRuntimeSnapshot is the validated, client-safe V2 state.
type MultiplayerRuntimeSnapshot struct {
	Version        int                        `json:"version"`
	RoomID         uint                       `json:"room_id"`
	Status         RoomStatus                 `json:"status"`
	Generation     string                     `json:"generation"`
	CurrentTurn    int                        `json:"current_turn"`
	RoundNumber    int                        `json:"round_number"`
	TurnOrder      []uint                     `json:"turn_order"`
	CurrentActorID uint                       `json:"current_actor_id"`
	DeadlineAt     *time.Time                 `json:"deadline_at"`
	Players        []MultiplayerRuntimePlayer `json:"players"`
	SummaryMemory  string                     `json:"summary_memory"`
	RecentMessages []RuntimeMessage           `json:"recent_messages"`
	ActionLease    *MultiplayerActionLease    `json:"-"`
}

// MultiplayerActionLease is internal recovery metadata for an in-flight turn.
type MultiplayerActionLease struct {
	Generation  string
	Turn        int
	UserID      uint
	RequestID   string
	Fingerprint string
	ClaimedAt   time.Time
}

// MultiplayerPlayerMutation groups authoritative effects for one frozen player.
type MultiplayerPlayerMutation struct {
	UserID             uint
	PlayerStateChanges map[string]string
	ItemMutations      []RuntimeItemMutation
	BuffMutations      []RuntimeBuffMutation
}

// MultiplayerActionMutation is committed only while the matching action lease is current.
type MultiplayerActionMutation struct {
	RoomID             uint
	UserID             uint
	Generation         string
	ExpectedTurn       int
	RequestID          string
	RequestFingerprint string
	PlayerMutations    []MultiplayerPlayerMutation
	Messages           []RuntimeMessage
	ResponseJSON       json.RawMessage
	NextDeadline       time.Time
}

type MultiplayerActionAcquireResult struct {
	Generation   string
	Duplicate    bool
	ResponseJSON json.RawMessage
}

type MultiplayerActionCommitResult struct {
	Duplicate    bool
	CurrentTurn  int
	ResponseJSON json.RawMessage
	Snapshot     *MultiplayerRuntimeSnapshot
}

type MultiplayerSkipRequest struct {
	RoomID             uint
	UserID             uint
	Generation         string
	ExpectedTurn       int
	RequestID          string
	RequestFingerprint string
	ResponseJSON       json.RawMessage
	Reason             string
	Now                time.Time
	NextDeadline       time.Time
	Timeout            bool
}

type MultiplayerSkipResult struct {
	Duplicate      bool      `json:"-"`
	Generation     string    `json:"generation"`
	SkippedUserID  uint      `json:"skipped_user_id"`
	CurrentTurn    int       `json:"current_turn"`
	RoundNumber    int       `json:"round_number"`
	CurrentActorID uint      `json:"current_actor_id"`
	DeadlineAt     time.Time `json:"deadline_at"`
	Reason         string    `json:"reason"`
}

type MultiplayerDeadlineTask struct {
	RoomID     uint
	Generation string
	Turn       int
	Member     string
}

type PendingMultiplayerAutoSave struct {
	Generation string
	Snapshot   *MultiplayerRuntimeSnapshot
}

// ActionCommitResult 是 Redis 行动提交或幂等重放的结果。
type ActionCommitResult struct {
	Duplicate        bool
	CurrentTurn      int
	ResponseJSON     json.RawMessage
	AutoSaveSnapshot *SoloRuntimeSnapshot
}

// PendingAutoSave 是 Redis 中等待持久化到 MySQL 的自动存档快照。
type PendingAutoSave struct {
	Generation string
	Snapshot   *SoloRuntimeSnapshot
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
