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
	MsgPong              MessageType = "pong"
	MsgSubscribed        MessageType = "subscribed"
	MsgNarrativeChunk    MessageType = "narrative_chunk"
	MsgNarrativeComplete MessageType = "narrative_complete"
	MsgDiceRoll          MessageType = "dice_roll"
	MsgStatusUpdate      MessageType = "status_update"
	MsgScriptProgress    MessageType = "script_progress"
	MsgSystem            MessageType = "system"
	MsgError             MessageType = "error"
	MsgSyncBatch         MessageType = "sync_batch"
	MsgPresence          MessageType = "presence"
	MsgTurnStart         MessageType = "turn_start"
	MsgTurnSkip          MessageType = "turn_skip"
)

// Message WebSocket 消息结构。
//
// 服务端 → 客户端：Seq 必填（按房间单调递增），RoomID 必填，UserID 表示目标玩家；
// RequestID 用于把同一动作的多个事件关联起来（例如行动流式推送）。
// 客户端 → 服务端：RoomID/UserID/Seq 一律不填，由服务端从 JWT 与订阅关系推导。
type Message struct {
	Type      MessageType     `json:"type"`
	RoomID    uint            `json:"room_id,omitempty"`
	UserID    uint            `json:"user_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp int64           `json:"timestamp"`
	Seq       int64           `json:"seq,omitempty"`        // 服务端按房间分配的序号，重连补推用
	RequestID string          `json:"request_id,omitempty"` // 请求关联 ID
}

// SubscribedData 订阅房间成功确认。Seq 为房间当前水位，客户端可据此判断是否需 sync。
type SubscribedData struct {
	RoomID uint  `json:"room_id"`
	Seq    int64 `json:"seq"`
}

// SyncRequestData 客户端请求重连补推：返回 Seq > SinceSeq 的近期服务端消息。
type SyncRequestData struct {
	SinceSeq int64 `json:"since_seq"`
}

// GameActionData 是客户端通过 WS 提交行动时携带的业务数据。
// UserID/RoomID 不从客户端读取，由订阅后的连接身份推导。
type GameActionData struct {
	RequestID    string `json:"request_id"`
	ExpectedTurn *int   `json:"expected_turn"`
	ActionText   string `json:"action_text"`
}

// SyncBatchData 重连补推批次。NextSeq 为房间当前水位。
type SyncBatchData struct {
	Messages []Message `json:"messages"`
	NextSeq  int64     `json:"next_seq"`
}

// ErrorData 服务端错误事件。
type ErrorData struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// NarrativeChunkData AI 叙事流式片段
type NarrativeChunkData struct {
	Content string `json:"content"`
	IsFinal bool   `json:"is_final"`
}

// NarrativeCompleteData 是一次行动提交成功后的最终叙事与回合水位。
type NarrativeCompleteData struct {
	Narrative   string `json:"narrative"`
	CurrentTurn int    `json:"current_turn"`
	Duplicate   bool   `json:"duplicate,omitempty"`
}

// DiceRollData 骰子检定结果
type DiceRollData struct {
	Type         string `json:"type"` // D20 / D100
	Result       int    `json:"result"`
	Target       int    `json:"target"`
	Success      bool   `json:"success"`
	CriticalHit  bool   `json:"critical_hit"`
	CriticalMiss bool   `json:"critical_miss"`
	Description  string `json:"description"`
	Reason       string `json:"reason,omitempty"`
}

// StatusUpdateData 角色状态变更
type StatusUpdateData struct {
	PlayerID uint           `json:"player_id"`
	Changes  map[string]any `json:"changes"`
}

// ScriptProgressData 剧本解析进度
type ScriptProgressData struct {
	ScriptID uint   `json:"script_id"`
	Stage    string `json:"stage"`    // parsing / embedding / done
	Progress int    `json:"progress"` // 0-100
	Error    string `json:"error,omitempty"`
}
