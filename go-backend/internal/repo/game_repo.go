package repo

import (
	"gorm.io/gorm"

	"trpggame/internal/model"
)

// GameRepo 游戏数据访问层
type GameRepo struct {
	db *gorm.DB
}

// NewGameRepo 创建 GameRepo
func NewGameRepo(db *gorm.DB) *GameRepo {
	return &GameRepo{db: db}
}

// CreateRoom 创建游戏房间
func (r *GameRepo) CreateRoom(room *model.GameRoom) error {
	return r.db.Create(room).Error
}

// FindRoomByID 按 ID 查询房间
func (r *GameRepo) FindRoomByID(id uint) (*model.GameRoom, error) {
	var room model.GameRoom
	err := r.db.First(&room, id).Error
	if err != nil {
		return nil, err
	}
	return &room, nil
}

// UpdateRoomStatus 更新房间状态
func (r *GameRepo) UpdateRoomStatus(id uint, status model.RoomStatus) error {
	return r.db.Model(&model.GameRoom{}).Where("id = ?", id).Update("status", status).Error
}

// AddPlayer 添加玩家到房间
func (r *GameRepo) AddPlayer(player *model.RoomPlayer) error {
	return r.db.Create(player).Error
}

// RemovePlayer 从房间移除玩家
func (r *GameRepo) RemovePlayer(roomID, userID uint) error {
	return r.db.Where("room_id = ? AND user_id = ?", roomID, userID).Delete(&model.RoomPlayer{}).Error
}

// FindPlayersByRoom 查询房间内所有玩家
func (r *GameRepo) FindPlayersByRoom(roomID uint) ([]model.RoomPlayer, error) {
	var players []model.RoomPlayer
	err := r.db.Where("room_id = ?", roomID).Find(&players).Error
	return players, err
}

// SaveGame 创建存档
func (r *GameRepo) SaveGame(save *model.GameSave) error {
	return r.db.Create(save).Error
}

// ListSaves 列出房间存档
func (r *GameRepo) ListSaves(roomID uint) ([]model.GameSave, error) {
	var saves []model.GameSave
	err := r.db.Where("room_id = ?", roomID).Order("created_at DESC").Find(&saves).Error
	return saves, err
}

// FindSaveByID 按 ID 查询存档
func (r *GameRepo) FindSaveByID(id uint) (*model.GameSave, error) {
	var save model.GameSave
	err := r.db.First(&save, id).Error
	if err != nil {
		return nil, err
	}
	return &save, nil
}
