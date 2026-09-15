package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"trpggame/internal/imws"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

// pushTimeout 独立于连接处理超时：消息已提交后推送只受尽力投递约束，
// 推送失败不回滚也不向发送者报告错误，客户端靠 REST/im_sync 补齐。
const chatPushTimeout = 3 * time.Second

// ChatRealtime 将 IM 业务帧接入 ChatService，并在事务提交后尽力推送实时事件。
type ChatRealtime struct {
	chat      *ChatService
	publisher UserEventPublisher
}

func NewChatRealtime(chat *ChatService, publisher UserEventPublisher) *ChatRealtime {
	return &ChatRealtime{chat: chat, publisher: publisher}
}

// HandleIM 实现 imws.InboundHandler。返回的消息只回给当前连接；
// 对端与双方视角的 conversation_updated 在此方法内于持久化之后推送。
func (r *ChatRealtime) HandleIM(ctx context.Context, userID uint, message imws.ClientMessage) (*imws.ServerMessage, error) {
	switch message.Type {
	case imws.MsgChatMessage:
		return r.handleChatMessage(ctx, userID, message)
	case imws.MsgImSync:
		return r.handleImSync(ctx, userID, message)
	default:
		return nil, &imws.BusinessError{
			Code: imws.ErrorCodeUnsupportedMessageType, Message: "unsupported message type",
		}
	}
}

type chatMessagePayload struct {
	ConversationID uint   `json:"conversation_id"`
	MessageType    string `json:"message_type"`
	Content        string `json:"content"`
}

type imSyncPayload struct {
	ConversationID uint   `json:"conversation_id"`
	SinceSeq       uint64 `json:"since_seq"`
	Limit          int    `json:"limit"`
}

type ChatAckData struct {
	Message   *MessageItem `json:"message"`
	Duplicate bool         `json:"duplicate"`
}

type ChatMessageEventData struct {
	Message *MessageItem `json:"message"`
}

type ConversationUpdatedEventData struct {
	Conversation ConversationSummary `json:"conversation"`
}

type ImSyncBatchData struct {
	ConversationID uint          `json:"conversation_id"`
	Messages       []MessageItem `json:"messages"`
	NextSeq        uint64        `json:"next_seq"`
	HasMore        bool          `json:"has_more"`
}

func (r *ChatRealtime) handleChatMessage(ctx context.Context, userID uint, message imws.ClientMessage) (*imws.ServerMessage, error) {
	var payload chatMessagePayload
	if err := decodeChatPayload(message.Data, &payload); err != nil {
		return nil, chatBusinessError(ErrInvalidChatMessage)
	}
	// M2.2 首版客户端只能发送 text；message_type 在信封数据内声明，必须与服务端持久化类型一致。
	if payload.MessageType != string(model.MessageTypeText) {
		return nil, chatBusinessError(ErrInvalidMessageContent)
	}
	item, duplicate, err := r.chat.SendText(ctx, userID, payload.ConversationID, message.RequestID, payload.Content)
	if err != nil {
		return nil, chatBusinessError(err)
	}
	if !duplicate {
		r.publishDelivery(userID, payload.ConversationID, item)
	}
	return serverMessage(imws.MsgChatAck, message.RequestID, ChatAckData{Message: item, Duplicate: duplicate})
}

func (r *ChatRealtime) handleImSync(ctx context.Context, userID uint, message imws.ClientMessage) (*imws.ServerMessage, error) {
	var payload imSyncPayload
	if err := decodeChatPayload(message.Data, &payload); err != nil {
		return nil, chatBusinessError(ErrInvalidSyncRequest)
	}
	if payload.ConversationID == 0 || payload.Limit < 0 || payload.Limit > 100 {
		return nil, chatBusinessError(ErrInvalidSyncRequest)
	}
	limit := payload.Limit
	if limit == 0 {
		limit = 100
	}
	items, hasMore, err := r.chat.ListMessagesSince(ctx, userID, payload.ConversationID, payload.SinceSeq, limit)
	if err != nil {
		if errors.Is(err, ErrInvalidMessageQuery) {
			return nil, chatBusinessError(ErrInvalidSyncRequest)
		}
		return nil, chatBusinessError(err)
	}
	nextSeq := payload.SinceSeq
	if len(items) > 0 {
		nextSeq = items[len(items)-1].Seq
	}
	return serverMessage(imws.MsgImSyncBatch, message.RequestID, ImSyncBatchData{
		ConversationID: payload.ConversationID,
		Messages:       items,
		NextSeq:        nextSeq,
		HasMore:        hasMore,
	})
}

// serverMessage 把业务数据编码进服务端信封；编码失败不影响入站处理，
// 调用方将其视为会话或消息内部错误上报。
func serverMessage(messageType imws.MessageType, requestID string, data any) (*imws.ServerMessage, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, &imws.BusinessError{Code: imws.ErrorCodeChatUnavailable, Message: "chat unavailable"}
	}
	return &imws.ServerMessage{
		Type:      messageType,
		RequestID: requestID,
		Data:      encoded,
	}, nil
}

// publishDelivery 在消息提交后推送对端 chat_message 和双方视角的 conversation_updated。
// 推送使用独立超时，避免慢请求挤占投递；失败仅记录不含正文的结构化错误。
func (r *ChatRealtime) publishDelivery(senderID, conversationID uint, item *MessageItem) {
	ctx, cancel := context.WithTimeout(context.Background(), chatPushTimeout)
	defer cancel()

	record, err := r.chat.chats.GetConversation(ctx, senderID, conversationID)
	if err != nil {
		log.Printf("[CHAT] load conversation %d for realtime delivery by user %d: %v", conversationID, senderID, err)
		return
	}
	peerID := record.Conversation.DirectPeerID(senderID)
	if peerID == 0 {
		return
	}
	r.publisher.PublishUserEvent(peerID, string(imws.MsgChatMessage), ChatMessageEventData{Message: item})
	r.pushConversationUpdated(ctx, conversationID, senderID, peerID)
}

func (r *ChatRealtime) pushConversationUpdated(ctx context.Context, conversationID uint, recipients ...uint) {
	for _, recipient := range recipients {
		record, err := r.chat.chats.GetConversation(ctx, recipient, conversationID)
		if err != nil {
			log.Printf("[CHAT] load conversation %d view for user %d realtime update: %v", conversationID, recipient, err)
			continue
		}
		senders, err := r.chat.loadMessageSenders(ctx, conversationLastMessages([]repo.ConversationRecord{*record}))
		if err != nil {
			log.Printf("[CHAT] load senders for conversation %d realtime update: %v", conversationID, err)
			continue
		}
		r.publisher.PublishUserEvent(
			recipient,
			string(imws.MsgConversationUpdated),
			ConversationUpdatedEventData{Conversation: conversationSummary(*record, senders)},
		)
	}
}

// chatBusinessError 将服务层错误映射为 IM 错误帧的稳定分类。
func chatBusinessError(err error) error {
	switch {
	case errors.Is(err, ErrConversationNotFound):
		return &imws.BusinessError{Code: imws.ErrorCodeConversationNotFound, Message: "conversation not found"}
	case errors.Is(err, ErrFriendshipRequired):
		return &imws.BusinessError{Code: imws.ErrorCodeFriendshipRequired, Message: "friendship required"}
	case errors.Is(err, ErrInvalidChatMessage):
		return &imws.BusinessError{Code: imws.ErrorCodeInvalidChatMessage, Message: "invalid chat message"}
	case errors.Is(err, ErrInvalidMessageContent):
		return &imws.BusinessError{Code: imws.ErrorCodeInvalidMessageContent, Message: "invalid message content"}
	case errors.Is(err, ErrInvalidSyncRequest):
		return &imws.BusinessError{Code: imws.ErrorCodeInvalidSyncRequest, Message: "invalid sync request"}
	default:
		return &imws.BusinessError{Code: imws.ErrorCodeChatUnavailable, Message: "chat unavailable"}
	}
}

func decodeChatPayload(data json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("chat payload data is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("chat payload has multiple JSON values")
		}
		return err
	}
	return nil
}
