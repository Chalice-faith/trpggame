package repo

import (
	"context"

	"gorm.io/gorm"
	"trpggame/internal/model"
)

type KeyEventBackfillRequest struct {
	RoomID          uint
	TimelineID      string
	AfterPosition   uint64
	ThroughPosition *uint64 // nil freezes the current durable watermark
	Limit           int
	Apply           bool
}

// A checkpoint is returned only after the entire bounded batch commits.
// Replaying a checkpoint is safe, including after a lost commit response.
type KeyEventBackfillResult struct {
	RoomID          uint   `json:"room_id"`
	TimelineID      string `json:"timeline_id"`
	NextPosition    uint64 `json:"next_position"`
	ThroughPosition uint64 `json:"through_position"`
	Scanned         int    `json:"scanned"`
	Missing         int    `json:"missing"`
	Inserted        int    `json:"inserted"`
	Done            bool   `json:"done"`
	Applied         bool   `json:"applied"`
}

// BackfillKeyEvents repairs only structured effects from immutable records in
// the explicitly selected timeline. Ancestors require separate runs; public
// readers continue to enforce their own branch visibility and permissions.
func (r *GameMemoryRepo) BackfillKeyEvents(ctx context.Context, request KeyEventBackfillRequest) (*KeyEventBackfillResult, error) {
	if request.RoomID == 0 || !model.ValidMemoryUUID(request.TimelineID) || request.Limit < 1 || request.Limit > 100 {
		return nil, model.ErrInvalidMemoryData
	}
	result := &KeyEventBackfillResult{RoomID: request.RoomID, TimelineID: request.TimelineID, Applied: request.Apply}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Archival and backfill take the same timeline lock. This serializes
		// concurrent repairs without updating immutable events on duplicates.
		timeline, err := findTimeline(tx, request.RoomID, request.TimelineID, true)
		if err != nil {
			return err
		}
		through := timeline.DurablePosition
		if request.ThroughPosition != nil {
			through = *request.ThroughPosition
		}
		after := request.AfterPosition
		if after < timeline.ForkPosition {
			after = timeline.ForkPosition
		}
		if after > through || through > timeline.DurablePosition || through < timeline.ForkPosition {
			return model.ErrInvalidMemoryData
		}
		result.NextPosition, result.ThroughPosition = after, through
		// Range arithmetic is bounded by the already validated watermark.
		end := through
		if through-after > uint64(request.Limit) {
			end = after + uint64(request.Limit)
		}
		var records []model.GameActionRecord
		if err := tx.Where("room_id = ? AND timeline_id = ? AND position > ? AND position <= ?", request.RoomID, request.TimelineID, after, end).
			Order("position").Limit(request.Limit).Find(&records).Error; err != nil {
			return err
		}
		if uint64(len(records)) != end-after {
			return ErrMemoryGap
		}
		for i := range records {
			record := &records[i]
			if record.Position != after+uint64(i)+1 {
				return ErrMemoryGap
			}
			if !model.ValidMemoryHash(record.PayloadHash) || record.Seal() != nil {
				return ErrMemoryConflict
			}
			expected, err := keyEventsFromRecord(record)
			if err != nil {
				return err
			}
			var existing []model.KeyEvent
			// Do not scope by room: a mismatched source in any room is a conflict.
			if err := tx.Where("source_commit_id = ?", record.CommitID).Order("event_index").Limit(9).Find(&existing).Error; err != nil {
				return err
			}
			seen := make(map[uint16]bool, len(existing))
			for _, event := range existing {
				if int(event.EventIndex) >= len(expected) || seen[event.EventIndex] || !sameKeyEvent(event, expected[event.EventIndex]) {
					return ErrMemoryConflict
				}
				seen[event.EventIndex] = true
			}
			for _, event := range expected {
				if seen[event.EventIndex] {
					continue
				}
				result.Missing++
				if request.Apply {
					// Preserve the original source timestamp, not the repair time.
					event.CreatedAt = record.CreatedAt
					if err := tx.Create(&event).Error; err != nil {
						return err
					}
					result.Inserted++
				}
			}
		}
		result.Scanned, result.NextPosition, result.Done = len(records), end, end == through
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func sameKeyEvent(a, b model.KeyEvent) bool {
	return a.RoomID == b.RoomID && a.TimelineID == b.TimelineID && a.Position == b.Position &&
		a.SourceCommitID == b.SourceCommitID && a.EventIndex == b.EventIndex && a.EventType == b.EventType &&
		a.Importance == b.Importance && a.Name == b.Name && a.Description == b.Description
}
