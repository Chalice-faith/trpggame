package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"trpggame/internal/model"
)

var (
	ErrRoomMissing                = errors.New("room missing or inaccessible")
	ErrRoomScriptUnavailable      = errors.New("room script unavailable")
	ErrRoomCharactersInsufficient = errors.New("not enough script characters")
	ErrRoomClosed                 = errors.New("room is not waiting")
	ErrRoomFull                   = errors.New("room is full")
	ErrRoomRemoved                = errors.New("removed player cannot rejoin")
	ErrRoomVersionConflict        = errors.New("room version conflict")
	errRoomNoChange               = errors.New("room mutation has no change")
)

// RoomMemberRecord 将有效成员和公开用户资料一起返回给大厅。
type RoomMemberRecord struct {
	Player model.RoomPlayer
	User   model.User
}

// RoomRecord 是 MySQL 权威大厅快照；候选角色和成员在同一次读取中组装。
type RoomRecord struct {
	Room           model.GameRoom
	Members        []RoomMemberRecord
	Characters     []model.ScriptCharacter
	Mutation       string
	AffectedUserID uint
}

type RoomRepo struct{ db *gorm.DB }

func NewRoomRepo(db *gorm.DB) *RoomRepo { return &RoomRepo{db: db} }

func (r *RoomRepo) Create(ctx context.Context, ownerID, scriptID uint, name, code string, capacity int, now time.Time) (*RoomRecord, error) {
	var roomID uint
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var script model.Script
		if err := tx.Where("id = ? AND user_id = ? AND status = ? AND deleted_at IS NULL", scriptID, ownerID, model.ScriptStatusReady).First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoomScriptUnavailable
			}
			return err
		}
		var characterCount int64
		if err := tx.Model(&model.ScriptCharacter{}).Where("script_id = ?", scriptID).Count(&characterCount).Error; err != nil {
			return err
		}
		if characterCount < int64(capacity) {
			return ErrRoomCharactersInsufficient
		}
		room := model.GameRoom{
			Name: name, ScriptID: scriptID, OwnerID: ownerID, Status: model.RoomStatusWaiting,
			MaxPlayers: capacity, TurnOrder: []byte("[]"), IsSolo: false, RoomCode: &code,
			Version: 1, TurnTimeoutSeconds: 120, CreatedAt: now,
		}
		if err := tx.Create(&room).Error; err != nil {
			return err
		}
		roomID = room.ID
		owner := model.RoomPlayer{RoomID: room.ID, UserID: ownerID, PlayerOrder: 0,
			Status: model.RoomPlayerStatusActive, JoinedAt: now}
		return tx.Create(&owner).Error
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, ownerID, roomID)
}

func (r *RoomRepo) List(ctx context.Context, userID uint) ([]model.GameRoom, error) {
	rooms := make([]model.GameRoom, 0)
	err := r.db.WithContext(ctx).Model(&model.GameRoom{}).
		Joins("JOIN room_players me ON me.room_id = game_rooms.id AND me.user_id = ? AND me.status = ?", userID, model.RoomPlayerStatusActive).
		Where("game_rooms.is_solo = ?", false).Order("game_rooms.id DESC").Limit(100).Find(&rooms).Error
	return rooms, err
}

func (r *RoomRepo) Get(ctx context.Context, userID, roomID uint) (*RoomRecord, error) {
	if userID == 0 || roomID == 0 {
		return nil, ErrRoomMissing
	}
	return r.snapshot(ctx, userID, roomID)
}

// AuthorizeSubscription keeps solo subscriptions owner-only and admits only active multiplayer members.
// The returned flag identifies a solo room so callers can preserve its existing protocol.
func (r *RoomRepo) AuthorizeSubscription(ctx context.Context, userID, roomID uint) (bool, error) {
	if userID == 0 || roomID == 0 {
		return false, ErrRoomMissing
	}
	var room model.GameRoom
	if err := r.db.WithContext(ctx).Select("id", "owner_id", "is_solo").Where("id = ?", roomID).First(&room).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrRoomMissing
		}
		return false, err
	}
	if room.IsSolo {
		if room.OwnerID != userID {
			return true, ErrRoomMissing
		}
		return true, nil
	}
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.RoomPlayer{}).
		Where("room_id = ? AND user_id = ? AND status = ?", roomID, userID, model.RoomPlayerStatusActive).
		Count(&count).Error; err != nil {
		return false, err
	}
	if count != 1 {
		return false, ErrRoomMissing
	}
	return false, nil
}

func (r *RoomRepo) snapshot(ctx context.Context, userID, roomID uint) (*RoomRecord, error) {
	var record *RoomRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		record, err = r.get(tx, userID, roomID)
		return err
	})
	return record, err
}

func (r *RoomRepo) get(tx *gorm.DB, userID, roomID uint) (*RoomRecord, error) {
	var room model.GameRoom
	if err := tx.Where("id = ? AND is_solo = ?", roomID, false).First(&room).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRoomMissing
		}
		return nil, err
	}
	if userID != 0 {
		var current model.RoomPlayer
		if err := tx.Where("room_id = ? AND user_id = ? AND status = ?", roomID, userID, model.RoomPlayerStatusActive).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrRoomMissing
			}
			return nil, err
		}
	}
	var players []model.RoomPlayer
	if err := tx.Where("room_id = ? AND status = ?", roomID, model.RoomPlayerStatusActive).
		Order("player_order ASC, id ASC").Find(&players).Error; err != nil {
		return nil, err
	}
	userIDs := make([]uint, 0, len(players))
	for _, player := range players {
		userIDs = append(userIDs, player.UserID)
	}
	var users []model.User
	if err := tx.Where("id IN ? AND deleted_at IS NULL", userIDs).Find(&users).Error; err != nil {
		return nil, err
	}
	usersByID := make(map[uint]model.User, len(users))
	for _, user := range users {
		usersByID[user.ID] = user
	}
	members := make([]RoomMemberRecord, 0, len(players))
	for _, player := range players {
		members = append(members, RoomMemberRecord{Player: player, User: usersByID[player.UserID]})
	}
	var characters []model.ScriptCharacter
	if err := tx.Where("script_id = ?", room.ScriptID).Order("id ASC").Find(&characters).Error; err != nil {
		return nil, err
	}
	return &RoomRecord{Room: room, Members: members, Characters: characters}, nil
}

type RoomVersionError struct{ Current uint64 }

func (e *RoomVersionError) Error() string {
	return fmt.Sprintf("room version conflict: current=%d", e.Current)
}
func (e *RoomVersionError) Is(target error) bool { return target == ErrRoomVersionConflict }

func (r *RoomRepo) mutate(ctx context.Context, actorID, roomID uint, expectedVersion uint64, mutation string, affectedUserID uint, change func(*gorm.DB, *model.GameRoom, model.RoomPlayer) error) (*RoomRecord, error) {
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var room model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_solo = ?", roomID, false).First(&room).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoomMissing
			}
			return err
		}
		var actor model.RoomPlayer
		if err := tx.Where("room_id = ? AND user_id = ? AND status = ?", roomID, actorID, model.RoomPlayerStatusActive).First(&actor).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoomMissing
			}
			return err
		}
		if room.Version != expectedVersion {
			return &RoomVersionError{Current: room.Version}
		}
		if room.Status != model.RoomStatusWaiting {
			return ErrRoomClosed
		}
		if err := change(tx, &room, actor); err != nil {
			if errors.Is(err, errRoomNoChange) {
				return nil
			}
			return err
		}
		changed = true
		return tx.Model(&room).Update("version", gorm.Expr("version + 1")).Error
	})
	if err != nil {
		return nil, err
	}
	record, err := r.snapshot(ctx, 0, roomID)
	if err == nil && changed {
		record.Mutation = mutation
		record.AffectedUserID = affectedUserID
	}
	return record, err
}

var (
	ErrRoomPermission       = errors.New("room permission denied")
	ErrRoomInvalidCharacter = errors.New("character not in room script")
	ErrRoomCharacterTaken   = errors.New("character already selected")
	ErrRoomStartConditions  = errors.New("room start conditions unmet")
	ErrRoomOwnerLeave       = errors.New("owner must transfer before leaving")
)

func (r *RoomRepo) SelectCharacter(ctx context.Context, actorID, roomID, characterID uint, expectedVersion uint64) (*RoomRecord, error) {
	return r.mutate(ctx, actorID, roomID, expectedVersion, "room_character_selected", actorID, func(tx *gorm.DB, room *model.GameRoom, actor model.RoomPlayer) error {
		var character model.ScriptCharacter
		if err := tx.Where("id = ? AND script_id = ?", characterID, room.ScriptID).First(&character).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoomInvalidCharacter
			}
			return err
		}
		if actor.CharacterID != nil && *actor.CharacterID == characterID {
			return errRoomNoChange
		}
		if err := tx.Model(&actor).Updates(map[string]any{"character_id": characterID, "is_ready": false}).Error; err != nil {
			var mysqlErr *mysql.MySQLError
			if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
				return ErrRoomCharacterTaken
			}
			return err
		}
		return nil
	})
}

func (r *RoomRepo) SetReady(ctx context.Context, actorID, roomID uint, ready bool, expectedVersion uint64) (*RoomRecord, error) {
	return r.mutate(ctx, actorID, roomID, expectedVersion, "room_ready_changed", actorID, func(tx *gorm.DB, room *model.GameRoom, actor model.RoomPlayer) error {
		if ready && actor.CharacterID == nil {
			return ErrRoomStartConditions
		}
		if actor.IsReady == ready {
			return errRoomNoChange
		}
		return tx.Model(&actor).Update("is_ready", ready).Error
	})
}

func (r *RoomRepo) Leave(ctx context.Context, actorID, roomID uint, expectedVersion uint64, now time.Time) (*RoomRecord, error) {
	return r.mutate(ctx, actorID, roomID, expectedVersion, "room_member_left", actorID, func(tx *gorm.DB, room *model.GameRoom, actor model.RoomPlayer) error {
		if room.OwnerID == actorID {
			return ErrRoomOwnerLeave
		}
		if err := tx.Model(&actor).Updates(map[string]any{"status": model.RoomPlayerStatusLeft, "left_at": now, "character_id": nil, "is_ready": false}).Error; err != nil {
			return err
		}
		return tx.Model(&model.RoomPlayer{}).Where("room_id = ? AND status = ?", roomID, model.RoomPlayerStatusActive).Update("is_ready", false).Error
	})
}

func (r *RoomRepo) Remove(ctx context.Context, actorID, roomID, targetID uint, expectedVersion uint64, now time.Time) (*RoomRecord, error) {
	return r.mutate(ctx, actorID, roomID, expectedVersion, "room_member_left", targetID, func(tx *gorm.DB, room *model.GameRoom, actor model.RoomPlayer) error {
		if room.OwnerID != actorID || targetID == actorID {
			return ErrRoomPermission
		}
		var target model.RoomPlayer
		if err := tx.Where("room_id = ? AND user_id = ? AND status = ?", roomID, targetID, model.RoomPlayerStatusActive).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoomMissing
			}
			return err
		}
		if err := tx.Model(&target).Updates(map[string]any{"status": model.RoomPlayerStatusRemoved, "left_at": now, "character_id": nil, "is_ready": false}).Error; err != nil {
			return err
		}
		return tx.Model(&model.RoomPlayer{}).Where("room_id = ? AND status = ?", roomID, model.RoomPlayerStatusActive).Update("is_ready", false).Error
	})
}

func (r *RoomRepo) Transfer(ctx context.Context, actorID, roomID, targetID uint, expectedVersion uint64) (*RoomRecord, error) {
	return r.mutate(ctx, actorID, roomID, expectedVersion, "room_snapshot", targetID, func(tx *gorm.DB, room *model.GameRoom, actor model.RoomPlayer) error {
		if room.OwnerID != actorID || targetID == actorID {
			return ErrRoomPermission
		}
		var target model.RoomPlayer
		if err := tx.Where("room_id = ? AND user_id = ? AND status = ?", roomID, targetID, model.RoomPlayerStatusActive).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoomMissing
			}
			return err
		}
		if err := tx.Model(room).Update("owner_id", targetID).Error; err != nil {
			return err
		}
		return tx.Model(&model.RoomPlayer{}).Where("room_id = ? AND status = ?", roomID, model.RoomPlayerStatusActive).Update("is_ready", false).Error
	})
}

func (r *RoomRepo) Start(ctx context.Context, actorID, roomID uint, expectedVersion uint64) (*RoomRecord, error) {
	return r.mutate(ctx, actorID, roomID, expectedVersion, "game_started", actorID, func(tx *gorm.DB, room *model.GameRoom, actor model.RoomPlayer) error {
		if room.OwnerID != actorID {
			return ErrRoomPermission
		}
		var players []model.RoomPlayer
		if err := tx.Where("room_id = ? AND status = ?", roomID, model.RoomPlayerStatusActive).Order("player_order ASC, id ASC").Find(&players).Error; err != nil {
			return err
		}
		if len(players) < 2 {
			return ErrRoomStartConditions
		}
		order := make([]uint, 0, len(players))
		seen := make(map[uint]bool, len(players))
		for _, player := range players {
			if !player.IsReady || player.CharacterID == nil || seen[*player.CharacterID] {
				return ErrRoomStartConditions
			}
			seen[*player.CharacterID] = true
			order = append(order, player.UserID)
		}
		encoded, err := json.Marshal(order)
		if err != nil {
			return err
		}
		return tx.Model(room).Updates(map[string]any{"status": model.RoomStatusPlaying, "turn_order": encoded}).Error
	})
}

func (r *RoomRepo) Join(ctx context.Context, userID uint, code string, now time.Time) (*RoomRecord, error) {
	var roomID uint
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var room model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("room_code = ? AND is_solo = ?", code, false).First(&room).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRoomMissing
			}
			return err
		}
		roomID = room.ID
		var player model.RoomPlayer
		err := tx.Where("room_id = ? AND user_id = ?", room.ID, userID).First(&player).Error
		if err == nil && player.Status == model.RoomPlayerStatusActive {
			return nil
		}
		if err == nil && player.Status == model.RoomPlayerStatusRemoved {
			return ErrRoomRemoved
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if room.Status != model.RoomStatusWaiting {
			return ErrRoomClosed
		}
		var count int64
		if err := tx.Model(&model.RoomPlayer{}).Where("room_id = ? AND status = ?", room.ID, model.RoomPlayerStatusActive).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(room.MaxPlayers) {
			return ErrRoomFull
		}
		var lastOrder int
		if err := tx.Model(&model.RoomPlayer{}).Where("room_id = ?", room.ID).Select("COALESCE(MAX(player_order), -1)").Scan(&lastOrder).Error; err != nil {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			player = model.RoomPlayer{RoomID: room.ID, UserID: userID, PlayerOrder: lastOrder + 1,
				Status: model.RoomPlayerStatusActive, JoinedAt: now}
			if err := tx.Create(&player).Error; err != nil {
				return err
			}
		} else {
			updates := map[string]any{"status": model.RoomPlayerStatusActive, "left_at": nil,
				"character_id": nil, "is_ready": false, "joined_at": now, "player_order": lastOrder + 1}
			if err := tx.Model(&player).Updates(updates).Error; err != nil {
				return err
			}
		}
		changed = true
		if err := tx.Model(&model.RoomPlayer{}).Where("room_id = ? AND status = ?", room.ID, model.RoomPlayerStatusActive).Update("is_ready", false).Error; err != nil {
			return err
		}
		return tx.Model(&room).Update("version", gorm.Expr("version + 1")).Error
	})
	if err != nil {
		return nil, err
	}
	record, err := r.Get(ctx, userID, roomID)
	if err == nil && changed {
		record.Mutation = "room_member_joined"
		record.AffectedUserID = userID
	}
	return record, err
}
