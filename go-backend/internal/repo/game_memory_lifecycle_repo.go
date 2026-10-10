package repo

import (
	"context"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
	"trpggame/internal/model"
)

func (r *GameMemoryRepo) FindOperation(ctx context.Context, roomID uint, id string) (*model.GameMemoryOperation, error) {
	if roomID == 0 || !model.ValidMemoryUUID(id) {
		return nil, model.ErrInvalidMemoryData
	}
	var op model.GameMemoryOperation
	err := r.db.WithContext(ctx).Where("room_id = ? AND operation_id = ?", roomID, id).First(&op).Error
	return &op, err
}

func (r *GameMemoryRepo) FindTimeline(ctx context.Context, roomID uint, id string) (*model.GameTimeline, error) {
	return findTimeline(r.db.WithContext(ctx), roomID, id, false)
}

// Recovery may safely pause a start while its Redis publication token is held.
// It cannot pause a newer branch or a room occupied by another operation.
func (r *GameMemoryRepo) PauseOperationRoom(ctx context.Context, roomID uint, id string, revision uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var room model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&room, roomID).Error; err != nil {
			return err
		}
		var op model.GameMemoryOperation
		if err := tx.Where("room_id = ? AND operation_id = ?", roomID, id).First(&op).Error; err != nil {
			return err
		}
		var state model.GameMemoryState
		if err := tx.Where("room_id = ?", roomID).First(&state).Error; err != nil {
			return err
		}
		owned := state.ActiveOperationID != nil && *state.ActiveOperationID == id && (op.Phase == "prepared" || op.Phase == "redis_applied")
		published := op.Phase == "completed" && state.Status == "ready" && state.ActiveOperationID == nil && state.ActiveTimelineID != nil && op.TargetTimelineID != nil && *state.ActiveTimelineID == *op.TargetTimelineID && state.Revision == revision
		if op.Kind != "start" || (!owned && !published) || (room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused) {
			return ErrMemoryBusy
		}
		return tx.Model(&room).Update("status", model.RoomStatusPaused).Error
	})
}

func (r *GameMemoryRepo) FindEndOperation(ctx context.Context, roomID uint, timeline string) (*model.GameMemoryOperation, error) {
	var op model.GameMemoryOperation
	err := r.db.WithContext(ctx).Where("room_id = ? AND kind = 'end' AND source_timeline_id = ?", roomID, timeline).Order("created_at DESC").First(&op).Error
	return &op, err
}

// Completed MySQL publication still needs Redis unlock/cleanup. It remains due
// until an exact Redis receipt is verified. No additional schema is needed.
func (r *GameMemoryRepo) FinalizeOperation(ctx context.Context, roomID uint, id, hash string) error {
	if !model.ValidMemoryUUID(id) || !model.ValidMemoryHash(hash) {
		return model.ErrInvalidMemoryData
	}
	result := r.db.WithContext(ctx).Model(&model.GameMemoryOperation{}).Where("room_id = ? AND operation_id = ? AND phase = 'completed' AND snapshot_hash = ?", roomID, id, hash).
		Updates(map[string]any{"next_retry_at": time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), "error_class": "", "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		op, err := r.FindOperation(ctx, roomID, id)
		if err != nil {
			return err
		}
		if op.Phase != "completed" || op.SnapshotHash != hash {
			return ErrMemoryConflict
		}
	}
	return nil
}

// Manual saves require a paused, current capability under the same room lock as
// load/start/end publication. A late capture cannot persist after branch replacement.
func (r *GameRepo) CreateCurrentMemorySave(ctx context.Context, save *model.GameSave, revision uint64) error {
	if save == nil || save.TimelineID == nil || save.IsAuto || revision == 0 {
		return model.ErrInvalidMemoryData
	}
	if memory, err := validateMemorySave(save); err != nil || !memory {
		return model.ErrInvalidMemoryData
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var room model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&room, save.RoomID).Error; err != nil {
			return err
		}
		var state model.GameMemoryState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("room_id = ?", save.RoomID).First(&state).Error; err != nil {
			return err
		}
		if room.Status != model.RoomStatusPaused || state.Status != "ready" || state.Revision != revision || state.ActiveOperationID != nil ||
			state.ActiveTimelineID == nil || *state.ActiveTimelineID != *save.TimelineID {
			return ErrMemoryBusy
		}
		_, err := (&GameRepo{db: tx}).createMemorySave(ctx, save)
		return err
	})
}

// Serialize status changes with control publication; an old branch cannot resume.
func (r *GameRepo) TransitionMemoryRoomStatus(ctx context.Context, roomID, ownerID uint, timeline string, revision uint64, to model.RoomStatus) error {
	if !model.ValidMemoryUUID(timeline) || revision == 0 || (to != model.RoomStatusPlaying && to != model.RoomStatusPaused) {
		return model.ErrInvalidMemoryData
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var room model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&room, roomID).Error; err != nil {
			return err
		}
		var state model.GameMemoryState
		if err := tx.Where("room_id = ?", roomID).First(&state).Error; err != nil {
			return err
		}
		if room.OwnerID != ownerID || (room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused) || state.Status != "ready" || state.ActiveOperationID != nil || state.ActiveTimelineID == nil || *state.ActiveTimelineID != timeline || state.Revision != revision {
			return ErrMemoryBusy
		}
		return tx.Model(&room).Update("status", to).Error
	})
}
