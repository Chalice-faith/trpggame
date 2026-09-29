package repo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"trpggame/internal/model"
)

var (
	ErrMemoryConflict = errors.New("game memory immutable data conflict")
	ErrMemoryGap      = errors.New("game memory non-contiguous position")
	ErrMemoryBranch   = errors.New("invalid game memory branch")
	ErrMemoryBusy     = errors.New("game memory control operation conflict")
	ErrMemoryCursor   = errors.New("game memory cursor expired")
)

type GameMemoryRepo struct{ db *gorm.DB }

func NewGameMemoryRepo(db *gorm.DB) *GameMemoryRepo { return &GameMemoryRepo{db: db} }

func (r *GameMemoryRepo) FindState(ctx context.Context, roomID uint) (*model.GameMemoryState, error) {
	var state model.GameMemoryState
	err := r.db.WithContext(ctx).Where("room_id = ?", roomID).First(&state).Error
	return &state, err
}

// PrepareOperation persists a control intent and its branch together. It does not enable Redis.
// The caller must fence runtime writes before preparing load/end (A2/A4).
func (r *GameMemoryRepo) PrepareOperation(ctx context.Context, input *model.GameMemoryOperation, target *model.GameTimeline) (*model.GameMemoryOperation, error) {
	if input == nil {
		return nil, model.ErrInvalidMemoryData
	}
	op := *input
	if err := op.Seal(); err != nil {
		return nil, err
	}
	if (op.TargetTimelineID == nil) != (target == nil) {
		return nil, model.ErrInvalidMemoryData
	}
	if target != nil && (target.RoomID != op.RoomID || target.ID != *op.TargetTimelineID ||
		!model.ValidMemoryUUID(target.ID) || target.DurablePosition != target.ForkPosition ||
		(target.ParentID != nil && !model.ValidMemoryUUID(*target.ParentID)) ||
		!reflect.DeepEqual(target.OriginSaveID, op.TargetSaveID)) {
		return nil, model.ErrInvalidMemoryData
	}
	if op.Kind == "start" && (target.ParentID != nil || target.ForkPosition != 0 || !target.HistoryComplete) {
		return nil, model.ErrInvalidMemoryData
	}
	var result model.GameMemoryOperation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the existing room even on first start, where no memory-state row exists yet.
		var room model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&room, op.RoomID).Error; err != nil {
			return err
		}
		err := tx.Where("operation_id = ?", op.OperationID).First(&result).Error
		if err == nil {
			if !sameOperation(&result, &op) {
				return ErrMemoryConflict
			}
			if target != nil {
				var existing model.GameTimeline
				if err := tx.Where("room_id = ? AND id = ?", op.RoomID, target.ID).First(&existing).Error; err != nil {
					return err
				}
				if !sameTimeline(&existing, target) {
					return ErrMemoryConflict
				}
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var state model.GameMemoryState
		err = tx.Where("room_id = ?", op.RoomID).First(&state).Error
		if op.Kind == "start" {
			if err == nil {
				return ErrMemoryBusy
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			state = model.GameMemoryState{RoomID: op.RoomID, Status: "initializing", Revision: 1, ActiveOperationID: &op.OperationID, UpdatedAt: time.Now().UTC()}
			if err := tx.Create(&state).Error; err != nil {
				return err
			}
		} else {
			if err != nil {
				return err
			}
			if state.Status != "ready" || state.ActiveOperationID != nil || !reflect.DeepEqual(state.ActiveTimelineID, op.SourceTimelineID) {
				return ErrMemoryBusy
			}
			if err := tx.Model(&state).Updates(map[string]any{"status": "recovering", "active_operation_id": op.OperationID,
				"revision": gorm.Expr("revision + 1"), "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		if target != nil {
			if target.ParentID == nil {
				if target.ForkPosition != 0 || (op.Kind == "load" && target.HistoryComplete) {
					return ErrMemoryBranch
				}
			} else {
				segments, err := resolveVisiblePath(op.RoomID, *target.ParentID, target.ForkPosition, func(id string) (*model.GameTimeline, error) {
					return findTimeline(tx, op.RoomID, id, false)
				})
				if err != nil {
					return err
				}
				if len(segments) >= model.GameMemoryMaxDepth {
					return ErrMemoryBranch
				}
				parent, err := findTimeline(tx, op.RoomID, *target.ParentID, false)
				if err != nil {
					return err
				}
				if target.HistoryComplete != parent.HistoryComplete {
					return ErrMemoryBranch
				}
			}
			if op.Kind == "load" {
				var save model.GameSave
				if err := tx.Where("room_id = ? AND id = ?", op.RoomID, *op.TargetSaveID).First(&save).Error; err != nil {
					return err
				}
				if target.ParentID == nil {
					if save.TimelineID != nil || save.MemoryPosition != nil {
						return ErrMemoryBranch
					}
				} else if save.TimelineID == nil || save.MemoryPosition == nil || *save.TimelineID != *target.ParentID || *save.MemoryPosition != target.ForkPosition {
					return ErrMemoryBranch
				}
			}
			branch := *target
			branch.Status, branch.CreatedAt = "prepared", time.Now().UTC()
			if err := tx.Create(&branch).Error; err != nil {
				return err
			}
		}
		op.Phase = "prepared"
		op.Attempts, op.ErrorClass = 0, ""
		op.CreatedAt, op.UpdatedAt, op.NextRetryAt = time.Now().UTC(), time.Now().UTC(), time.Now().UTC()
		if err := tx.Create(&op).Error; err != nil {
			return err
		}
		result = op
		return nil
	})
	return &result, err
}

func sameOperation(a, b *model.GameMemoryOperation) bool {
	return a.RoomID == b.RoomID && a.Kind == b.Kind && a.Fingerprint == b.Fingerprint &&
		a.SourceGeneration == b.SourceGeneration && reflect.DeepEqual(a.SourceTimelineID, b.SourceTimelineID) &&
		reflect.DeepEqual(a.TargetTimelineID, b.TargetTimelineID) && reflect.DeepEqual(a.TargetSaveID, b.TargetSaveID) && a.SnapshotHash == b.SnapshotHash
}
func sameTimeline(a, b *model.GameTimeline) bool {
	return a.ID == b.ID && a.RoomID == b.RoomID && reflect.DeepEqual(a.ParentID, b.ParentID) &&
		a.ForkPosition == b.ForkPosition && reflect.DeepEqual(a.OriginSaveID, b.OriginSaveID) && a.HistoryComplete == b.HistoryComplete
}

// AdvanceOperation is a phase CAS; completion atomically publishes the durable branch.
// redis_applied must only be reported after verifying the corresponding Redis token.
func (r *GameMemoryRepo) AdvanceOperation(ctx context.Context, roomID uint, operationID, from, to string) error {
	allowed := (from == "prepared" && (to == "redis_applied" || to == "aborted" || to == "blocked")) ||
		(from == "redis_applied" && (to == "completed" || to == "blocked"))
	if roomID == 0 || !model.ValidMemoryUUID(operationID) || !allowed {
		return model.ErrInvalidMemoryData
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var room model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&room, roomID).Error; err != nil {
			return err
		}
		var op model.GameMemoryOperation
		if err := tx.Where("room_id = ? AND operation_id = ?", roomID, operationID).First(&op).Error; err != nil {
			return err
		}
		if op.Phase == to {
			return nil
		}
		if op.Phase != from {
			return ErrMemoryBusy
		}
		var state model.GameMemoryState
		if err := tx.Where("room_id = ?", roomID).First(&state).Error; err != nil {
			return err
		}
		if state.ActiveOperationID == nil || *state.ActiveOperationID != operationID {
			return ErrMemoryBusy
		}
		updates := map[string]any{"updated_at": time.Now().UTC(), "revision": gorm.Expr("revision + 1")}
		switch to {
		case "completed":
			updates["active_operation_id"] = nil
			if op.Kind == "end" {
				updates["status"] = "ended"
			} else {
				updates["status"], updates["active_timeline_id"] = "ready", *op.TargetTimelineID
				res := tx.Model(&model.GameTimeline{}).Where("room_id = ? AND id = ? AND status = ?", roomID, *op.TargetTimelineID, "prepared").Update("status", "active")
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected != 1 {
					return ErrMemoryBranch
				}
			}
		case "aborted":
			updates["active_operation_id"] = nil
			updates["status"] = "ready"
			if op.Kind == "start" {
				updates["status"] = "blocked"
			}
			if op.TargetTimelineID != nil {
				if err := tx.Model(&model.GameTimeline{}).Where("room_id = ? AND id = ? AND status = ?", roomID, *op.TargetTimelineID, "prepared").Update("status", "aborted").Error; err != nil {
					return err
				}
			}
		case "blocked":
			updates["status"] = "blocked"
		}
		if err := tx.Model(&state).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&op).Updates(map[string]any{"phase": to, "updated_at": time.Now().UTC()}).Error
	})
}

func (r *GameMemoryRepo) ListRecoverableOperations(ctx context.Context, now time.Time, limit int) ([]model.GameMemoryOperation, error) {
	if limit < 1 || limit > 100 {
		return nil, model.ErrInvalidMemoryData
	}
	var operations []model.GameMemoryOperation
	err := r.db.WithContext(ctx).Where("phase IN ? AND next_retry_at <= ?", []string{"prepared", "redis_applied"}, now.UTC()).
		Order("next_retry_at ASC, operation_id ASC").Limit(limit).Find(&operations).Error
	return operations, err
}

// ScheduleOperationRetry prevents a stale worker from replacing a newer retry schedule.
func (r *GameMemoryRepo) ScheduleOperationRetry(ctx context.Context, roomID uint, operationID, phase string, expectedAttempts uint, next time.Time, errorClass string) (bool, error) {
	if roomID == 0 || !model.ValidMemoryUUID(operationID) || (phase != "prepared" && phase != "redis_applied") || next.IsZero() {
		return false, model.ErrInvalidMemoryData
	}
	switch errorClass {
	case "storage", "redis", "timeout", "unknown":
	default:
		return false, model.ErrInvalidMemoryData
	}
	result := r.db.WithContext(ctx).Model(&model.GameMemoryOperation{}).
		Where("room_id = ? AND operation_id = ? AND phase = ? AND attempts = ?", roomID, operationID, phase, expectedAttempts).
		Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_retry_at": next.UTC(), "error_class": errorClass, "updated_at": time.Now().UTC()})
	return result.RowsAffected == 1, result.Error
}

// Archive advances only the next continuous watermark, with exact content replay checks.
// A committed record from a superseded generation still belongs to its original timeline.
func (r *GameMemoryRepo) Archive(ctx context.Context, input *model.GameActionRecord) (bool, error) {
	if input == nil {
		return false, model.ErrInvalidMemoryData
	}
	record := *input
	if err := record.Seal(); err != nil {
		return false, err
	}
	inserted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		timeline, err := findTimeline(tx, record.RoomID, record.TimelineID, true)
		if err != nil {
			return err
		}
		if timeline.Status != "active" {
			return ErrMemoryBranch
		}
		var existing []model.GameActionRecord
		if err := tx.Where("commit_id = ? OR (room_id = ? AND request_namespace = ? AND request_id = ?)",
			record.CommitID, record.RoomID, record.RequestNamespace, record.RequestID).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) > 0 {
			if len(existing) != 1 || existing[0].CommitID != record.CommitID || existing[0].PayloadHash != record.PayloadHash {
				return ErrMemoryConflict
			}
			if record.Position > timeline.DurablePosition {
				return ErrMemoryGap
			}
			stored := existing[0]
			if err := stored.Seal(); err != nil {
				return ErrMemoryConflict
			}
			return nil
		}
		if record.Kind == "legacy_baseline" {
			if timeline.ParentID != nil || timeline.HistoryComplete || timeline.DurablePosition != 0 {
				return ErrMemoryBranch
			}
		} else if timeline.DurablePosition == math.MaxUint64 || record.Position != timeline.DurablePosition+1 {
			return ErrMemoryGap
		}
		if record.Kind == "opening" && (timeline.ParentID != nil || !timeline.HistoryComplete || record.Position != 1) {
			return ErrMemoryBranch
		}
		if timeline.ParentID == nil && timeline.HistoryComplete && record.Position == 1 && record.Kind != "opening" {
			return ErrMemoryBranch
		}
		if !timeline.HistoryComplete && timeline.ParentID == nil && record.Position == 1 {
			var count int64
			if err := tx.Model(&model.GameActionRecord{}).Where("room_id = ? AND timeline_id = ? AND position = 0 AND kind = ?", record.RoomID, record.TimelineID, "legacy_baseline").Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return ErrMemoryGap
			}
		}
		record.CreatedAt = time.Now().UTC()
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if record.Position > 0 {
			if err := tx.Model(timeline).Update("durable_position", record.Position).Error; err != nil {
				return err
			}
		}
		inserted = true
		return nil
	})
	var duplicate *mysqlDriver.MySQLError
	if errors.As(err, &duplicate) && duplicate.Number == 1062 {
		return false, ErrMemoryConflict
	}
	return inserted && err == nil, err
}

func findTimeline(tx *gorm.DB, roomID uint, id string, lock bool) (*model.GameTimeline, error) {
	query := tx.Where("room_id = ? AND id = ?", roomID, id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var timeline model.GameTimeline
	err := query.First(&timeline).Error
	return &timeline, err
}

type visibleSegment struct {
	TimelineID   string
	Lower, Upper uint64
	IncludeZero  bool
}

// resolveVisiblePath never falls back to room-wide history on damaged ancestry.
func resolveVisiblePath(roomID uint, timelineID string, through uint64, load func(string) (*model.GameTimeline, error)) ([]visibleSegment, error) {
	visited := make(map[string]bool)
	segments := make([]visibleSegment, 0)
	var childHistoryComplete *bool
	for {
		if len(segments) >= model.GameMemoryMaxDepth || visited[timelineID] {
			return nil, ErrMemoryBranch
		}
		visited[timelineID] = true
		timeline, err := load(timelineID)
		if err != nil {
			return nil, err
		}
		if timeline == nil || timeline.RoomID != roomID || timeline.ID != timelineID || !model.ValidMemoryUUID(timeline.ID) || timeline.Status != "active" ||
			timeline.DurablePosition < timeline.ForkPosition || through > timeline.DurablePosition || through < timeline.ForkPosition {
			return nil, ErrMemoryBranch
		}
		if childHistoryComplete != nil && *childHistoryComplete != timeline.HistoryComplete {
			return nil, ErrMemoryBranch
		}
		complete := timeline.HistoryComplete
		childHistoryComplete = &complete
		segments = append(segments, visibleSegment{timeline.ID, timeline.ForkPosition, through, timeline.ParentID == nil && !timeline.HistoryComplete})
		if timeline.ParentID == nil {
			if timeline.ForkPosition != 0 {
				return nil, ErrMemoryBranch
			}
			break
		}
		timelineID, through = *timeline.ParentID, timeline.ForkPosition
	}
	for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
		segments[i], segments[j] = segments[j], segments[i]
	}
	return segments, nil
}

// Cursor is opaque to callers and binds pages to a fixed branch and durable path.
type VisibleRecordCursor struct {
	roomID       uint
	timelineID   string
	through      uint64
	pathHash     string
	lastPosition uint64
	hasLast      bool
}

func (r *GameMemoryRepo) ListVisibleRecords(ctx context.Context, roomID uint, timelineID string, cursor *VisibleRecordCursor, limit int) ([]model.GameActionRecord, *VisibleRecordCursor, error) {
	if roomID == 0 || !model.ValidMemoryUUID(timelineID) || limit < 1 || limit > 100 {
		return nil, nil, model.ErrInvalidMemoryData
	}
	var records []model.GameActionRecord
	var next *VisibleRecordCursor
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state model.GameMemoryState
		if err := tx.Where("room_id = ?", roomID).First(&state).Error; err != nil {
			return err
		}
		if state.ActiveTimelineID == nil || *state.ActiveTimelineID != timelineID {
			return ErrMemoryCursor
		}
		if state.Status != "ready" && state.Status != "ended" {
			return ErrMemoryBusy
		}
		timeline, err := findTimeline(tx, roomID, timelineID, false)
		if err != nil {
			return err
		}
		current := VisibleRecordCursor{roomID: roomID, timelineID: timelineID, through: timeline.DurablePosition}
		if cursor != nil {
			if cursor.roomID != roomID || cursor.timelineID != timelineID {
				return ErrMemoryCursor
			}
			current = *cursor
		}
		segments, err := resolveVisiblePath(roomID, timelineID, current.through, func(id string) (*model.GameTimeline, error) { return findTimeline(tx, roomID, id, false) })
		if err != nil {
			return err
		}
		hash, err := model.MemoryHash(segments)
		if err != nil {
			return err
		}
		if cursor != nil && current.pathHash != hash {
			return ErrMemoryCursor
		}
		current.pathHash = hash
		for _, segment := range segments {
			if len(records) > limit {
				break
			}
			query := tx.Where("room_id = ? AND timeline_id = ? AND position <= ?", roomID, segment.TimelineID, segment.Upper)
			if segment.IncludeZero {
				query = query.Where("position >= ?", segment.Lower)
			} else {
				query = query.Where("position > ?", segment.Lower)
			}
			if current.hasLast {
				query = query.Where("position > ?", current.lastPosition)
			}
			var part []model.GameActionRecord
			if err := query.Order("position ASC").Limit(limit + 1 - len(records)).Find(&part).Error; err != nil {
				return err
			}
			expected := segment.Lower
			if !segment.IncludeZero {
				expected++
			}
			if current.hasLast && current.lastPosition >= expected {
				if current.lastPosition == math.MaxUint64 {
					continue
				}
				expected = current.lastPosition + 1
			}
			for _, record := range part {
				if !model.ValidMemoryHash(record.PayloadHash) {
					return ErrMemoryConflict
				}
				if record.Position != expected {
					return ErrMemoryGap
				}
				expected++
				if err := record.Seal(); err != nil {
					return fmt.Errorf("%w: corrupt record", ErrMemoryConflict)
				}
				records = append(records, record)
			}
			if len(records) <= limit && expected <= segment.Upper {
				return ErrMemoryGap
			}
		}
		if len(records) > limit {
			records = records[:limit]
			current.lastPosition, current.hasLast = records[len(records)-1].Position, true
			next = &current
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return records, next, nil
}
