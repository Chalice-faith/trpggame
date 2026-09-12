package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"trpggame/internal/model"
)

var (
	ErrChatFriendshipRequired  = errors.New("chat friendship required")
	ErrChatConversationMissing = errors.New("chat conversation missing")
	ErrChatReadConflict        = errors.New("chat read sequence conflict")
	ErrChatIdempotencyConflict = errors.New("chat idempotency conflict")
)

type ConversationRecord struct {
	Conversation     model.Conversation
	LastReadSeq      uint64
	ActivityAt       time.Time
	Peer             model.User
	FriendshipStatus model.FriendshipStatus
	LastMessage      *model.Message
}

type SendMessageResult struct {
	Message   model.Message
	Duplicate bool
}

type conversationRecordRow struct {
	ID               uint
	Type             model.ConversationType
	DirectLowID      uint
	DirectHighID     uint
	GroupID          *uint
	LastSeq          uint64
	LastMessageAt    *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastReadSeq      uint64
	ActivityAt       time.Time
	PeerID           uint
	PeerUsername     string
	PeerNickname     string
	PeerAvatarURL    string
	FriendshipStatus model.FriendshipStatus
	LastMessageID    *uint
	LastMessageSeq   *uint64
	LastSenderID     *uint
	LastClientID     *string
	LastMessageType  *model.MessageType
	LastContent      *string
	LastMetadata     []byte
	LastCreatedAt    *time.Time
}

type ChatRepo struct{ db *gorm.DB }

func NewChatRepo(db *gorm.DB) *ChatRepo { return &ChatRepo{db: db} }

func (r *ChatRepo) EnsureDirectConversation(ctx context.Context, lowID, highID uint) (*model.Conversation, error) {
	var conversation model.Conversation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAcceptedFriendship(tx, lowID, highID); err != nil {
			return err
		}
		candidate := model.Conversation{
			Type: model.ConversationTypeDirect, DirectLowID: &lowID, DirectHighID: &highID,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("direct_low_id = ? AND direct_high_id = ?", lowID, highID).
			First(&conversation).Error; err != nil {
			return err
		}
		joinedAt := time.Now().UTC()
		for _, userID := range []uint{lowID, highID} {
			member := model.ConversationMember{
				ConversationID: conversation.ID, UserID: userID,
				Role: model.ConversationMemberRoleMember, Status: model.ConversationMemberStatusActive,
				JoinedAt: joinedAt,
			}
			if err := tx.Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{
				"role": model.ConversationMemberRoleMember, "status": model.ConversationMemberStatusActive, "left_at": nil,
			})}).Create(&member).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &conversation, nil
}

func (r *ChatRepo) GetConversation(ctx context.Context, userID, conversationID uint) (*ConversationRecord, error) {
	rows, err := r.queryConversations(ctx, userID, " AND c.id = ?", []any{conversationID}, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrChatConversationMissing
	}
	return &rows[0], nil
}

func (r *ChatRepo) ListConversations(ctx context.Context, userID uint, before time.Time, beforeID uint, limit int) ([]ConversationRecord, error) {
	extra := ""
	args := []any{}
	if !before.IsZero() {
		extra = " AND (COALESCE(c.last_message_at, c.created_at) < ? OR (COALESCE(c.last_message_at, c.created_at) = ? AND c.id < ?))"
		args = append(args, before, before, beforeID)
	}
	return r.queryConversations(ctx, userID, extra, args, limit)
}

func (r *ChatRepo) queryConversations(ctx context.Context, userID uint, extra string, extraArgs []any, limit int) ([]ConversationRecord, error) {
	query := `SELECT
 c.id, c.type, c.direct_low_id, c.direct_high_id, c.group_id, c.last_seq,
 c.last_message_at, c.created_at, c.updated_at, me.last_read_seq,
 COALESCE(c.last_message_at, c.created_at) AS activity_at,
 peer.id AS peer_id, peer.username AS peer_username, peer.nickname AS peer_nickname,
 peer.avatar_url AS peer_avatar_url, COALESCE(f.status, '') AS friendship_status,
 lm.id AS last_message_id, lm.seq AS last_message_seq, lm.sender_id AS last_sender_id,
 lm.client_message_id AS last_client_id, lm.message_type AS last_message_type,
 lm.content AS last_content, lm.metadata AS last_metadata, lm.created_at AS last_created_at
FROM conversations c
JOIN conversation_members me ON me.conversation_id = c.id AND me.user_id = ? AND me.status = 'active'
JOIN users peer ON peer.id = CASE WHEN c.direct_low_id = ? THEN c.direct_high_id ELSE c.direct_low_id END
 AND peer.deleted_at IS NULL
LEFT JOIN friendships f ON f.user_low_id = c.direct_low_id AND f.user_high_id = c.direct_high_id
LEFT JOIN messages lm ON lm.conversation_id = c.id AND lm.seq = c.last_seq
WHERE c.type = 'direct'` + extra + `
ORDER BY COALESCE(c.last_message_at, c.created_at) DESC, c.id DESC
LIMIT ?`
	args := []any{userID, userID}
	args = append(args, extraArgs...)
	args = append(args, limit)
	var rows []conversationRecordRow
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]ConversationRecord, 0, len(rows))
	for _, row := range rows {
		low, high := row.DirectLowID, row.DirectHighID
		record := ConversationRecord{
			Conversation: model.Conversation{
				ID: row.ID, Type: row.Type, DirectLowID: &low, DirectHighID: &high, GroupID: row.GroupID,
				LastSeq: row.LastSeq, LastMessageAt: row.LastMessageAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			},
			LastReadSeq: row.LastReadSeq, ActivityAt: row.ActivityAt,
			Peer:             model.User{ID: row.PeerID, Username: row.PeerUsername, Nickname: row.PeerNickname, AvatarURL: row.PeerAvatarURL},
			FriendshipStatus: row.FriendshipStatus,
		}
		if row.LastMessageID != nil && row.LastMessageSeq != nil && row.LastSenderID != nil && row.LastClientID != nil && row.LastMessageType != nil && row.LastContent != nil && row.LastCreatedAt != nil {
			record.LastMessage = &model.Message{
				ID: *row.LastMessageID, ConversationID: row.ID, Seq: *row.LastMessageSeq,
				SenderID: *row.LastSenderID, ClientMessageID: *row.LastClientID,
				MessageType: *row.LastMessageType, Content: *row.LastContent,
				Metadata: append([]byte(nil), row.LastMetadata...), CreatedAt: *row.LastCreatedAt,
			}
		}
		result = append(result, record)
	}
	return result, nil
}

func (r *ChatRepo) ListMessages(ctx context.Context, userID, conversationID uint, beforeSeq uint64, limit int) ([]model.Message, error) {
	query := r.db.WithContext(ctx).Model(&model.Message{}).
		Joins("JOIN conversation_members cm ON cm.conversation_id = messages.conversation_id AND cm.user_id = ? AND cm.status = ?", userID, model.ConversationMemberStatusActive).
		Where("messages.conversation_id = ?", conversationID)
	if beforeSeq > 0 {
		query = query.Where("messages.seq < ?", beforeSeq)
	}
	var messages []model.Message
	if err := query.Order("messages.seq DESC").Limit(limit).Find(&messages).Error; err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		var count int64
		if err := r.db.WithContext(ctx).Model(&model.ConversationMember{}).
			Where("conversation_id = ? AND user_id = ? AND status = ?", conversationID, userID, model.ConversationMemberStatusActive).
			Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, ErrChatConversationMissing
		}
	}
	return messages, nil
}

func (r *ChatRepo) MarkRead(ctx context.Context, userID, conversationID uint, requested uint64) (uint64, uint64, error) {
	var current, lastSeq uint64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conversation model.Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conversation, conversationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChatConversationMissing
			}
			return err
		}
		lastSeq = conversation.LastSeq
		if requested > lastSeq {
			return ErrChatReadConflict
		}
		var member model.ConversationMember
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("conversation_id = ? AND user_id = ? AND status = ?", conversationID, userID, model.ConversationMemberStatusActive).
			First(&member).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChatConversationMissing
			}
			return err
		}
		if requested > member.LastReadSeq {
			if err := tx.Model(&model.ConversationMember{}).Where("id = ?", member.ID).
				Update("last_read_seq", gorm.Expr("GREATEST(last_read_seq, ?)", requested)).Error; err != nil {
				return err
			}
			member.LastReadSeq = requested
		}
		current = member.LastReadSeq
		return nil
	})
	return current, lastSeq, err
}

func (r *ChatRepo) SendMessage(ctx context.Context, userID, conversationID uint, clientMessageID, content string, now time.Time) (*SendMessageResult, error) {
	var result SendMessageResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var snapshot model.Conversation
		if err := tx.First(&snapshot, conversationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChatConversationMissing
			}
			return err
		}
		if !snapshot.IncludesDirectUser(userID) || snapshot.DirectLowID == nil || snapshot.DirectHighID == nil {
			return ErrChatConversationMissing
		}
		if err := lockAcceptedFriendship(tx, *snapshot.DirectLowID, *snapshot.DirectHighID); err != nil {
			return err
		}
		var conversation model.Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conversation, conversationID).Error; err != nil {
			return err
		}
		if !conversation.IncludesDirectUser(userID) {
			return ErrChatConversationMissing
		}
		var member model.ConversationMember
		if err := tx.Where("conversation_id = ? AND user_id = ? AND status = ?", conversationID, userID, model.ConversationMemberStatusActive).
			First(&member).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChatConversationMissing
			}
			return err
		}
		var existing model.Message
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("conversation_id = ? AND sender_id = ? AND client_message_id = ?", conversationID, userID, clientMessageID).
			First(&existing).Error
		if err == nil {
			if existing.MessageType != model.MessageTypeText || existing.Content != content {
				return ErrChatIdempotencyConflict
			}
			result = SendMessageResult{Message: existing, Duplicate: true}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if conversation.LastSeq == ^uint64(0) {
			return fmt.Errorf("conversation sequence exhausted")
		}
		message := model.Message{
			ConversationID: conversationID, Seq: conversation.LastSeq + 1, SenderID: userID,
			ClientMessageID: clientMessageID, MessageType: model.MessageTypeText,
			Content: content, Metadata: []byte(`{}`), CreatedAt: now,
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Conversation{}).Where("id = ?", conversationID).Updates(map[string]any{
			"last_seq": message.Seq, "last_message_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ConversationMember{}).Where("id = ?", member.ID).
			Update("last_read_seq", gorm.Expr("GREATEST(last_read_seq, ?)", message.Seq)).Error; err != nil {
			return err
		}
		result = SendMessageResult{Message: message}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func lockAcceptedFriendship(tx *gorm.DB, lowID, highID uint) error {
	var friendship model.Friendship
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_low_id = ? AND user_high_id = ? AND status = ?", lowID, highID, model.FriendshipStatusAccepted).
		First(&friendship).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrChatFriendshipRequired
		}
		return err
	}
	return nil
}
