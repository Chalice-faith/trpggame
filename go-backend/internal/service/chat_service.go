package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

var (
	ErrInvalidConversationRequest = errors.New("invalid conversation request")
	ErrConversationNotFound       = errors.New("conversation not found")
	ErrFriendshipRequired         = errors.New("friendship required")
	ErrInvalidChatMessage         = errors.New("invalid chat message")
	ErrInvalidMessageContent      = errors.New("invalid message content")
	ErrInvalidMessageQuery        = errors.New("invalid message query")
	ErrReadSequenceConflict       = errors.New("read sequence conflict")
)

type ChatRepository interface {
	EnsureDirectConversation(context.Context, uint, uint) (*model.Conversation, error)
	GetConversation(context.Context, uint, uint) (*repo.ConversationRecord, error)
	ListConversations(context.Context, uint, time.Time, uint, int) ([]repo.ConversationRecord, error)
	ListMessages(context.Context, uint, uint, uint64, int) ([]model.Message, error)
	MarkRead(context.Context, uint, uint, uint64) (uint64, uint64, error)
	SendMessage(context.Context, uint, uint, string, string, time.Time) (*repo.SendMessageResult, error)
}

type ChatUserRepository interface {
	FindActiveByID(context.Context, uint) (*model.User, error)
	FindActiveByIDs(context.Context, []uint) ([]model.User, error)
}

type MessageItem struct {
	ID              uint              `json:"id"`
	ConversationID  uint              `json:"conversation_id"`
	Seq             uint64            `json:"seq"`
	Sender          *PublicUser       `json:"sender"`
	ClientMessageID string            `json:"client_message_id"`
	MessageType     model.MessageType `json:"message_type"`
	Content         string            `json:"content"`
	Metadata        any               `json:"metadata"`
	CreatedAt       time.Time         `json:"created_at"`
}

type ConversationSummary struct {
	ID          uint         `json:"id"`
	Type        string       `json:"type"`
	Peer        PublicUser   `json:"peer"`
	CanSend     bool         `json:"can_send"`
	LastSeq     uint64       `json:"last_seq"`
	LastReadSeq uint64       `json:"last_read_seq"`
	UnreadCount uint64       `json:"unread_count"`
	LastMessage *MessageItem `json:"last_message"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	activityAt  time.Time
}

type ConversationPage struct {
	Items      []ConversationSummary `json:"items"`
	NextCursor string                `json:"next_cursor"`
}

type MessagePage struct {
	Items         []MessageItem `json:"items"`
	NextBeforeSeq uint64        `json:"next_before_seq"`
	HasMore       bool          `json:"has_more"`
}

type ReadResult struct {
	ConversationID uint   `json:"conversation_id"`
	LastReadSeq    uint64 `json:"last_read_seq"`
	UnreadCount    uint64 `json:"unread_count"`
}

type ChatService struct {
	chats ChatRepository
	users ChatUserRepository
	now   func() time.Time
}

func NewChatService(chats ChatRepository, users ChatUserRepository) *ChatService {
	return &ChatService{chats: chats, users: users, now: time.Now}
}

func (s *ChatService) CreateDirect(ctx context.Context, currentUserID, peerUserID uint) (*ConversationSummary, error) {
	if currentUserID == 0 || peerUserID == 0 || currentUserID == peerUserID {
		return nil, ErrInvalidConversationRequest
	}
	if _, err := s.users.FindActiveByID(ctx, peerUserID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFriendshipRequired
		}
		return nil, fmt.Errorf("find conversation peer: %w", err)
	}
	low, high := normalizedPair(currentUserID, peerUserID)
	conversation, err := s.chats.EnsureDirectConversation(ctx, low, high)
	if err != nil {
		return nil, mapChatRepositoryError("ensure direct conversation", err)
	}
	record, err := s.chats.GetConversation(ctx, currentUserID, conversation.ID)
	if err != nil {
		return nil, mapChatRepositoryError("load direct conversation", err)
	}
	senders, err := s.loadMessageSenders(ctx, conversationLastMessages([]repo.ConversationRecord{*record}))
	if err != nil {
		return nil, err
	}
	summary := conversationSummary(*record, senders)
	return &summary, nil
}

func (s *ChatService) ListConversations(ctx context.Context, currentUserID uint, cursor string, limit int) (*ConversationPage, error) {
	if currentUserID == 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalidConversationRequest
	}
	before, beforeID, err := decodeConversationCursor(cursor)
	if err != nil {
		return nil, ErrInvalidConversationRequest
	}
	rows, err := s.chats.ListConversations(ctx, currentUserID, before, beforeID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	next := ""
	if len(rows) > limit {
		last := rows[limit-1]
		next = encodeConversationCursor(last.ActivityAt, last.Conversation.ID)
		rows = rows[:limit]
	}
	senders, err := s.loadMessageSenders(ctx, conversationLastMessages(rows))
	if err != nil {
		return nil, err
	}
	items := make([]ConversationSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, conversationSummary(row, senders))
	}
	return &ConversationPage{Items: items, NextCursor: next}, nil
}

func (s *ChatService) ListMessages(ctx context.Context, currentUserID, conversationID uint, beforeSeq uint64, limit int) (*MessagePage, error) {
	if currentUserID == 0 || conversationID == 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalidMessageQuery
	}
	rows, err := s.chats.ListMessages(ctx, currentUserID, conversationID, beforeSeq, limit+1)
	if err != nil {
		return nil, mapChatRepositoryError("list messages", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	senders, err := s.loadMessageSenders(ctx, rows)
	if err != nil {
		return nil, err
	}
	items := make([]MessageItem, len(rows))
	for index := range rows {
		items[len(rows)-1-index] = messageItem(rows[index], senders)
	}
	nextBeforeSeq := uint64(0)
	if hasMore && len(items) > 0 {
		nextBeforeSeq = items[0].Seq
	}
	return &MessagePage{Items: items, NextBeforeSeq: nextBeforeSeq, HasMore: hasMore}, nil
}

func (s *ChatService) MarkRead(ctx context.Context, currentUserID, conversationID uint, requested uint64) (*ReadResult, error) {
	if currentUserID == 0 || conversationID == 0 {
		return nil, ErrInvalidConversationRequest
	}
	current, lastSeq, err := s.chats.MarkRead(ctx, currentUserID, conversationID, requested)
	if err != nil {
		return nil, mapChatRepositoryError("mark conversation read", err)
	}
	return &ReadResult{
		ConversationID: conversationID, LastReadSeq: current, UnreadCount: unreadCount(lastSeq, current),
	}, nil
}

// SendText 持久化消息并返回权威结果。M2.2-B 只提供给测试和下一块的 IM 入站层，不注册 REST 发送端点。
func (s *ChatService) SendText(ctx context.Context, currentUserID, conversationID uint, clientMessageID, content string) (*MessageItem, bool, error) {
	if currentUserID == 0 || conversationID == 0 || !canonicalUUID(clientMessageID) {
		return nil, false, ErrInvalidChatMessage
	}
	if !validMessageContent(content) {
		return nil, false, ErrInvalidMessageContent
	}
	result, err := s.chats.SendMessage(ctx, currentUserID, conversationID, clientMessageID, content, s.now().UTC())
	if err != nil {
		return nil, false, mapChatRepositoryError("send chat message", err)
	}
	senders, err := s.loadMessageSenders(ctx, []model.Message{result.Message})
	if err != nil {
		return nil, false, err
	}
	item := messageItem(result.Message, senders)
	return &item, result.Duplicate, nil
}

func mapChatRepositoryError(operation string, err error) error {
	switch {
	case errors.Is(err, repo.ErrChatFriendshipRequired):
		return ErrFriendshipRequired
	case errors.Is(err, repo.ErrChatConversationMissing), errors.Is(err, gorm.ErrRecordNotFound):
		return ErrConversationNotFound
	case errors.Is(err, repo.ErrChatReadConflict):
		return ErrReadSequenceConflict
	case errors.Is(err, repo.ErrChatIdempotencyConflict):
		return ErrInvalidChatMessage
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func conversationSummary(record repo.ConversationRecord, senders map[uint]model.User) ConversationSummary {
	lastRead := record.LastReadSeq
	if lastRead > record.Conversation.LastSeq {
		lastRead = record.Conversation.LastSeq
	}
	result := ConversationSummary{
		ID: record.Conversation.ID, Type: string(record.Conversation.Type), Peer: publicUser(record.Peer),
		CanSend: record.FriendshipStatus == model.FriendshipStatusAccepted,
		LastSeq: record.Conversation.LastSeq, LastReadSeq: lastRead,
		UnreadCount: unreadCount(record.Conversation.LastSeq, lastRead),
		CreatedAt:   record.Conversation.CreatedAt, UpdatedAt: record.Conversation.UpdatedAt,
		activityAt: record.ActivityAt,
	}
	if record.LastMessage != nil {
		item := messageItem(*record.LastMessage, senders)
		result.LastMessage = &item
	}
	return result
}

func messageItem(message model.Message, users map[uint]model.User) MessageItem {
	var sender *PublicUser
	if user, ok := users[message.SenderID]; ok && message.SenderID != 0 {
		value := publicUser(user)
		sender = &value
	}
	metadata := any(map[string]any{})
	if len(message.Metadata) > 0 {
		metadata = message.Metadata
	}
	return MessageItem{
		ID: message.ID, ConversationID: message.ConversationID, Seq: message.Seq, Sender: sender,
		ClientMessageID: message.ClientMessageID, MessageType: message.MessageType,
		Content: message.Content, Metadata: metadata, CreatedAt: message.CreatedAt,
	}
}

func (s *ChatService) loadMessageSenders(ctx context.Context, messages []model.Message) (map[uint]model.User, error) {
	ids := make([]uint, 0, len(messages))
	seen := map[uint]bool{}
	for _, message := range messages {
		if message.SenderID > 0 && !seen[message.SenderID] {
			seen[message.SenderID] = true
			ids = append(ids, message.SenderID)
		}
	}
	users, err := s.users.FindActiveByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load message senders: %w", err)
	}
	result := make(map[uint]model.User, len(users))
	for _, user := range users {
		result[user.ID] = user
	}
	return result, nil
}

func conversationLastMessages(rows []repo.ConversationRecord) []model.Message {
	messages := make([]model.Message, 0, len(rows))
	for _, row := range rows {
		if row.LastMessage != nil {
			messages = append(messages, *row.LastMessage)
		}
	}
	return messages
}

func unreadCount(lastSeq, lastReadSeq uint64) uint64 {
	if lastReadSeq >= lastSeq {
		return 0
	}
	return lastSeq - lastReadSeq
}

func encodeConversationCursor(activityAt time.Time, id uint) string {
	buffer := make([]byte, 16)
	binary.BigEndian.PutUint64(buffer[:8], uint64(activityAt.UTC().UnixMilli()))
	binary.BigEndian.PutUint64(buffer[8:], uint64(id))
	return base64.RawURLEncoding.EncodeToString(buffer)
}

func decodeConversationCursor(cursor string) (time.Time, uint, error) {
	if cursor == "" {
		return time.Time{}, 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) != 16 {
		return time.Time{}, 0, ErrInvalidConversationRequest
	}
	milliseconds := binary.BigEndian.Uint64(decoded[:8])
	id64 := binary.BigEndian.Uint64(decoded[8:])
	if milliseconds == 0 || id64 == 0 || uint64(uint(id64)) != id64 {
		return time.Time{}, 0, ErrInvalidConversationRequest
	}
	return time.UnixMilli(int64(milliseconds)).UTC(), uint(id64), nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validMessageContent(content string) bool {
	runes := []rune(content)
	if len(runes) == 0 || len(runes) > 4000 || strings.TrimSpace(content) == "" {
		return false
	}
	for _, value := range runes {
		if value == 0 || (unicode.IsControl(value) && value != '\n' && value != '\t') {
			return false
		}
	}
	return true
}
