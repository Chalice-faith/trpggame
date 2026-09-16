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

type GroupEventSummary struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	AvatarURL   string `json:"avatar_url"`
	OwnerID     uint   `json:"owner_id"`
	MemberCount int    `json:"member_count"`
	Version     uint64 `json:"version"`
}

type GroupUpdatedEventData struct {
	Group GroupEventSummary `json:"group"`
}

type GroupMemberChangedEventData struct {
	GroupID      uint   `json:"group_id"`
	Event        string `json:"event"`
	ActorUserID  uint   `json:"actor_user_id"`
	TargetUserID uint   `json:"target_user_id"`
	Version      uint64 `json:"version"`
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

type groupConversationViewRepository interface {
	ListActiveConversationViews(context.Context, uint) ([]repo.ConversationView, error)
}

// publishDelivery 在消息提交后按会话类型推送 chat_message 和各成员视角的 conversation_updated。
// 推送使用独立超时，避免慢请求挤占投递；失败仅记录不含正文的结构化错误。
func (r *ChatRealtime) publishDelivery(senderID, conversationID uint, item *MessageItem) {
	ctx, cancel := context.WithTimeout(context.Background(), chatPushTimeout)
	defer cancel()

	record, err := r.chat.chats.GetConversation(ctx, senderID, conversationID)
	if err != nil {
		log.Printf("[CHAT] load conversation %d for realtime delivery by user %d: %v", conversationID, senderID, err)
		return
	}
	switch record.Conversation.Type {
	case model.ConversationTypeDirect:
		peerID := record.Conversation.DirectPeerID(senderID)
		if peerID == 0 {
			return
		}
		r.publishUserEvent(peerID, imws.MsgChatMessage, ChatMessageEventData{Message: item})
		r.pushConversationUpdated(ctx, conversationID, senderID, peerID)
	case model.ConversationTypeGroup:
		r.publishGroupMessageDelivery(ctx, senderID, conversationID, item)
	}
}

func (r *ChatRealtime) publishGroupMessageDelivery(ctx context.Context, senderID, conversationID uint, item *MessageItem) {
	views, err := r.groupConversationViews(ctx, conversationID)
	if err != nil {
		log.Printf("[CHAT] load group conversation %d views for realtime delivery: %v", conversationID, err)
		return
	}
	for _, view := range views {
		if view.UserID != senderID {
			r.publishUserEvent(view.UserID, imws.MsgChatMessage, ChatMessageEventData{Message: item})
		}
	}
	r.pushConversationViews(ctx, views)
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
		r.publishUserEvent(
			recipient,
			imws.MsgConversationUpdated,
			ConversationUpdatedEventData{Conversation: conversationSummary(*record, senders)},
		)
	}
}

func (r *ChatRealtime) groupConversationViews(ctx context.Context, conversationID uint) ([]repo.ConversationView, error) {
	repository, ok := r.chat.chats.(groupConversationViewRepository)
	if !ok {
		return nil, fmt.Errorf("chat repository does not support group conversation views")
	}
	return repository.ListActiveConversationViews(ctx, conversationID)
}

func (r *ChatRealtime) pushConversationViews(ctx context.Context, views []repo.ConversationView) {
	records := make([]repo.ConversationRecord, 0, len(views))
	for _, view := range views {
		records = append(records, view.Record)
	}
	senders, err := r.chat.loadMessageSenders(ctx, conversationLastMessages(records))
	if err != nil {
		log.Printf("[CHAT] load group conversation senders for realtime update: %v", err)
		return
	}
	for _, view := range views {
		r.publishUserEvent(
			view.UserID,
			imws.MsgConversationUpdated,
			ConversationUpdatedEventData{Conversation: conversationSummary(view.Record, senders)},
		)
	}
}

type groupSystemMetadata struct {
	Event        string `json:"event"`
	ActorUserID  uint   `json:"actor_user_id"`
	TargetUserID uint   `json:"target_user_id"`
	GroupID      uint   `json:"group_id"`
	GroupVersion uint64 `json:"group_version"`
}

// PublishGroupMutation 实现 GroupMutationPublisher。仓储事务已经提交后，
// 才向变更后的有效成员推送群事件、系统消息和各自视角的会话摘要。
func (r *ChatRealtime) PublishGroupMutation(result *repo.GroupMutationResult) {
	if r == nil || result == nil || !result.Changed || r.publisher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatPushTimeout)
	defer cancel()

	views, err := r.groupConversationViews(ctx, result.Record.ConversationID)
	if err != nil {
		log.Printf("[GROUP] load conversation %d views for realtime mutation: %v", result.Record.ConversationID, err)
		return
	}
	active := make(map[uint]struct{}, len(views))
	for _, view := range views {
		active[view.UserID] = struct{}{}
	}

	metadata := make([]groupSystemMetadata, 0, len(result.Messages))
	groupUpdated := len(result.Messages) == 0
	for _, message := range result.Messages {
		var current groupSystemMetadata
		if err := json.Unmarshal(message.Metadata, &current); err != nil {
			log.Printf("[GROUP] decode system message %d metadata for realtime mutation: %v", message.ID, err)
			continue
		}
		metadata = append(metadata, current)
		switch current.Event {
		case "group_created", "group_name_changed", "group_avatar_changed":
			groupUpdated = true
		}
	}

	if groupUpdated {
		data := GroupUpdatedEventData{Group: groupEventSummary(result.Record)}
		for _, view := range views {
			r.publishUserEvent(view.UserID, imws.MsgGroupUpdated, data)
		}
	}
	for _, current := range metadata {
		if !groupMemberEvent(current.Event) {
			continue
		}
		data := GroupMemberChangedEventData{
			GroupID: current.GroupID, Event: current.Event, ActorUserID: current.ActorUserID,
			TargetUserID: current.TargetUserID, Version: current.GroupVersion,
		}
		for _, view := range views {
			r.publishUserEvent(view.UserID, imws.MsgGroupMemberChanged, data)
		}
		if (current.Event == "member_removed" || current.Event == "member_left") && current.TargetUserID > 0 {
			if _, stillActive := active[current.TargetUserID]; !stillActive {
				r.publishUserEvent(current.TargetUserID, imws.MsgGroupMemberChanged, data)
			}
		}
	}

	senders, err := r.chat.loadMessageSenders(ctx, result.Messages)
	if err != nil {
		log.Printf("[GROUP] load system message senders for realtime mutation: %v", err)
		return
	}
	for _, message := range result.Messages {
		item := messageItem(message, senders)
		for _, view := range views {
			r.publishUserEvent(view.UserID, imws.MsgChatMessage, ChatMessageEventData{Message: &item})
		}
	}
	r.pushConversationViews(ctx, views)
}

func groupEventSummary(record repo.GroupRecord) GroupEventSummary {
	return GroupEventSummary{
		ID: record.Group.ID, Name: record.Group.Name, AvatarURL: record.Group.AvatarURL,
		OwnerID: record.Group.OwnerID, MemberCount: record.MemberCount, Version: record.Group.Version,
	}
}

func groupMemberEvent(event string) bool {
	switch event {
	case "member_joined", "member_role_changed", "member_removed", "member_left", "owner_transferred":
		return true
	default:
		return false
	}
}

func (r *ChatRealtime) publishUserEvent(userID uint, eventType imws.MessageType, data any) {
	if r.publisher == nil || !r.publisher.PublishUserEvent(userID, string(eventType), data) {
		log.Printf("[CHAT] realtime delivery unavailable type=%s user_id=%d", eventType, userID)
	}
}

// chatBusinessError 将服务层错误映射为 IM 错误帧的稳定分类。
func chatBusinessError(err error) error {
	descriptor := DescribeChatError(err)
	return &imws.BusinessError{Code: descriptor.Code, Message: descriptor.Message}
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
