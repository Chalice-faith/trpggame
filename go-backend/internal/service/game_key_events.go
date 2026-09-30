package service

import (
	"context"
	"fmt"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type GameKeyEventPage struct {
	TimelineID   string           `json:"timeline_id"`
	Items        []model.KeyEvent `json:"items"`
	HasMore      bool             `json:"has_more"`
	NextPosition uint64           `json:"next_position,omitempty"`
	NextIndex    uint16           `json:"next_index,omitempty"`
}

// ListGameKeyEvents grants read access only to the owner or an active room
// member and binds every page to the caller's expected timeline.
func (s *GameService) ListGameKeyEvents(ctx context.Context, userID, roomID uint, timelineID string, afterPosition uint64, afterIndex uint16, limit int) (*GameKeyEventPage, error) {
	if !model.ValidMemoryUUID(timelineID) || limit < 1 || limit > 100 {
		return nil, ErrInvalidGameRequest
	}
	if _, err := s.authorizeMemoryReader(ctx, userID, roomID); err != nil {
		return nil, err
	}
	if s.memoryLifecycle == nil {
		return nil, repo.ErrMemoryCursor
	}
	reader, ok := s.memoryLifecycle.journal.(interface {
		ListVisibleKeyEvents(context.Context, uint, string, uint64, uint16, int) ([]model.KeyEvent, bool, error)
	})
	if !ok {
		return nil, ErrInternal
	}
	items, more, err := reader.ListVisibleKeyEvents(ctx, roomID, timelineID, afterPosition, afterIndex, limit)
	if err != nil {
		return nil, fmt.Errorf("list key events: %w", err)
	}
	page := &GameKeyEventPage{TimelineID: timelineID, Items: items, HasMore: more}
	if more && len(items) > 0 {
		last := items[len(items)-1]
		page.NextPosition, page.NextIndex = last.Position, last.EventIndex
	}
	return page, nil
}
