package repo

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"trpggame/internal/model"
)

// ListVisibleKeyEvents follows the immutable ancestry path and reads only
// committed events at or before each branch's visible watermark.
func (r *GameMemoryRepo) ListVisibleKeyEvents(ctx context.Context, roomID uint, timelineID string, afterPosition uint64, afterIndex uint16, limit int) ([]model.KeyEvent, bool, error) {
	if roomID == 0 || !model.ValidMemoryUUID(timelineID) || limit < 1 || limit > 100 {
		return nil, false, model.ErrInvalidMemoryData
	}
	items := make([]model.KeyEvent, 0, limit+1)
	hasMore := false
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
		segments, err := resolveVisiblePath(roomID, timelineID, timeline.DurablePosition, func(id string) (*model.GameTimeline, error) {
			return findTimeline(tx, roomID, id, false)
		})
		if err != nil {
			return err
		}
		for _, segment := range segments {
			if len(items) > limit {
				break
			}
			var part []model.KeyEvent
			query := tx.Where("room_id = ? AND timeline_id = ? AND position > ? AND position <= ?", roomID, segment.TimelineID, segment.Lower, segment.Upper)
			if segment.IncludeZero {
				query = tx.Where("room_id = ? AND timeline_id = ? AND position >= ? AND position <= ?", roomID, segment.TimelineID, segment.Lower, segment.Upper)
			}
			if err := query.Where("position > ? OR (position = ? AND event_index > ?)", afterPosition, afterPosition, afterIndex).
				Order("position ASC, event_index ASC").Limit(limit + 1 - len(items)).Find(&part).Error; err != nil {
				return err
			}
			items = append(items, part...)
		}
		if len(items) > limit {
			hasMore = true
			items = items[:limit]
		}
		if len(items) > 0 {
			ids := make([]string, 0, len(items))
			for _, item := range items {
				ids = append(ids, item.SourceCommitID)
			}
			var records []model.GameActionRecord
			if err := tx.Where("room_id = ? AND commit_id IN ?", roomID, ids).Find(&records).Error; err != nil {
				return err
			}
			byID := make(map[string]model.GameActionRecord, len(records))
			for _, record := range records {
				if err := record.Seal(); err != nil {
					return ErrMemoryConflict
				}
				byID[record.CommitID] = record
			}
			for i := range items {
				record, exists := byID[items[i].SourceCommitID]
				if !exists || record.TimelineID != items[i].TimelineID || record.Position != items[i].Position {
					return ErrMemoryConflict
				}
				var source struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
					Response struct {
						Narrative string `json:"narrative"`
					} `json:"response"`
				}
				if err := json.Unmarshal(record.Payload, &source); err != nil {
					return ErrMemoryConflict
				}
				if len(source.Messages) > 0 {
					items[i].SourceAction = source.Messages[0].Content
				}
				items[i].SourceNarrative = source.Response.Narrative
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return items, hasMore, nil
}

// keyEventsFromRecord uses the normalized effect result, never narrative text.
// It runs inside the action archive transaction so the source and its events
// become durable together. Old records without effects produce no events.
func keyEventsFromRecord(record *model.GameActionRecord) ([]model.KeyEvent, error) {
	if record.Kind != "action" {
		return nil, nil
	}
	var payload struct {
		Response struct {
			Effects *struct {
				Events []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"events"`
			} `json:"effects"`
			MultiplayerEffects *struct {
				Events []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"events"`
			} `json:"multiplayer_effects"`
		} `json:"response"`
	}
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return nil, model.ErrInvalidMemoryData
	}
	if payload.Response.Effects != nil && payload.Response.MultiplayerEffects != nil {
		return nil, model.ErrInvalidMemoryData
	}
	var candidates []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if payload.Response.Effects != nil {
		candidates = payload.Response.Effects.Events
	} else if payload.Response.MultiplayerEffects != nil {
		candidates = payload.Response.MultiplayerEffects.Events
	}
	if len(candidates) > 8 {
		return nil, model.ErrInvalidMemoryData
	}
	events := make([]model.KeyEvent, 0, len(candidates))
	for index, candidate := range candidates {
		name, description := strings.TrimSpace(candidate.Name), strings.TrimSpace(candidate.Description)
		if name == "" || description == "" || utf8.RuneCountInString(name) > 200 || utf8.RuneCountInString(description) > 2000 {
			return nil, model.ErrInvalidMemoryData
		}
		events = append(events, model.KeyEvent{
			RoomID: record.RoomID, TimelineID: record.TimelineID, Position: record.Position,
			SourceCommitID: record.CommitID, EventIndex: uint16(index), EventType: "trigger_event",
			Importance: "major", Name: name, Description: description,
		})
	}
	return events, nil
}
