package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

var (
	ErrInvalidGroupRequest     = errors.New("invalid group request")
	ErrGroupNotFound           = errors.New("group not found")
	ErrGroupPermissionDenied   = errors.New("group permission denied")
	ErrGroupFriendshipRequired = errors.New("group friendship required")
	ErrGroupMemberLimitReached = errors.New("group member limit reached")
	ErrGroupOwnerConflict      = errors.New("group owner conflict")
	ErrGroupVersionConflict    = errors.New("group version conflict")
)

type GroupRepository interface {
	Create(context.Context, uint, string, string, time.Time) (*repo.GroupMutationResult, error)
	List(context.Context, uint, uint, int) ([]repo.GroupRecord, error)
	Get(context.Context, uint, uint) (*repo.GroupRecord, error)
	ListMembers(context.Context, uint, uint, uint, int) ([]repo.GroupMemberRecord, error)
	Update(context.Context, uint, uint, uint64, *string, *string, time.Time) (*repo.GroupMutationResult, error)
	Invite(context.Context, uint, uint, []uint, uint64, time.Time) (*repo.GroupMutationResult, error)
	SetRole(context.Context, uint, uint, uint, model.GroupRole, uint64, time.Time) (*repo.GroupMutationResult, error)
	Remove(context.Context, uint, uint, uint, uint64, time.Time) (*repo.GroupMutationResult, error)
	Transfer(context.Context, uint, uint, uint, uint64, time.Time) (*repo.GroupMutationResult, error)
}

// GroupMutationPublisher 在群事务提交成功后执行尽力实时投递。
// 推送失败不能改变 REST 操作的持久化结果。
type GroupMutationPublisher interface {
	PublishGroupMutation(*repo.GroupMutationResult)
}

type GroupSummary struct {
	ID              uint             `json:"id"`
	Name            string           `json:"name"`
	AvatarURL       string           `json:"avatar_url"`
	OwnerID         uint             `json:"owner_id"`
	ConversationID  uint             `json:"conversation_id"`
	CurrentUserRole *model.GroupRole `json:"current_user_role"`
	MemberCount     int              `json:"member_count"`
	Version         uint64           `json:"version"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type GroupPage struct {
	Items      []GroupSummary `json:"items"`
	NextCursor string         `json:"next_cursor"`
}

type GroupMemberItem struct {
	ID       uint            `json:"id"`
	User     PublicUser      `json:"user"`
	Role     model.GroupRole `json:"role"`
	JoinedAt time.Time       `json:"joined_at"`
}

type GroupMemberPage struct {
	Items      []GroupMemberItem `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

type GroupService struct {
	groups    GroupRepository
	publisher GroupMutationPublisher
	now       func() time.Time
}

func NewGroupService(groups GroupRepository) *GroupService {
	return &GroupService{groups: groups, now: time.Now}
}

// SetMutationPublisher 在服务开始处理请求前注入实时投递器。
func (s *GroupService) SetMutationPublisher(publisher GroupMutationPublisher) {
	if s != nil {
		s.publisher = publisher
	}
}

func (s *GroupService) Create(ctx context.Context, userID uint, name, avatarURL string) (*GroupSummary, error) {
	name = strings.TrimSpace(name)
	avatarURL = strings.TrimSpace(avatarURL)
	if userID == 0 || !validGroupName(name) || !validGroupAvatar(avatarURL) {
		return nil, ErrInvalidGroupRequest
	}
	result, err := s.groups.Create(ctx, userID, name, avatarURL, s.now().UTC())
	if err != nil {
		return nil, mapGroupRepositoryError("create group", err)
	}
	s.publishMutation(result)
	summary := groupSummary(result.Record)
	return &summary, nil
}

func (s *GroupService) List(ctx context.Context, userID uint, cursor string, limit int) (*GroupPage, error) {
	beforeID, err := groupCursor(cursor)
	if userID == 0 || err != nil || limit < 1 || limit > 100 {
		return nil, ErrInvalidGroupRequest
	}
	rows, err := s.groups.List(ctx, userID, beforeID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	next := ""
	if len(rows) > limit {
		next = strconv.FormatUint(uint64(rows[limit-1].Group.ID), 10)
		rows = rows[:limit]
	}
	items := make([]GroupSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, groupSummary(row))
	}
	return &GroupPage{Items: items, NextCursor: next}, nil
}

func (s *GroupService) Get(ctx context.Context, userID, groupID uint) (*GroupSummary, error) {
	if userID == 0 || groupID == 0 {
		return nil, ErrInvalidGroupRequest
	}
	record, err := s.groups.Get(ctx, userID, groupID)
	if err != nil {
		return nil, mapGroupRepositoryError("get group", err)
	}
	summary := groupSummary(*record)
	return &summary, nil
}

func (s *GroupService) ListMembers(ctx context.Context, userID, groupID uint, cursor string, limit int) (*GroupMemberPage, error) {
	beforeID, err := groupCursor(cursor)
	if userID == 0 || groupID == 0 || err != nil || limit < 1 || limit > 100 {
		return nil, ErrInvalidGroupRequest
	}
	rows, err := s.groups.ListMembers(ctx, userID, groupID, beforeID, limit+1)
	if err != nil {
		return nil, mapGroupRepositoryError("list group members", err)
	}
	next := ""
	if len(rows) > limit {
		next = strconv.FormatUint(uint64(rows[limit-1].Member.ID), 10)
		rows = rows[:limit]
	}
	items := make([]GroupMemberItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, GroupMemberItem{ID: row.Member.ID, User: publicUser(row.User), Role: row.Member.Role, JoinedAt: row.Member.JoinedAt})
	}
	return &GroupMemberPage{Items: items, NextCursor: next}, nil
}

func (s *GroupService) Update(ctx context.Context, userID, groupID uint, expectedVersion uint64, name, avatarURL *string) (*GroupSummary, error) {
	if userID == 0 || groupID == 0 || expectedVersion == 0 || (name == nil && avatarURL == nil) {
		return nil, ErrInvalidGroupRequest
	}
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if !validGroupName(trimmed) {
			return nil, ErrInvalidGroupRequest
		}
		name = &trimmed
	}
	if avatarURL != nil {
		trimmed := strings.TrimSpace(*avatarURL)
		if !validGroupAvatar(trimmed) {
			return nil, ErrInvalidGroupRequest
		}
		avatarURL = &trimmed
	}
	result, err := s.groups.Update(ctx, userID, groupID, expectedVersion, name, avatarURL, s.now().UTC())
	return s.groupMutationSummary(result, err, "update group")
}

func (s *GroupService) Invite(ctx context.Context, userID, groupID uint, targetIDs []uint, expectedVersion uint64) (*GroupSummary, error) {
	if userID == 0 || groupID == 0 || expectedVersion == 0 || len(targetIDs) == 0 || len(targetIDs) > 20 {
		return nil, ErrInvalidGroupRequest
	}
	seen := make(map[uint]struct{}, len(targetIDs))
	unique := make([]uint, 0, len(targetIDs))
	for _, targetID := range targetIDs {
		if targetID == 0 || targetID == userID {
			return nil, ErrInvalidGroupRequest
		}
		if _, ok := seen[targetID]; ok {
			continue
		}
		seen[targetID] = struct{}{}
		unique = append(unique, targetID)
	}
	result, err := s.groups.Invite(ctx, userID, groupID, unique, expectedVersion, s.now().UTC())
	return s.groupMutationSummary(result, err, "invite group members")
}

func (s *GroupService) SetRole(ctx context.Context, userID, groupID, targetID uint, role model.GroupRole, expectedVersion uint64) (*GroupSummary, error) {
	if userID == 0 || groupID == 0 || targetID == 0 || expectedVersion == 0 ||
		(role != model.GroupRoleMember && role != model.GroupRoleAdmin) {
		return nil, ErrInvalidGroupRequest
	}
	result, err := s.groups.SetRole(ctx, userID, groupID, targetID, role, expectedVersion, s.now().UTC())
	return s.groupMutationSummary(result, err, "set group member role")
}

func (s *GroupService) Remove(ctx context.Context, userID, groupID, targetID uint, expectedVersion uint64) (*GroupSummary, error) {
	if userID == 0 || groupID == 0 || targetID == 0 || expectedVersion == 0 {
		return nil, ErrInvalidGroupRequest
	}
	result, err := s.groups.Remove(ctx, userID, groupID, targetID, expectedVersion, s.now().UTC())
	return s.groupMutationSummary(result, err, "remove group member")
}

func (s *GroupService) Transfer(ctx context.Context, userID, groupID, targetID uint, expectedVersion uint64) (*GroupSummary, error) {
	if userID == 0 || groupID == 0 || targetID == 0 || expectedVersion == 0 {
		return nil, ErrInvalidGroupRequest
	}
	result, err := s.groups.Transfer(ctx, userID, groupID, targetID, expectedVersion, s.now().UTC())
	return s.groupMutationSummary(result, err, "transfer group owner")
}

func (s *GroupService) groupMutationSummary(result *repo.GroupMutationResult, err error, operation string) (*GroupSummary, error) {
	if err != nil {
		return nil, mapGroupRepositoryError(operation, err)
	}
	s.publishMutation(result)
	summary := groupSummary(result.Record)
	return &summary, nil
}

func (s *GroupService) publishMutation(result *repo.GroupMutationResult) {
	if result != nil && result.Changed && s.publisher != nil {
		s.publisher.PublishGroupMutation(result)
	}
}

func mapGroupRepositoryError(operation string, err error) error {
	switch {
	case errors.Is(err, repo.ErrGroupMissing):
		return ErrGroupNotFound
	case errors.Is(err, repo.ErrGroupPermissionDenied):
		return ErrGroupPermissionDenied
	case errors.Is(err, repo.ErrGroupFriendshipRequired), errors.Is(err, repo.ErrChatFriendshipRequired):
		return ErrGroupFriendshipRequired
	case errors.Is(err, repo.ErrGroupMemberLimit):
		return ErrGroupMemberLimitReached
	case errors.Is(err, repo.ErrGroupOwnerConflict):
		return ErrGroupOwnerConflict
	case errors.Is(err, repo.ErrGroupVersionConflict):
		return ErrGroupVersionConflict
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func groupSummary(record repo.GroupRecord) GroupSummary {
	var role *model.GroupRole
	if record.CurrentUserRole != "" {
		value := record.CurrentUserRole
		role = &value
	}
	return GroupSummary{
		ID: record.Group.ID, Name: record.Group.Name, AvatarURL: record.Group.AvatarURL,
		OwnerID: record.Group.OwnerID, ConversationID: record.ConversationID,
		CurrentUserRole: role, MemberCount: record.MemberCount,
		Version: record.Group.Version, CreatedAt: record.Group.CreatedAt, UpdatedAt: record.Group.UpdatedAt,
	}
}

func groupCursor(cursor string) (uint, error) {
	if strings.TrimSpace(cursor) == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil || value == 0 || uint64(uint(value)) != value {
		return 0, ErrInvalidGroupRequest
	}
	return uint(value), nil
}

func validGroupName(value string) bool {
	count := 0
	for _, current := range value {
		if current == '\x00' || unicode.IsControl(current) {
			return false
		}
		count++
	}
	return count >= 1 && count <= 80
}

func validGroupAvatar(value string) bool {
	if len([]rune(value)) > 2048 {
		return false
	}
	for _, current := range value {
		if current == '\x00' || unicode.IsControl(current) {
			return false
		}
	}
	return true
}
