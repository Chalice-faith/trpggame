package imws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"trpggame/internal/realtime"
)

// MaxTextMessageSize 允许 4000 个 UTF-8 字符正文加 JSON 信封仍有余量，见 M2.2 实施方案 3.8。
const MaxTextMessageSize = 32768

const (
	ErrorCodeMissingToken           = 1700
	ErrorCodeInvalidToken           = 1701
	ErrorCodeOriginNotAllowed       = 1702
	ErrorCodeMalformedMessage       = 1703
	ErrorCodeInvalidMessage         = 1704
	ErrorCodeUnsupportedMessageType = 1705
	ErrorCodeConversationNotFound   = 1709
	ErrorCodeFriendshipRequired     = 1710
	ErrorCodeInvalidChatMessage     = 1711
	ErrorCodeInvalidMessageContent  = 1712
	ErrorCodeInvalidSyncRequest     = 1715
	ErrorCodeChatUnavailable        = 1716
	ErrorCodeRateLimited            = 1717
)

const (
	// 保留 IM 包内别名，兼容已有调用点；权威值由 realtime 包统一定义。
	CloseCodeConnectionReplaced   = realtime.CloseCodeConnectionReplaced
	CloseReasonConnectionReplaced = realtime.CloseReasonConnectionReplaced
	CloseReasonServiceUnavailable = realtime.CloseReasonServiceUnavailable
)

var (
	ErrMalformedClientMessage = errors.New("malformed IM client message")
	ErrInvalidClientMessage   = errors.New("invalid IM client message")
	messageTypePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// BusinessError 是业务处理器返回给连接层的稳定错误分类。
// 连接层只回显 Code/Message，不携带正文或内部错误细节。
type BusinessError struct {
	Code    int
	Message string
}

func (e *BusinessError) Error() string {
	return fmt.Sprintf("IM business error %d: %s", e.Code, e.Message)
}

// MessageType IM WebSocket 消息类型。
type MessageType string

const (
	MsgPing                MessageType = "ping"
	MsgPong                MessageType = "pong"
	MsgConnected           MessageType = "connected"
	MsgError               MessageType = "error"
	MsgConnectionReplaced  MessageType = "connection_replaced"
	MsgPresence            MessageType = "presence"
	MsgFriendshipUpdated   MessageType = "friendship_updated"
	MsgChatMessage         MessageType = "chat_message"
	MsgChatAck             MessageType = "chat_ack"
	MsgImSync              MessageType = "im_sync"
	MsgImSyncBatch         MessageType = "im_sync_batch"
	MsgConversationUpdated MessageType = "conversation_updated"
	MsgGroupUpdated        MessageType = "group_updated"
	MsgGroupMemberChanged  MessageType = "group_member_changed"
)

// ClientMessage 是客户端发送到 IM 通道的严格信封。
type ClientMessage struct {
	Type      MessageType     `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// ServerMessage 是 IM 通道的服务端信封。持久化消息 seq 将由具体消息数据携带。
type ServerMessage struct {
	Type      MessageType     `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type ConnectedData struct {
	UserID       uint   `json:"user_id"`
	ConnectionID string `json:"connection_id"`
}

type ErrorData struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type ConnectionReplacedData struct {
	Reason string `json:"reason"`
}

// MarshalServerMessage 编码一个由服务端生成的 IM 消息信封。
func MarshalServerMessage(
	messageType MessageType,
	requestID string,
	timestamp int64,
	data any,
) ([]byte, error) {
	var rawData json.RawMessage
	switch value := data.(type) {
	case nil:
	case json.RawMessage:
		if len(bytes.TrimSpace(value)) > 0 {
			rawData = append(json.RawMessage(nil), value...)
		}
	default:
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("marshal IM message data: %w", err)
		}
		rawData = encoded
	}
	return json.Marshal(ServerMessage{
		Type:      messageType,
		RequestID: requestID,
		Timestamp: timestamp,
		Data:      rawData,
	})
}

// DecodeClientMessage 严格解码并校验一个客户端信封。
func DecodeClientMessage(raw []byte) (*ClientMessage, error) {
	if len(raw) == 0 || len(raw) > MaxTextMessageSize {
		return nil, fmt.Errorf("%w: message size is invalid", ErrInvalidClientMessage)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var message ClientMessage
	if err := decoder.Decode(&message); err != nil {
		return nil, fmt.Errorf("%w: decode envelope", ErrMalformedClientMessage)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: multiple JSON values", ErrMalformedClientMessage)
	}
	if !messageTypePattern.MatchString(string(message.Type)) {
		return nil, fmt.Errorf("%w: invalid message type", ErrInvalidClientMessage)
	}
	if message.RequestID != "" {
		parsed, err := uuid.Parse(message.RequestID)
		if err != nil || parsed.String() != message.RequestID {
			return nil, fmt.Errorf("%w: invalid request ID", ErrInvalidClientMessage)
		}
	}
	if err := validateObjectData(message.Data); err != nil {
		return nil, fmt.Errorf("%w: invalid data", ErrInvalidClientMessage)
	}
	return &message, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected additional JSON value")
	}
	return err
}

func validateObjectData(data json.RawMessage) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] != '{' {
		return errors.New("data must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return err
	}
	return nil
}

// ValidMessageType 判断消息类型是否满足信封命名约束，不判断业务处理器是否支持。
func ValidMessageType(value MessageType) bool {
	return messageTypePattern.MatchString(strings.TrimSpace(string(value))) &&
		string(value) == strings.TrimSpace(string(value))
}
