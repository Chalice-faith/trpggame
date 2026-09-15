package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"trpggame/internal/model"
)

const MaxGroupMembers = 50

var (
	ErrGroupMissing            = errors.New("group missing")
	ErrGroupPermissionDenied   = errors.New("group permission denied")
	ErrGroupFriendshipRequired = errors.New("group friendship required")
	ErrGroupMemberLimit        = errors.New("group member limit reached")
	ErrGroupOwnerConflict      = errors.New("group owner conflict")
	ErrGroupVersionConflict    = errors.New("group version conflict")
)

type GroupRecord struct {
	Group           model.Group
	ConversationID  uint
	CurrentUserRole model.GroupRole
	MemberCount     int
}

type GroupMemberRecord struct {
	Member model.GroupMember
	User   model.User
}

type GroupMutationResult struct {
	Record   GroupRecord
	Changed  bool
	Messages []model.Message
}

type GroupRepo struct{ db *gorm.DB }

func NewGroupRepo(db *gorm.DB) *GroupRepo { return &GroupRepo{db: db} }

func (r *GroupRepo) Create(ctx context.Context, ownerID uint, name, avatarURL string, now time.Time) (*GroupMutationResult, error) {
	var result GroupMutationResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", ownerID).First(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrGroupPermissionDenied
			}
			return err
		}
		group := model.Group{Name: name, AvatarURL: avatarURL, OwnerID: ownerID, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&group).Error; err != nil {
			return err
		}
		conversation := model.Conversation{Type: model.ConversationTypeGroup, GroupID: &group.ID, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&conversation).Error; err != nil {
			return err
		}
		member := model.GroupMember{
			GroupID: group.ID, UserID: ownerID, Role: model.GroupRoleOwner,
			Status: model.GroupMemberStatusActive, JoinedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		conversationMember := model.ConversationMember{
			ConversationID: conversation.ID, UserID: ownerID, Role: model.ConversationMemberRoleOwner,
			Status: model.ConversationMemberStatusActive, JoinedAt: now,
		}
		if err := tx.Create(&conversationMember).Error; err != nil {
			return err
		}
		message, err := appendGroupSystemMessage(tx, &conversation, ownerID, "group_created", 0, group.Version, "group created", now)
		if err != nil {
			return err
		}
		result = GroupMutationResult{
			Record:  GroupRecord{Group: group, ConversationID: conversation.ID, CurrentUserRole: model.GroupRoleOwner, MemberCount: 1},
			Changed: true, Messages: []model.Message{*message},
		}
		return nil
	})
	return &result, err
}

func (r *GroupRepo) List(ctx context.Context, userID, beforeID uint, limit int) ([]GroupRecord, error) {
	query := r.db.WithContext(ctx).Table("`groups` AS g").
		Select(`g.id, g.name, g.avatar_url, g.owner_id, g.version, g.created_at, g.updated_at,
 c.id AS conversation_id, me.role AS current_user_role,
 (SELECT COUNT(*) FROM group_members active_members WHERE active_members.group_id = g.id AND active_members.status = 'active') AS member_count`).
		Joins("JOIN group_members me ON me.group_id = g.id AND me.user_id = ? AND me.status = 'active'", userID).
		Joins("JOIN conversations c ON c.group_id = g.id AND c.type = 'group'")
	if beforeID > 0 {
		query = query.Where("g.id < ?", beforeID)
	}
	var rows []groupRecordRow
	if err := query.Order("g.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return groupRecords(rows), nil
}

func (r *GroupRepo) Get(ctx context.Context, userID, groupID uint) (*GroupRecord, error) {
	return loadGroupRecord(r.db.WithContext(ctx), userID, groupID)
}

func (r *GroupRepo) ListMembers(ctx context.Context, userID, groupID, beforeID uint, limit int) ([]GroupMemberRecord, error) {
	if _, err := loadActiveGroupMember(r.db.WithContext(ctx), groupID, userID, false); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGroupMissing
		}
		return nil, err
	}
	query := r.db.WithContext(ctx).Table("group_members gm").
		Select(`gm.id, gm.group_id, gm.user_id, gm.role, gm.status, gm.joined_at, gm.left_at, gm.created_at, gm.updated_at,
 u.id AS member_user_id, u.username AS member_username, u.nickname AS member_nickname, u.avatar_url AS member_avatar_url`).
		Joins("JOIN users u ON u.id = gm.user_id AND u.deleted_at IS NULL").
		Where("gm.group_id = ? AND gm.status = 'active'", groupID)
	if beforeID > 0 {
		query = query.Where("gm.id < ?", beforeID)
	}
	var rows []groupMemberRecordRow
	if err := query.Order("gm.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]GroupMemberRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, GroupMemberRecord{
			Member: model.GroupMember{ID: row.ID, GroupID: row.GroupID, UserID: row.UserID, Role: row.Role, Status: row.Status, JoinedAt: row.JoinedAt, LeftAt: row.LeftAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt},
			User:   model.User{ID: row.MemberUserID, Username: row.MemberUsername, Nickname: row.MemberNickname, AvatarURL: row.MemberAvatarURL},
		})
	}
	return result, nil
}

func (r *GroupRepo) Update(ctx context.Context, actorID, groupID uint, expectedVersion uint64, name, avatarURL *string, now time.Time) (*GroupMutationResult, error) {
	return r.mutate(ctx, actorID, groupID, expectedVersion, now, func(tx *gorm.DB, group *model.Group, conversation *model.Conversation, actor *model.GroupMember) ([]model.Message, bool, error) {
		if actor.Role != model.GroupRoleOwner {
			return nil, false, ErrGroupPermissionDenied
		}
		changes := make([]string, 0, 2)
		if name != nil && *name != group.Name {
			group.Name = *name
			changes = append(changes, "group_name_changed")
		}
		if avatarURL != nil && *avatarURL != group.AvatarURL {
			group.AvatarURL = *avatarURL
			changes = append(changes, "group_avatar_changed")
		}
		if len(changes) == 0 {
			return nil, false, nil
		}
		group.Version++
		group.UpdatedAt = now
		if err := tx.Save(group).Error; err != nil {
			return nil, false, err
		}
		messages := make([]model.Message, 0, len(changes))
		for _, event := range changes {
			message, err := appendGroupSystemMessage(tx, conversation, actorID, event, 0, group.Version, event, now)
			if err != nil {
				return nil, false, err
			}
			messages = append(messages, *message)
		}
		return messages, true, nil
	})
}

func (r *GroupRepo) Invite(ctx context.Context, actorID, groupID uint, targetIDs []uint, expectedVersion uint64, now time.Time) (*GroupMutationResult, error) {
	targets := append([]uint(nil), targetIDs...)
	sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
	return r.mutate(ctx, actorID, groupID, expectedVersion, now, func(tx *gorm.DB, group *model.Group, conversation *model.Conversation, actor *model.GroupMember) ([]model.Message, bool, error) {
		if actor.Role != model.GroupRoleOwner && actor.Role != model.GroupRoleAdmin {
			return nil, false, ErrGroupPermissionDenied
		}
		var activeCount int64
		if err := tx.Model(&model.GroupMember{}).Where("group_id = ? AND status = ?", groupID, model.GroupMemberStatusActive).Count(&activeCount).Error; err != nil {
			return nil, false, err
		}
		members := make(map[uint]model.GroupMember, len(targets))
		var existing []model.GroupMember
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("group_id = ? AND user_id IN ?", groupID, targets).Find(&existing).Error; err != nil {
			return nil, false, err
		}
		for _, member := range existing {
			members[member.UserID] = member
		}
		toAdd := 0
		for _, targetID := range targets {
			if targetID == actorID {
				continue
			}
			if member, ok := members[targetID]; !ok || !member.Active() {
				toAdd++
			} else if err := requireConversationMember(tx, conversation.ID, targetID, member.Role); err != nil {
				return nil, false, err
			}
		}
		if activeCount+int64(toAdd) > MaxGroupMembers {
			return nil, false, ErrGroupMemberLimit
		}
		for _, targetID := range targets {
			if member, ok := members[targetID]; ok && member.Active() {
				continue
			}
			var user model.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", targetID).First(&user).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, false, ErrGroupFriendshipRequired
				}
				return nil, false, err
			}
			lowID, highID := orderedPair(actorID, targetID)
			if err := lockAcceptedFriendship(tx, lowID, highID); err != nil {
				if errors.Is(err, ErrChatFriendshipRequired) {
					return nil, false, ErrGroupFriendshipRequired
				}
				return nil, false, err
			}
		}
		messages := make([]model.Message, 0, toAdd)
		for _, targetID := range targets {
			if targetID == actorID {
				continue
			}
			member, exists := members[targetID]
			if exists && member.Active() {
				continue
			}
			if exists {
				if err := tx.Model(&model.GroupMember{}).Where("id = ?", member.ID).Updates(map[string]any{
					"role": model.GroupRoleMember, "status": model.GroupMemberStatusActive,
					"joined_at": now, "left_at": nil, "updated_at": now,
				}).Error; err != nil {
					return nil, false, err
				}
			} else {
				member = model.GroupMember{GroupID: groupID, UserID: targetID, Role: model.GroupRoleMember, Status: model.GroupMemberStatusActive, JoinedAt: now, CreatedAt: now, UpdatedAt: now}
				if err := tx.Create(&member).Error; err != nil {
					return nil, false, err
				}
			}
			conversationMember := model.ConversationMember{ConversationID: conversation.ID, UserID: targetID, Role: model.ConversationMemberRoleMember, Status: model.ConversationMemberStatusActive, JoinedAt: now}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "conversation_id"}, {Name: "user_id"}},
				DoUpdates: clause.Assignments(map[string]any{
					"role": model.ConversationMemberRoleMember, "status": model.ConversationMemberStatusActive,
					"joined_at": now, "left_at": nil,
				}),
			}).Create(&conversationMember).Error; err != nil {
				return nil, false, err
			}
			members[targetID] = member
		}
		if toAdd == 0 {
			return nil, false, nil
		}
		group.Version++
		group.UpdatedAt = now
		if err := tx.Save(group).Error; err != nil {
			return nil, false, err
		}
		for _, targetID := range targets {
			original, existed := findMember(existing, targetID)
			if targetID == actorID || (existed && original.Active()) {
				continue
			}
			message, err := appendGroupSystemMessage(tx, conversation, actorID, "member_joined", targetID, group.Version, fmt.Sprintf("member %d joined the group", targetID), now)
			if err != nil {
				return nil, false, err
			}
			messages = append(messages, *message)
		}
		return messages, true, nil
	})
}

func (r *GroupRepo) SetRole(ctx context.Context, actorID, groupID, targetID uint, role model.GroupRole, expectedVersion uint64, now time.Time) (*GroupMutationResult, error) {
	return r.mutate(ctx, actorID, groupID, expectedVersion, now, func(tx *gorm.DB, group *model.Group, conversation *model.Conversation, actor *model.GroupMember) ([]model.Message, bool, error) {
		if actor.Role != model.GroupRoleOwner || targetID == actorID {
			return nil, false, ErrGroupPermissionDenied
		}
		target, err := loadActiveGroupMember(tx, groupID, targetID, true)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, false, ErrGroupMissing
			}
			return nil, false, err
		}
		if target.Role == model.GroupRoleOwner {
			return nil, false, ErrGroupOwnerConflict
		}
		if err := requireConversationMember(tx, conversation.ID, targetID, target.Role); err != nil {
			return nil, false, err
		}
		if target.Role == role {
			return nil, false, nil
		}
		if err := tx.Model(&model.GroupMember{}).Where("id = ?", target.ID).Updates(map[string]any{"role": role, "updated_at": now}).Error; err != nil {
			return nil, false, err
		}
		updatedConversationMember := tx.Model(&model.ConversationMember{}).Where("conversation_id = ? AND user_id = ? AND status = ?", conversation.ID, targetID, model.ConversationMemberStatusActive).
			Update("role", conversationRole(role))
		if updatedConversationMember.Error != nil {
			return nil, false, updatedConversationMember.Error
		}
		if updatedConversationMember.RowsAffected != 1 {
			return nil, false, fmt.Errorf("group member conversation mirror missing")
		}
		group.Version++
		group.UpdatedAt = now
		if err := tx.Save(group).Error; err != nil {
			return nil, false, err
		}
		message, err := appendGroupSystemMessage(tx, conversation, actorID, "member_role_changed", targetID, group.Version, fmt.Sprintf("member %d role changed", targetID), now)
		if err != nil {
			return nil, false, err
		}
		return []model.Message{*message}, true, nil
	})
}

func (r *GroupRepo) Remove(ctx context.Context, actorID, groupID, targetID uint, expectedVersion uint64, now time.Time) (*GroupMutationResult, error) {
	return r.mutate(ctx, actorID, groupID, expectedVersion, now, func(tx *gorm.DB, group *model.Group, conversation *model.Conversation, actor *model.GroupMember) ([]model.Message, bool, error) {
		target, err := loadGroupMember(tx, groupID, targetID, true)
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, false, err
			}
			if targetID != actorID && (actor.Role == model.GroupRoleOwner || actor.Role == model.GroupRoleAdmin) {
				return nil, false, nil
			}
			return nil, false, ErrGroupMissing
		}
		if !target.Active() {
			return nil, false, nil
		}
		self := targetID == actorID
		if self {
			if target.Role == model.GroupRoleOwner {
				return nil, false, ErrGroupOwnerConflict
			}
		} else {
			switch actor.Role {
			case model.GroupRoleOwner:
				if target.Role == model.GroupRoleOwner {
					return nil, false, ErrGroupOwnerConflict
				}
			case model.GroupRoleAdmin:
				if target.Role != model.GroupRoleMember {
					return nil, false, ErrGroupPermissionDenied
				}
			default:
				return nil, false, ErrGroupPermissionDenied
			}
		}
		if err := tx.Model(&model.GroupMember{}).Where("id = ?", target.ID).Updates(map[string]any{
			"role": model.GroupRoleMember, "status": model.GroupMemberStatusLeft, "left_at": now, "updated_at": now,
		}).Error; err != nil {
			return nil, false, err
		}
		updatedConversationMember := tx.Model(&model.ConversationMember{}).Where("conversation_id = ? AND user_id = ? AND status = ?", conversation.ID, targetID, model.ConversationMemberStatusActive).Updates(map[string]any{
			"role": model.ConversationMemberRoleMember, "status": model.ConversationMemberStatusLeft, "left_at": now,
		})
		if updatedConversationMember.Error != nil {
			return nil, false, updatedConversationMember.Error
		}
		if updatedConversationMember.RowsAffected != 1 {
			return nil, false, fmt.Errorf("group member conversation mirror missing")
		}
		group.Version++
		group.UpdatedAt = now
		if err := tx.Save(group).Error; err != nil {
			return nil, false, err
		}
		event, content := "member_removed", fmt.Sprintf("member %d was removed from the group", targetID)
		if self {
			event, content = "member_left", fmt.Sprintf("member %d left the group", targetID)
		}
		message, err := appendGroupSystemMessage(tx, conversation, actorID, event, targetID, group.Version, content, now)
		if err != nil {
			return nil, false, err
		}
		return []model.Message{*message}, true, nil
	})
}

func (r *GroupRepo) Transfer(ctx context.Context, actorID, groupID, targetID uint, expectedVersion uint64, now time.Time) (*GroupMutationResult, error) {
	return r.mutate(ctx, actorID, groupID, expectedVersion, now, func(tx *gorm.DB, group *model.Group, conversation *model.Conversation, actor *model.GroupMember) ([]model.Message, bool, error) {
		if actor.Role != model.GroupRoleOwner {
			return nil, false, ErrGroupPermissionDenied
		}
		if targetID == actorID {
			return nil, false, nil
		}
		target, err := loadActiveGroupMember(tx, groupID, targetID, true)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, false, ErrGroupOwnerConflict
			}
			return nil, false, err
		}
		if err := requireConversationMember(tx, conversation.ID, targetID, target.Role); err != nil {
			return nil, false, err
		}
		if err := tx.Model(&model.GroupMember{}).Where("id = ?", actor.ID).Updates(map[string]any{"role": model.GroupRoleMember, "updated_at": now}).Error; err != nil {
			return nil, false, err
		}
		if err := tx.Model(&model.GroupMember{}).Where("id = ?", target.ID).Updates(map[string]any{"role": model.GroupRoleOwner, "updated_at": now}).Error; err != nil {
			return nil, false, err
		}
		oldOwnerConversation := tx.Model(&model.ConversationMember{}).Where("conversation_id = ? AND user_id = ? AND status = ?", conversation.ID, actorID, model.ConversationMemberStatusActive).Update("role", model.ConversationMemberRoleMember)
		if oldOwnerConversation.Error != nil {
			return nil, false, oldOwnerConversation.Error
		}
		if oldOwnerConversation.RowsAffected != 1 {
			return nil, false, fmt.Errorf("old owner conversation mirror missing")
		}
		newOwnerConversation := tx.Model(&model.ConversationMember{}).Where("conversation_id = ? AND user_id = ? AND status = ?", conversation.ID, targetID, model.ConversationMemberStatusActive).Update("role", model.ConversationMemberRoleOwner)
		if newOwnerConversation.Error != nil {
			return nil, false, newOwnerConversation.Error
		}
		if newOwnerConversation.RowsAffected != 1 {
			return nil, false, fmt.Errorf("new owner conversation mirror missing")
		}
		group.OwnerID = targetID
		group.Version++
		group.UpdatedAt = now
		if err := tx.Save(group).Error; err != nil {
			return nil, false, err
		}
		message, err := appendGroupSystemMessage(tx, conversation, actorID, "owner_transferred", targetID, group.Version, fmt.Sprintf("ownership transferred to member %d", targetID), now)
		if err != nil {
			return nil, false, err
		}
		return []model.Message{*message}, true, nil
	})
}

type groupMutation func(*gorm.DB, *model.Group, *model.Conversation, *model.GroupMember) ([]model.Message, bool, error)

func (r *GroupRepo) mutate(ctx context.Context, actorID, groupID uint, expectedVersion uint64, now time.Time, mutation groupMutation) (*GroupMutationResult, error) {
	var result GroupMutationResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group model.Group
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&group, groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrGroupMissing
			}
			return err
		}
		if group.Version != expectedVersion {
			return ErrGroupVersionConflict
		}
		actor, err := loadActiveGroupMember(tx, groupID, actorID, true)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrGroupMissing
			}
			return err
		}
		var conversation model.Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("type = ? AND group_id = ?", model.ConversationTypeGroup, groupID).First(&conversation).Error; err != nil {
			return err
		}
		if err := requireConversationMember(tx, conversation.ID, actorID, actor.Role); err != nil {
			return err
		}
		messages, changed, err := mutation(tx, &group, &conversation, actor)
		if err != nil {
			return err
		}
		record, err := loadGroupRecord(tx, actorID, groupID)
		if err != nil {
			if changed && actorID != group.OwnerID {
				// 主动退群后，当前用户不再可见；仍返回最终群版本供 Handler 成功响应。
				var memberCount int64
				if countErr := tx.Model(&model.GroupMember{}).Where("group_id = ? AND status = ?", groupID, model.GroupMemberStatusActive).Count(&memberCount).Error; countErr != nil {
					return countErr
				}
				result = GroupMutationResult{Record: GroupRecord{Group: group, ConversationID: conversation.ID, MemberCount: int(memberCount)}, Changed: true, Messages: messages}
				return nil
			}
			return err
		}
		result = GroupMutationResult{Record: *record, Changed: changed, Messages: messages}
		return nil
	})
	return &result, err
}

type groupRecordRow struct {
	ID              uint
	Name            string
	AvatarURL       string
	OwnerID         uint
	Version         uint64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ConversationID  uint
	CurrentUserRole model.GroupRole
	MemberCount     int
}

type groupMemberRecordRow struct {
	ID              uint
	GroupID         uint
	UserID          uint
	Role            model.GroupRole
	Status          model.GroupMemberStatus
	JoinedAt        time.Time
	LeftAt          *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	MemberUserID    uint
	MemberUsername  string
	MemberNickname  string
	MemberAvatarURL string
}

func loadGroupRecord(db *gorm.DB, userID, groupID uint) (*GroupRecord, error) {
	var row groupRecordRow
	err := db.Table("`groups` AS g").
		Select(`g.id, g.name, g.avatar_url, g.owner_id, g.version, g.created_at, g.updated_at,
 c.id AS conversation_id, me.role AS current_user_role,
 (SELECT COUNT(*) FROM group_members active_members WHERE active_members.group_id = g.id AND active_members.status = 'active') AS member_count`).
		Joins("JOIN group_members me ON me.group_id = g.id AND me.user_id = ? AND me.status = 'active'", userID).
		Joins("JOIN conversations c ON c.group_id = g.id AND c.type = 'group'").
		Where("g.id = ?", groupID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrGroupMissing
	}
	if err != nil {
		return nil, err
	}
	record := groupRecords([]groupRecordRow{row})[0]
	return &record, nil
}

func groupRecords(rows []groupRecordRow) []GroupRecord {
	result := make([]GroupRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, GroupRecord{
			Group:          model.Group{ID: row.ID, Name: row.Name, AvatarURL: row.AvatarURL, OwnerID: row.OwnerID, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt},
			ConversationID: row.ConversationID, CurrentUserRole: row.CurrentUserRole, MemberCount: row.MemberCount,
		})
	}
	return result
}

func loadActiveGroupMember(db *gorm.DB, groupID, userID uint, lock bool) (*model.GroupMember, error) {
	query := db.Where("group_id = ? AND user_id = ? AND status = ?", groupID, userID, model.GroupMemberStatusActive)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var member model.GroupMember
	err := query.First(&member).Error
	return &member, err
}

func loadGroupMember(db *gorm.DB, groupID, userID uint, lock bool) (*model.GroupMember, error) {
	query := db.Where("group_id = ? AND user_id = ?", groupID, userID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var member model.GroupMember
	err := query.First(&member).Error
	return &member, err
}

func appendGroupSystemMessage(tx *gorm.DB, conversation *model.Conversation, actorID uint, event string, targetID uint, version uint64, content string, now time.Time) (*model.Message, error) {
	if conversation.LastSeq == ^uint64(0) {
		return nil, fmt.Errorf("conversation sequence exhausted")
	}
	metadata := map[string]any{"event": event, "actor_user_id": actorID, "group_id": *conversation.GroupID, "group_version": version}
	if targetID > 0 {
		metadata["target_user_id"] = targetID
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	message := model.Message{
		ConversationID: conversation.ID, Seq: conversation.LastSeq + 1, SenderID: actorID,
		ClientMessageID: uuid.NewString(), MessageType: model.MessageTypeSystem,
		Content: content, Metadata: encoded, CreatedAt: now,
	}
	if err := tx.Create(&message).Error; err != nil {
		return nil, err
	}
	conversation.LastSeq = message.Seq
	conversation.LastMessageAt = &now
	conversation.UpdatedAt = now
	if err := tx.Model(&model.Conversation{}).Where("id = ?", conversation.ID).Updates(map[string]any{
		"last_seq": message.Seq, "last_message_at": now, "updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&model.ConversationMember{}).
		Where("conversation_id = ? AND user_id = ?", conversation.ID, actorID).
		Update("last_read_seq", gorm.Expr("GREATEST(last_read_seq, ?)", message.Seq)).Error; err != nil {
		return nil, err
	}
	return &message, nil
}

func conversationRole(role model.GroupRole) model.ConversationMemberRole {
	return model.ConversationMemberRole(role)
}

func requireConversationMember(tx *gorm.DB, conversationID, userID uint, role model.GroupRole) error {
	var member model.ConversationMember
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("conversation_id = ? AND user_id = ? AND status = ?", conversationID, userID, model.ConversationMemberStatusActive).
		First(&member).Error; err != nil {
		return fmt.Errorf("group member conversation mirror missing: %w", err)
	}
	if member.Role != conversationRole(role) {
		return fmt.Errorf("group member conversation role drift")
	}
	return nil
}

func orderedPair(left, right uint) (uint, uint) {
	if left < right {
		return left, right
	}
	return right, left
}

func findMember(members []model.GroupMember, userID uint) (model.GroupMember, bool) {
	for _, member := range members {
		if member.UserID == userID {
			return member, true
		}
	}
	return model.GroupMember{}, false
}
