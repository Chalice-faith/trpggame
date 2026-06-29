package ws

import "encoding/json"

// MessageType WebSocket 消息类型
type MessageType string

// 客户端 → 服务端
const (
	MsgPing        MessageType = "ping"
	MsgGameAction  MessageType = "game_action"
	MsgSync        MessageType = "sync"
	MsgChatMessage MessageType = "chat_message"
)

// 服务端 → 客户端
const (
	MsgPong             MessageType = "pong"
	MsgNarrativeChunk   MessageType = "narrative_chunk"
	MsgNarrativeComplete MessageType = "narrative_complete"
	MsgDiceRoll         MessageType = "dice_roll"
	MsgStatusUpdate     MessageType = "status_update"
	MsgScriptProgress   MessageType = "script_progress"
	MsgSystem           MessageType = "system"
	MsgError            MessageType = "error"
	MsgSyncBatch        MessageType = "sync_batch"
	MsgPresence         MessageType = "presence"
	MsgTurnStart        MessageType = "turn_start"
	MsgTurnSkip         MessageType = "turn_skip"
)

// Message WebSocket 消息结构
type Message struct {
	Type      MessageType     `json:"type"`
	RoomID    uint            `json:"room_id,omitempty"`
	UserID    uint            `json:"user_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp int64           `json:"timestamp"`
	Seq       int64           `json:"seq,omitempty"` // 消息序号，重连补推用
}

// NarrativeChunkData AI 叙事流式片段
type NarrativeChunkData struct {
	Content string `json:"content"`
	IsFinal bool   `json:"is_final"`
}

// DiceRollData 骰子检定结果
type DiceRollData struct {
	Type         string `json:"type"`          // D20 / D100
	Result       int    `json:"result"`
	Target       int    `json:"target"`
	Success      bool   `json:"success"`
	CriticalHit  bool   `json:"critical_hit"`
	CriticalMiss bool   `json:"critical_miss"`
	Description  string `json:"description"`
}

// StatusUpdateData 角色状态变更
type StatusUpdateData struct {
	PlayerID uint            `json:"player_id"`
	Changes  map[string]any  `json:"changes"`
}

// ScriptProgressData 剧本解析进度
type ScriptProgressData struct {
	ScriptID uint   `json:"script_id"`
	Stage    string `json:"stage"`    // parsing / embedding / done
	Progress int    `json:"progress"` // 0-100
	Error    string `json:"error,omitempty"`
}
