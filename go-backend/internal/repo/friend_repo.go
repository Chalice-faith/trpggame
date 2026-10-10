package repo

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"trpggame/internal/model"
)

// FriendshipMutation 在已加行锁的关系上计算状态变更。
type FriendshipMutation func(*model.Friendship) (bool, error)

// FriendRepo 好友关系数据访问层。
type FriendRepo struct{ db *gorm.DB }

func NewFriendRepo(db *gorm.DB) *FriendRepo { return &FriendRepo{db: db} }

// MutatePair 以唯一用户对为边界串行化创建及状态变更。
func (r *FriendRepo) MutatePair(ctx context.Context, lowID, highID uint, initial *model.Friendship, mutate FriendshipMutation) (*model.Friendship, bool, error) {
	var result model.Friendship
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created := false
		if initial != nil {
			candidate := *initial
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if res.Error != nil {
				return res.Error
			}
			created = res.RowsAffected == 1
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_low_id = ? AND user_high_id = ?", lowID, highID).
			First(&result).Error; err != nil {
			return err
		}
		if created {
			changed = true
			return nil
		}
		if mutate == nil {
			return nil
		}
		shouldSave, err := mutate(&result)
		if err != nil {
			return err
		}
		if !shouldSave {
			return nil
		}
		changed = true
		return tx.Save(&result).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &result, changed, nil
}

// MutateByID 锁定指定关系并执行状态变更。
func (r *FriendRepo) MutateByID(ctx context.Context, id uint, mutate FriendshipMutation) (*model.Friendship, bool, error) {
	var result model.Friendship
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&result, id).Error; err != nil {
			return err
		}
		shouldSave, err := mutate(&result)
		if err != nil {
			return err
		}
		if !shouldSave {
			return nil
		}
		changed = true
		return tx.Save(&result).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &result, changed, nil
}

func (r *FriendRepo) FindPair(ctx context.Context, lowID, highID uint) (*model.Friendship, error) {
	var friendship model.Friendship
	err := r.db.WithContext(ctx).Where("user_low_id = ? AND user_high_id = ?", lowID, highID).First(&friendship).Error
	return &friendship, err
}

func (r *FriendRepo) ListRequests(ctx context.Context, userID uint, incoming bool, status model.FriendshipStatus, cursor uint, limit int) ([]model.Friendship, error) {
	query := r.db.WithContext(ctx).Where("status = ?", status)
	if incoming {
		query = query.Where("requested_by <> ? AND (user_low_id = ? OR user_high_id = ?)", userID, userID, userID)
	} else {
		query = query.Where("requested_by = ?", userID)
	}
	if cursor > 0 {
		query = query.Where("id < ?", cursor)
	}
	var rows []model.Friendship
	return rows, query.Order("id DESC").Limit(limit).Find(&rows).Error
}

func (r *FriendRepo) ListFriends(ctx context.Context, userID, cursor uint, limit int) ([]model.Friendship, error) {
	query := r.db.WithContext(ctx).
		Where("status = ? AND (user_low_id = ? OR user_high_id = ?)", model.FriendshipStatusAccepted, userID, userID)
	if cursor > 0 {
		query = query.Where("id < ?", cursor)
	}
	var rows []model.Friendship
	return rows, query.Order("id DESC").Limit(limit).Find(&rows).Error
}

func (r *FriendRepo) FindRelationshipsWithPeers(ctx context.Context, userID uint, peerIDs []uint) ([]model.Friendship, error) {
	if len(peerIDs) == 0 {
		return []model.Friendship{}, nil
	}
	var rows []model.Friendship
	err := r.db.WithContext(ctx).
		Where("(user_low_id = ? AND user_high_id IN ?) OR (user_high_id = ? AND user_low_id IN ?)", userID, peerIDs, userID, peerIDs).
		Find(&rows).Error
	return rows, err
}

// ListAcceptedPeerIDs 返回当前用户所有已接受好友的用户 ID。
func (r *FriendRepo) ListAcceptedPeerIDs(ctx context.Context, userID uint) ([]uint, error) {
	var peerIDs []uint
	err := r.db.WithContext(ctx).Model(&model.Friendship{}).
		Select("CASE WHEN user_low_id = ? THEN user_high_id ELSE user_low_id END", userID).
		Where("status = ? AND (user_low_id = ? OR user_high_id = ?)", model.FriendshipStatusAccepted, userID, userID).
		Order("id").Scan(&peerIDs).Error
	return peerIDs, err
}
