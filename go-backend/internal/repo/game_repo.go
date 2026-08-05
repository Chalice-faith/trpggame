package repo

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"trpggame/internal/model"
)

var (
	// ErrInvalidRoomStatus 防止非法状态进入持久化更新。
	ErrInvalidRoomStatus = errors.New("invalid room status")
	// ErrEmptySourceStatuses 防止无来源状态约束的非原子状态更新。
	ErrEmptySourceStatuses = errors.New("source room statuses must not be empty")
)

// GameRepo 游戏房间、玩家与存档的数据访问层。
type GameRepo struct {
	db *gorm.DB
}

// NewGameRepo 创建 GameRepo。
func NewGameRepo(db *gorm.DB) *GameRepo {
	return &GameRepo{db: db}
}

// CreateRoomWithPlayer 在同一事务中创建房间及首位玩家。
func (r *GameRepo) CreateRoomWithPlayer(
	ctx context.Context,
	room *model.GameRoom,
	player *model.RoomPlayer,
) error {
	originalRoomID := room.ID
	originalPlayerID := player.ID
	originalPlayerRoomID := player.RoomID

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(room).Error; err != nil {
			return err
		}
		player.RoomID = room.ID
		return tx.Create(player).Error
	})
	if err != nil {
		room.ID = originalRoomID
		player.ID = originalPlayerID
		player.RoomID = originalPlayerRoomID
	}
	return err
}

// FindRoomByID 按 ID 查询房间。
func (r *GameRepo) FindRoomByID(
	ctx context.Context,
	id uint,
) (*model.GameRoom, error) {
	var room model.GameRoom
	err := r.db.WithContext(ctx).First(&room, id).Error
	if err != nil {
		return nil, err
	}
	return &room, nil
}

// FindRoomByIDAndOwnerID 按房间及所有者查询，避免跨用户访问。
func (r *GameRepo) FindRoomByIDAndOwnerID(
	ctx context.Context,
	id uint,
	ownerID uint,
) (*model.GameRoom, error) {
	var room model.GameRoom
	err := r.db.WithContext(ctx).
		Where("id = ? AND owner_id = ?", id, ownerID).
		First(&room).Error
	if err != nil {
		return nil, err
	}
	return &room, nil
}

// TransitionRoomStatus 使用来源状态集合进行原子比较更新。
func (r *GameRepo) TransitionRoomStatus(
	ctx context.Context,
	roomID uint,
	ownerID uint,
	from []model.RoomStatus,
	to model.RoomStatus,
) (bool, error) {
	if !to.Valid() {
		return false, ErrInvalidRoomStatus
	}
	if len(from) == 0 {
		return false, ErrEmptySourceStatuses
	}
	for _, status := range from {
		if !status.Valid() {
			return false, ErrInvalidRoomStatus
		}
	}

	updates := map[string]any{"status": to}
	if to == model.RoomStatusEnded {
		updates["ended_at"] = time.Now().UTC()
	}
	result := r.db.WithContext(ctx).
		Model(&model.GameRoom{}).
		Where("id = ? AND owner_id = ? AND status IN ?", roomID, ownerID, from).
		Updates(updates)
	return result.RowsAffected == 1, result.Error
}

// AddPlayer 添加玩家到已有房间。
func (r *GameRepo) AddPlayer(
	ctx context.Context,
	player *model.RoomPlayer,
) error {
	return r.db.WithContext(ctx).Create(player).Error
}

// RemovePlayer 从指定房间移除玩家。
func (r *GameRepo) RemovePlayer(
	ctx context.Context,
	roomID uint,
	userID uint,
) (bool, error) {
	result := r.db.WithContext(ctx).
		Where("room_id = ? AND user_id = ?", roomID, userID).
		Delete(&model.RoomPlayer{})
	return result.RowsAffected == 1, result.Error
}

// FindPlayer 查询玩家在指定房间中的关联记录。
func (r *GameRepo) FindPlayer(
	ctx context.Context,
	roomID uint,
	userID uint,
) (*model.RoomPlayer, error) {
	var player model.RoomPlayer
	err := r.db.WithContext(ctx).
		Where("room_id = ? AND user_id = ?", roomID, userID).
		First(&player).Error
	if err != nil {
		return nil, err
	}
	return &player, nil
}

// FindPlayersByRoom 按行动顺序查询房间玩家。
func (r *GameRepo) FindPlayersByRoom(
	ctx context.Context,
	roomID uint,
) ([]model.RoomPlayer, error) {
	players := make([]model.RoomPlayer, 0)
	err := r.db.WithContext(ctx).
		Where("room_id = ?", roomID).
		Order("player_order ASC, id ASC").
		Find(&players).Error
	return players, err
}

// CreateSave 创建房间存档。
func (r *GameRepo) CreateSave(
	ctx context.Context,
	save *model.GameSave,
) error {
	return r.db.WithContext(ctx).Create(save).Error
}

// ListSaves 列出房间存档，创建时间相同时按 ID 逆序稳定排序。
func (r *GameRepo) ListSaves(
	ctx context.Context,
	roomID uint,
) ([]model.GameSave, error) {
	saves := make([]model.GameSave, 0)
	err := r.db.WithContext(ctx).
		Where("room_id = ?", roomID).
		Order("created_at DESC, id DESC").
		Find(&saves).Error
	return saves, err
}

// FindSaveByID 在房间范围内查询存档，避免跨房间加载。
func (r *GameRepo) FindSaveByID(
	ctx context.Context,
	roomID uint,
	saveID uint,
) (*model.GameSave, error) {
	var save model.GameSave
	err := r.db.WithContext(ctx).
		Where("id = ? AND room_id = ?", saveID, roomID).
		First(&save).Error
	if err != nil {
		return nil, err
	}
	return &save, nil
}

// DeleteSave 在房间范围内删除存档。
func (r *GameRepo) DeleteSave(
	ctx context.Context,
	roomID uint,
	saveID uint,
) (bool, error) {
	result := r.db.WithContext(ctx).
		Where("id = ? AND room_id = ?", saveID, roomID).
		Delete(&model.GameSave{})
	return result.RowsAffected == 1, result.Error
}
