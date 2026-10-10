package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"trpggame/internal/model"
)

func (r *GameMemoryRepo) ListSummaryRooms(ctx context.Context, after uint) ([]uint, error) {
	var rooms []uint
	err := r.db.WithContext(ctx).Model(&model.GameMemoryState{}).Where("status = ? AND active_operation_id IS NULL AND room_id > ?", "ready", after).Order("room_id").Limit(32).Pluck("room_id", &rooms).Error
	return rooms, err
}

// summaryAt never uses an ancestor summary that extends past the fork watermark.
func summaryAt(tx *gorm.DB, room uint, timeline string, through uint64) (*model.GameSummary, error) {
	segments, err := resolveVisiblePath(room, timeline, through, func(id string) (*model.GameTimeline, error) { return findTimeline(tx, room, id, false) })
	if err != nil {
		return nil, err
	}
	for i := len(segments) - 1; i >= 0; i-- {
		segment := segments[i]
		var summary model.GameSummary
		err = tx.Where("room_id = ? AND timeline_id = ? AND through_position <= ?", room, segment.TimelineID, segment.Upper).Order("through_position DESC, version DESC").First(&summary).Error
		if err == nil {
			return &summary, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		branch, issue := findTimeline(tx, room, segment.TimelineID, false)
		if issue != nil {
			return nil, issue
		}
		if branch.OriginSaveID != nil {
			var save model.GameSave
			if issue := tx.Where("room_id = ? AND id = ?", room, *branch.OriginSaveID).First(&save).Error; issue != nil {
				return nil, issue
			}
			if save.SummaryVersion != nil {
				if issue := validateSummarySave(&save); issue != nil {
					return nil, issue
				}
				return &model.GameSummary{RoomID: room, TimelineID: *save.SummaryTimelineID, Version: *save.SummaryVersion, ThroughPosition: *save.SummaryThroughPosition, Content: save.SummaryMemory}, nil
			}
		}
	}
	// Imported legacy snapshots are the sole known baseline at position zero.
	var baseline model.GameActionRecord
	err = tx.Where("room_id = ? AND timeline_id = ? AND kind = ?", room, segments[0].TimelineID, "legacy_baseline").First(&baseline).Error
	if err == nil {
		if baseline.Seal() != nil {
			return nil, ErrMemoryConflict
		}
		var payload struct {
			Summary       string `json:"summary"`
			SummaryMemory string `json:"summary_memory"`
		}
		if json.Unmarshal(baseline.Payload, &payload) != nil {
			return nil, ErrMemoryConflict
		}
		if payload.Summary == "" {
			payload.Summary = payload.SummaryMemory
		}
		return &model.GameSummary{RoomID: room, TimelineID: segments[0].TimelineID, Content: payload.Summary}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return &model.GameSummary{RoomID: room, TimelineID: timeline}, nil
}

// readSummaryRecords verifies immutable source hashes and continuity across forks.
func readSummaryRecords(tx *gorm.DB, room uint, timeline string, after, through uint64, limit int) ([]model.GameActionRecord, bool, error) {
	segments, err := resolveVisiblePath(room, timeline, through, func(id string) (*model.GameTimeline, error) { return findTimeline(tx, room, id, false) })
	if err != nil {
		return nil, false, err
	}
	records := make([]model.GameActionRecord, 0)
	if after == 0 {
		var baseline model.GameActionRecord
		issue := tx.Where("room_id = ? AND timeline_id = ? AND position = 0 AND kind = ?", room, segments[0].TimelineID, "legacy_baseline").First(&baseline).Error
		if issue == nil {
			if baseline.Seal() != nil {
				return nil, false, ErrMemoryConflict
			}
			records = append(records, baseline)
		} else if !errors.Is(issue, gorm.ErrRecordNotFound) {
			return nil, false, issue
		}
	}
	for _, segment := range segments {
		lower := segment.Lower
		if after > lower {
			lower = after
		}
		if lower >= segment.Upper {
			continue
		}
		var part []model.GameActionRecord
		if err := tx.Where("room_id = ? AND timeline_id = ? AND position > ? AND position <= ?", room, segment.TimelineID, lower, segment.Upper).Order("position").Limit(limit + 1 - len(records)).Find(&part).Error; err != nil {
			return nil, false, err
		}
		expected := lower + 1
		for _, record := range part {
			if record.Position != expected {
				return nil, false, ErrMemoryGap
			}
			if !model.ValidMemoryHash(record.PayloadHash) || record.Seal() != nil {
				return nil, false, ErrMemoryConflict
			}
			expected++
			records = append(records, record)
		}
		if len(records) > limit {
			return records[:limit], true, nil
		}
		if expected <= segment.Upper {
			return nil, false, ErrMemoryGap
		}
	}
	return records, false, nil
}

func recordMessages(record model.GameActionRecord) ([]model.RuntimeMessage, error) {
	var payload struct {
		Messages  []model.RuntimeMessage `json:"messages"`
		Narrative string                 `json:"narrative"`
		Response  struct {
			Narrative string `json:"narrative"`
		} `json:"response"`
	}
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return nil, err
	}
	if record.Kind != "action" && record.Kind != "opening" && record.Kind != "legacy_baseline" {
		return nil, nil
	}
	if len(payload.Messages) > 0 {
		return payload.Messages, nil
	}
	if record.Kind == "legacy_baseline" {
		return nil, nil
	}
	narrative := payload.Response.Narrative
	if narrative == "" {
		narrative = payload.Narrative
	}
	if narrative == "" {
		return nil, model.ErrInvalidMemoryData
	}
	return []model.RuntimeMessage{{Role: "assistant", Content: narrative}}, nil
}

func sourceHash(previous string, records []model.GameActionRecord) (string, error) {
	hashes := make([]string, 0, len(records))
	for _, record := range records {
		hashes = append(hashes, record.PayloadHash)
	}
	return model.MemoryHash(struct {
		Previous string
		Hashes   []string
	}{previous, hashes})
}

// ClaimSummary schedules facts and a retryable job transactionally. The room lock
// matches lifecycle lock order. An expired lease is reclaimed after a restart.
func (r *GameMemoryRepo) ClaimSummary(ctx context.Context, room uint, trigger int, lease time.Duration) (*model.GameSummaryWork, error) {
	var result *model.GameSummaryWork
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var game model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&game, room).Error; err != nil {
			return err
		}
		var state model.GameMemoryState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("room_id = ?", room).First(&state).Error; err != nil {
			return err
		}
		if state.Status != "ready" || state.ActiveTimelineID == nil || state.ActiveOperationID != nil {
			return nil
		}
		timeline, err := findTimeline(tx, room, *state.ActiveTimelineID, true)
		if err != nil {
			return err
		}
		var own model.GameSummary
		err = tx.Where("room_id = ? AND timeline_id = ?", room, timeline.ID).Order("version DESC").First(&own).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		version := own.Version
		inherited := errors.Is(err, gorm.ErrRecordNotFound)
		previous, err := summaryAt(tx, room, timeline.ID, timeline.DurablePosition)
		if err != nil {
			return err
		}
		if inherited {
			version = previous.Version
		}
		now := time.Now().UTC()
		var work model.GameSummaryWork
		err = tx.Where("timeline_id = ? AND expected_version = ?", timeline.ID, version).First(&work).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			records, more, err := readSummaryRecords(tx, room, timeline.ID, previous.ThroughPosition, timeline.DurablePosition, 100)
			if err != nil {
				return err
			}
			count, through := 0, previous.ThroughPosition
			messages := make([]model.RuntimeMessage, 0)
			selected := make([]model.GameActionRecord, 0)
			for _, record := range records {
				part, err := recordMessages(record)
				if err != nil {
					return err
				}
				messages = append(messages, part...)
				selected = append(selected, record)
				through = record.Position
				if record.Kind == "action" {
					count++
				}
				if count >= trigger {
					break
				}
			}
			if count < trigger && !more {
				return nil
			}
			hash, err := sourceHash(previous.Content, selected)
			if err != nil {
				return err
			}
			// A full page of excluded records must still advance the watermark.
			// No narrative facts changed, so carry the existing summary forward.
			if len(messages) == 0 {
				return tx.Create(&model.GameSummary{RoomID: room, TimelineID: timeline.ID, Version: version + 1, ThroughPosition: through, Content: previous.Content, SourceHash: hash, CreatedAt: now}).Error
			}
			body, _ := json.Marshal(messages)
			work = model.GameSummaryWork{ID: uuid.NewString(), RoomID: room, TimelineID: timeline.ID, StateRevision: state.Revision,
				ExpectedVersion: version, FromPosition: previous.ThroughPosition, ThroughPosition: through, SourceHash: hash,
				PreviousSummary: previous.Content, Messages: body, Status: "pending", LeaseUntil: now, NextRetryAt: now, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&work).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if work.Status == "completed" || work.NextRetryAt.After(now) || (work.Status == "running" && work.LeaseUntil.After(now)) {
			return nil
		}
		// A cancelled lifecycle operation can fence and reopen the same branch.
		// Rebind only when reclaiming an eligible job with a new owner token.
		work.StateRevision = state.Revision
		work.Status, work.OwnerToken, work.LeaseUntil = "running", uuid.NewString(), now.Add(lease)
		if err := tx.Model(&work).Updates(map[string]any{"status": work.Status, "owner_token": work.OwnerToken, "lease_until": work.LeaseUntil, "state_revision": state.Revision, "updated_at": now}).Error; err != nil {
			return err
		}
		result = &work
		return nil
	})
	return result, err
}

// CompleteSummary fences lease ownership, branch, revision, old summary version
// and re-read source hashes. A stale model reply cannot mutate a new timeline.
func (r *GameMemoryRepo) CompleteSummary(ctx context.Context, work *model.GameSummaryWork, candidate string) error {
	candidate = strings.TrimSpace(candidate)
	if work == nil || utf8.RuneCountInString(candidate) < 200 || utf8.RuneCountInString(candidate) > 500 {
		return model.ErrInvalidMemoryData
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var room model.GameRoom
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&room, work.RoomID).Error; err != nil {
			return err
		}
		var state model.GameMemoryState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("room_id = ?", work.RoomID).First(&state).Error; err != nil {
			return err
		}
		if state.Status != "ready" || state.ActiveOperationID != nil || state.ActiveTimelineID == nil || *state.ActiveTimelineID != work.TimelineID || state.Revision != work.StateRevision {
			return ErrMemoryCursor
		}
		var current model.GameSummaryWork
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", work.ID).Error; err != nil {
			return err
		}
		if current.Status != "running" || current.OwnerToken != work.OwnerToken || !current.LeaseUntil.After(time.Now().UTC()) {
			return ErrMemoryBusy
		}
		if current.RoomID != work.RoomID || current.TimelineID != work.TimelineID {
			return ErrMemoryConflict
		}
		var own model.GameSummary
		err := tx.Where("room_id = ? AND timeline_id = ?", work.RoomID, work.TimelineID).Order("version DESC").First(&own).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		inherited := errors.Is(err, gorm.ErrRecordNotFound)
		previous, err := summaryAt(tx, work.RoomID, work.TimelineID, work.FromPosition)
		if err != nil {
			return err
		}
		version := own.Version
		if inherited {
			version = previous.Version
		}
		if version != work.ExpectedVersion {
			return ErrMemoryConflict
		}
		if previous.Content != work.PreviousSummary || previous.ThroughPosition != work.FromPosition || current.PreviousSummary != work.PreviousSummary || current.FromPosition != work.FromPosition || current.ThroughPosition != work.ThroughPosition || current.ExpectedVersion != work.ExpectedVersion || current.StateRevision != work.StateRevision {
			return ErrMemoryConflict
		}
		records, more, err := readSummaryRecords(tx, work.RoomID, work.TimelineID, work.FromPosition, work.ThroughPosition, 100)
		if err != nil {
			return err
		}
		hash, err := sourceHash(work.PreviousSummary, records)
		if err != nil || more || hash != work.SourceHash || current.SourceHash != work.SourceHash {
			return ErrMemoryConflict
		}
		messages := make([]model.RuntimeMessage, 0)
		for _, record := range records {
			part, err := recordMessages(record)
			if err != nil {
				return err
			}
			messages = append(messages, part...)
		}
		var stored, supplied []model.RuntimeMessage
		if json.Unmarshal(current.Messages, &stored) != nil || json.Unmarshal(work.Messages, &supplied) != nil {
			return ErrMemoryConflict
		}
		expectedJSON, _ := json.Marshal(messages)
		storedJSON, _ := json.Marshal(stored)
		suppliedJSON, _ := json.Marshal(supplied)
		if !bytes.Equal(expectedJSON, storedJSON) || !bytes.Equal(expectedJSON, suppliedJSON) {
			return ErrMemoryConflict
		}
		summary := model.GameSummary{TimelineID: work.TimelineID, Version: work.ExpectedVersion + 1, RoomID: work.RoomID, ThroughPosition: work.ThroughPosition, Content: candidate, SourceHash: hash, CreatedAt: time.Now().UTC()}
		if err := tx.Create(&summary).Error; err != nil {
			return err
		}
		return tx.Model(&current).Updates(map[string]any{"status": "completed", "owner_token": "", "updated_at": time.Now().UTC()}).Error
	})
}

func (r *GameMemoryRepo) RetrySummary(ctx context.Context, work *model.GameSummaryWork, category string) error {
	delay := time.Second * time.Duration(1<<min(work.Attempts, 8))
	return r.db.WithContext(ctx).Model(&model.GameSummaryWork{}).Where("id = ? AND owner_token = ? AND status = ?", work.ID, work.OwnerToken, "running").Updates(map[string]any{
		"status": "pending", "owner_token": "", "attempts": gorm.Expr("attempts + 1"), "next_retry_at": time.Now().UTC().Add(delay), "error_class": category, "updated_at": time.Now().UTC()}).Error
}

// LoadMemoryContext supplements a lagging summary from durable records. A bounded
// overflow is explicit; the Python budget makes the final deterministic cut.
func (r *GameMemoryRepo) LoadMemoryContext(ctx context.Context, room uint, timelineID string) (*model.MemoryContext, error) {
	var result *model.MemoryContext
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state model.GameMemoryState
		if err := tx.Where("room_id = ?", room).First(&state).Error; err != nil {
			return err
		}
		if state.Status != "ready" || state.ActiveOperationID != nil || state.ActiveTimelineID == nil || *state.ActiveTimelineID != timelineID {
			return ErrMemoryCursor
		}
		timeline, err := findTimeline(tx, room, timelineID, false)
		if err != nil {
			return err
		}
		summary, err := summaryAt(tx, room, timelineID, timeline.DurablePosition)
		if err != nil {
			return err
		}
		// Retain ten recent durable records even when the summary covers them.
		// Taking the union with the uncovered interval avoids duplicate messages.
		after := summary.ThroughPosition
		recentAfter := uint64(0)
		if timeline.DurablePosition > 10 {
			recentAfter = timeline.DurablePosition - 10
		}
		if recentAfter < after {
			after = recentAfter
		}
		records, more, err := readSummaryRecords(tx, room, timelineID, after, timeline.DurablePosition, 100)
		if err != nil {
			return err
		}
		if more {
			// Prefer the latest facts when the bounded catch-up window overflows.
			after := summary.ThroughPosition
			if timeline.DurablePosition > 100 && timeline.DurablePosition-100 > after {
				after = timeline.DurablePosition - 100
			}
			records, _, err = readSummaryRecords(tx, room, timelineID, after, timeline.DurablePosition, 100)
			if err != nil {
				return err
			}
		}
		result = &model.MemoryContext{TimelineID: timelineID, ThroughPosition: timeline.DurablePosition, SummaryThroughPosition: summary.ThroughPosition, SummaryVersion: summary.Version, Summary: summary.Content, Messages: []model.RuntimeMessage{}, KeyEvents: []string{}, Degraded: more || !timeline.HistoryComplete}
		for _, record := range records {
			messages, err := recordMessages(record)
			if err != nil {
				return err
			}
			result.Messages = append(result.Messages, messages...)
		}
		segments, err := resolveVisiblePath(room, timelineID, timeline.DurablePosition, func(id string) (*model.GameTimeline, error) { return findTimeline(tx, room, id, false) })
		if err != nil {
			return err
		}
		seen := make(map[string]bool)
		for i := len(segments) - 1; i >= 0 && len(result.KeyEvents) < 20; i-- {
			segment := segments[i]
			var events []model.KeyEvent
			if err := tx.Where("room_id = ? AND timeline_id = ? AND position > ? AND position <= ?", room, segment.TimelineID, segment.Lower, segment.Upper).Order("position DESC, event_index DESC").Limit(20 - len(result.KeyEvents)).Find(&events).Error; err != nil {
				return err
			}
			for _, event := range events {
				text := fmt.Sprintf("%s：%s", event.Name, event.Description)
				if !seen[text] {
					result.KeyEvents = append(result.KeyEvents, text)
					seen[text] = true
				}
			}
		}
		return nil
	})
	return result, err
}
