package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

var (
	ErrInvalidFriendRequest = errors.New("invalid friend request")
	ErrFriendTargetNotFound = errors.New("friend target not found")
	ErrCannotFriendSelf     = errors.New("cannot add yourself as a friend")
	ErrFriendRequestMissing = errors.New("friend request not found")
	ErrFriendForbidden      = errors.New("friend operation forbidden")
	ErrFriendStateConflict  = errors.New("friendship state conflict")
	ErrFriendshipNotFound   = errors.New("friendship not found")
	ErrInvalidFriendQuery   = errors.New("invalid friend query")
)

type FriendRepository interface {
	MutatePair(context.Context, uint, uint, *model.Friendship, repo.FriendshipMutation) (*model.Friendship, bool, error)
	MutateByID(context.Context, uint, repo.FriendshipMutation) (*model.Friendship, bool, error)
	ListRequests(context.Context, uint, bool, model.FriendshipStatus, uint, int) ([]model.Friendship, error)
	ListFriends(context.Context, uint, uint, int) ([]model.Friendship, error)
	FindRelationshipsWithPeers(context.Context, uint, []uint) ([]model.Friendship, error)
}

type FriendUserRepository interface {
	FindActiveByID(context.Context, uint) (*model.User, error)
	FindActiveByIDs(context.Context, []uint) ([]model.User, error)
	SearchActive(context.Context, uint, string) ([]model.User, error)
}

type PresenceStatus string

const (
	PresenceOffline PresenceStatus = "offline"
	PresenceOnline  PresenceStatus = "online"
	PresenceUnknown PresenceStatus = "unknown"
)

type PresenceProvider interface {
	Statuses(context.Context, []uint) (map[uint]PresenceStatus, error)
}

type FriendshipPublisher interface {
	PublishFriendshipUpdated(context.Context, FriendshipUpdatedEvent) error
}

type FriendshipUpdatedEvent struct {
	FriendshipID uint                   `json:"friendship_id"`
	UserLowID    uint                   `json:"user_low_id"`
	UserHighID   uint                   `json:"user_high_id"`
	Status       model.FriendshipStatus `json:"status"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// OfflinePresenceProvider 是 M2.1-B 的占位实现；M2.1-C 将替换为 Redis provider。
type OfflinePresenceProvider struct{}

func (OfflinePresenceProvider) Statuses(_ context.Context, ids []uint) (map[uint]PresenceStatus, error) {
	result := make(map[uint]PresenceStatus, len(ids))
	for _, id := range ids {
		result[id] = PresenceOffline
	}
	return result, nil
}

type PublicUser struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url"`
}

type RelationshipSummary struct {
	ID        uint                   `json:"id"`
	Status    model.FriendshipStatus `json:"status"`
	Direction string                 `json:"direction,omitempty"`
}

type UserSearchItem struct {
	User       PublicUser           `json:"user"`
	Friendship *RelationshipSummary `json:"friendship"`
}

type FriendRequestItem struct {
	ID          uint                   `json:"id"`
	Status      model.FriendshipStatus `json:"status"`
	Direction   string                 `json:"direction"`
	RequestedBy uint                   `json:"requested_by"`
	Peer        PublicUser             `json:"peer"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	RespondedAt *time.Time             `json:"responded_at"`
}

type FriendItem struct {
	ID        uint           `json:"id"`
	Peer      PublicUser     `json:"peer"`
	Presence  PresenceStatus `json:"presence"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type FriendRequestPage struct {
	Items      []FriendRequestItem `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type FriendPage struct {
	Items      []FriendItem `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type FriendService struct {
	friends   FriendRepository
	users     FriendUserRepository
	presence  PresenceProvider
	publisher FriendshipPublisher
	now       func() time.Time
}

func NewFriendService(friends FriendRepository, users FriendUserRepository, presence PresenceProvider, publisher FriendshipPublisher) *FriendService {
	if presence == nil {
		presence = OfflinePresenceProvider{}
	}
	return &FriendService{friends: friends, users: users, presence: presence, publisher: publisher, now: time.Now}
}

func (s *FriendService) SearchUsers(ctx context.Context, currentUserID uint, keyword string) ([]UserSearchItem, error) {
	keyword = strings.TrimSpace(keyword)
	if currentUserID == 0 || keyword == "" || len([]rune(keyword)) > 50 {
		return nil, ErrInvalidFriendRequest
	}
	users, err := s.users.SearchActive(ctx, currentUserID, keyword)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	ids := make([]uint, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.ID)
	}
	relationships, err := s.friends.FindRelationshipsWithPeers(ctx, currentUserID, ids)
	if err != nil {
		return nil, fmt.Errorf("load search relationships: %w", err)
	}
	byPeer := make(map[uint]model.Friendship, len(relationships))
	for _, friendship := range relationships {
		if friendship.Status != model.FriendshipStatusRemoved {
			byPeer[friendship.PeerID(currentUserID)] = friendship
		}
	}
	items := make([]UserSearchItem, 0, len(users))
	for _, user := range users {
		item := UserSearchItem{User: publicUser(user)}
		if friendship, ok := byPeer[user.ID]; ok {
			direction := ""
			if friendship.Status == model.FriendshipStatusPending {
				direction = relationshipDirection(friendship, currentUserID)
			}
			item.Friendship = &RelationshipSummary{ID: friendship.ID, Status: friendship.Status, Direction: direction}
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *FriendService) SendRequest(ctx context.Context, currentUserID, targetUserID uint) (*FriendRequestItem, error) {
	if currentUserID == 0 || targetUserID == 0 {
		return nil, ErrInvalidFriendRequest
	}
	if currentUserID == targetUserID {
		return nil, ErrCannotFriendSelf
	}
	if _, err := s.users.FindActiveByID(ctx, targetUserID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFriendTargetNotFound
		}
		return nil, fmt.Errorf("find friend target: %w", err)
	}
	low, high := normalizedPair(currentUserID, targetUserID)
	now := s.now().UTC()
	initial := &model.Friendship{UserLowID: low, UserHighID: high, RequestedBy: currentUserID, Status: model.FriendshipStatusPending}
	friendship, changed, err := s.friends.MutatePair(ctx, low, high, initial, func(existing *model.Friendship) (bool, error) {
		switch existing.Status {
		case model.FriendshipStatusPending:
			if existing.RequestedBy == currentUserID {
				return false, nil
			}
			existing.Status = model.FriendshipStatusAccepted
			existing.RespondedAt = &now
			existing.RemovedAt = nil
			return true, nil
		case model.FriendshipStatusAccepted:
			return false, nil
		case model.FriendshipStatusRejected, model.FriendshipStatusRemoved:
			existing.Status = model.FriendshipStatusPending
			existing.RequestedBy = currentUserID
			existing.RespondedAt = nil
			existing.RemovedAt = nil
			return true, nil
		default:
			return false, ErrFriendStateConflict
		}
	})
	if err != nil {
		return nil, fmt.Errorf("send friend request: %w", err)
	}
	s.publish(ctx, friendship, changed)
	peer, err := s.users.FindActiveByID(ctx, friendship.PeerID(currentUserID))
	if err != nil {
		return nil, fmt.Errorf("load friend request peer: %w", err)
	}
	item := requestItem(*friendship, currentUserID, *peer)
	return &item, nil
}

func (s *FriendService) RespondRequest(ctx context.Context, currentUserID, requestID uint, accept bool) (*FriendRequestItem, error) {
	if currentUserID == 0 || requestID == 0 {
		return nil, ErrInvalidFriendRequest
	}
	now := s.now().UTC()
	desired := model.FriendshipStatusRejected
	if accept {
		desired = model.FriendshipStatusAccepted
	}
	friendship, changed, err := s.friends.MutateByID(ctx, requestID, func(existing *model.Friendship) (bool, error) {
		if !existing.Includes(currentUserID) {
			return false, ErrFriendRequestMissing
		}
		if existing.RequestedBy == currentUserID {
			return false, ErrFriendForbidden
		}
		if existing.Status == desired {
			return false, nil
		}
		if existing.Status != model.FriendshipStatusPending {
			return false, ErrFriendStateConflict
		}
		existing.Status = desired
		existing.RespondedAt = &now
		existing.RemovedAt = nil
		return true, nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFriendRequestMissing
		}
		if errors.Is(err, ErrFriendRequestMissing) || errors.Is(err, ErrFriendForbidden) || errors.Is(err, ErrFriendStateConflict) {
			return nil, err
		}
		return nil, fmt.Errorf("respond friend request: %w", err)
	}
	s.publish(ctx, friendship, changed)
	peer, err := s.users.FindActiveByID(ctx, friendship.PeerID(currentUserID))
	if err != nil {
		return nil, fmt.Errorf("load friend request peer: %w", err)
	}
	item := requestItem(*friendship, currentUserID, *peer)
	return &item, nil
}

func (s *FriendService) DeleteFriend(ctx context.Context, currentUserID, friendUserID uint) error {
	if currentUserID == 0 || friendUserID == 0 || currentUserID == friendUserID {
		return ErrInvalidFriendRequest
	}
	low, high := normalizedPair(currentUserID, friendUserID)
	now := s.now().UTC()
	friendship, changed, err := s.friends.MutatePair(ctx, low, high, nil, func(existing *model.Friendship) (bool, error) {
		switch existing.Status {
		case model.FriendshipStatusAccepted:
			existing.Status = model.FriendshipStatusRemoved
			existing.RemovedAt = &now
			return true, nil
		case model.FriendshipStatusRemoved:
			return false, nil
		default:
			return false, ErrFriendshipNotFound
		}
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrFriendshipNotFound) {
			return ErrFriendshipNotFound
		}
		return fmt.Errorf("delete friend: %w", err)
	}
	s.publish(ctx, friendship, changed)
	return nil
}

func (s *FriendService) ListRequests(ctx context.Context, currentUserID uint, direction string, status model.FriendshipStatus, cursor uint, limit int) (*FriendRequestPage, error) {
	if currentUserID == 0 || (direction != "incoming" && direction != "outgoing") ||
		(status != model.FriendshipStatusPending && status != model.FriendshipStatusRejected) || limit < 1 || limit > 100 {
		return nil, ErrInvalidFriendQuery
	}
	rows, err := s.friends.ListRequests(ctx, currentUserID, direction == "incoming", status, cursor, limit+1)
	if err != nil {
		return nil, fmt.Errorf("list friend requests: %w", err)
	}
	next := ""
	if len(rows) > limit {
		next = strconv.FormatUint(uint64(rows[limit-1].ID), 10)
		rows = rows[:limit]
	}
	users, err := s.loadPeers(ctx, currentUserID, rows)
	if err != nil {
		return nil, err
	}
	items := make([]FriendRequestItem, 0, len(rows))
	for _, row := range rows {
		peer, ok := users[row.PeerID(currentUserID)]
		if ok {
			items = append(items, requestItem(row, currentUserID, peer))
		}
	}
	return &FriendRequestPage{Items: items, NextCursor: next}, nil
}

func (s *FriendService) ListFriends(ctx context.Context, currentUserID, cursor uint, limit int) (*FriendPage, error) {
	if currentUserID == 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalidFriendQuery
	}
	rows, err := s.friends.ListFriends(ctx, currentUserID, cursor, limit+1)
	if err != nil {
		return nil, fmt.Errorf("list friends: %w", err)
	}
	next := ""
	if len(rows) > limit {
		next = strconv.FormatUint(uint64(rows[limit-1].ID), 10)
		rows = rows[:limit]
	}
	users, err := s.loadPeers(ctx, currentUserID, rows)
	if err != nil {
		return nil, err
	}
	peerIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		peerIDs = append(peerIDs, row.PeerID(currentUserID))
	}
	statuses, statusErr := s.presence.Statuses(ctx, peerIDs)
	if statusErr != nil {
		statuses = map[uint]PresenceStatus{}
	}
	items := make([]FriendItem, 0, len(rows))
	for _, row := range rows {
		peerID := row.PeerID(currentUserID)
		peer, ok := users[peerID]
		if !ok {
			continue
		}
		presence := statuses[peerID]
		if presence == "" || statusErr != nil {
			presence = PresenceUnknown
		}
		items = append(items, FriendItem{ID: row.ID, Peer: publicUser(peer), Presence: presence, UpdatedAt: row.UpdatedAt})
	}
	return &FriendPage{Items: items, NextCursor: next}, nil
}

func (s *FriendService) loadPeers(ctx context.Context, userID uint, rows []model.Friendship) (map[uint]model.User, error) {
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.PeerID(userID))
	}
	users, err := s.users.FindActiveByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load friend peers: %w", err)
	}
	result := make(map[uint]model.User, len(users))
	for _, user := range users {
		result[user.ID] = user
	}
	return result, nil
}

func (s *FriendService) publish(ctx context.Context, friendship *model.Friendship, changed bool) {
	if !changed || s.publisher == nil || friendship == nil {
		return
	}
	err := s.publisher.PublishFriendshipUpdated(ctx, FriendshipUpdatedEvent{
		FriendshipID: friendship.ID, UserLowID: friendship.UserLowID, UserHighID: friendship.UserHighID,
		Status: friendship.Status, UpdatedAt: friendship.UpdatedAt,
	})
	if err != nil {
		log.Printf("publish friendship update: %v", err)
	}
}

func normalizedPair(a, b uint) (uint, uint) {
	if a < b {
		return a, b
	}
	return b, a
}

func publicUser(user model.User) PublicUser {
	return PublicUser{ID: user.ID, Username: user.Username, Nickname: user.Nickname, AvatarURL: user.AvatarURL}
}

func relationshipDirection(friendship model.Friendship, userID uint) string {
	if friendship.Status == model.FriendshipStatusAccepted {
		return "friend"
	}
	if friendship.RequestedBy == userID {
		return "outgoing"
	}
	return "incoming"
}

func requestItem(friendship model.Friendship, userID uint, peer model.User) FriendRequestItem {
	return FriendRequestItem{
		ID: friendship.ID, Status: friendship.Status, Direction: relationshipDirection(friendship, userID),
		RequestedBy: friendship.RequestedBy, Peer: publicUser(peer), CreatedAt: friendship.CreatedAt,
		UpdatedAt: friendship.UpdatedAt, RespondedAt: friendship.RespondedAt,
	}
}
