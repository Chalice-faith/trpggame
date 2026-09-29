package repo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"trpggame/internal/model"
)

// validateMemorySave checks relational metadata against the V3 envelope without
// replacing the mode-specific runtime decoders planned in A4.
func validateMemorySave(save *model.GameSave) (bool, error) {
	if save == nil {
		return false, model.ErrInvalidMemoryData
	}
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Mode          string          `json:"mode"`
		Runtime       json.RawMessage `json:"runtime"`
		Memory        struct {
			TimelineID      string  `json:"timeline_id"`
			Position        *uint64 `json:"position"`
			HistoryComplete *bool   `json:"history_complete"`
		} `json:"memory"`
	}
	if err := json.Unmarshal(save.RedisSnapshot, &envelope); err != nil {
		return false, model.ErrInvalidMemoryData
	}
	if save.TimelineID == nil && save.MemoryPosition == nil && envelope.SchemaVersion == 0 {
		return false, nil
	}
	if save.TimelineID == nil || save.MemoryPosition == nil || save.RoomID == 0 || save.RoundNumber < 0 ||
		!model.ValidMemoryUUID(*save.TimelineID) || envelope.SchemaVersion != 3 ||
		(envelope.Mode != "solo" && envelope.Mode != "multiplayer") ||
		envelope.Memory.TimelineID != *save.TimelineID || envelope.Memory.Position == nil ||
		*envelope.Memory.Position != *save.MemoryPosition || envelope.Memory.HistoryComplete == nil {
		return false, model.ErrInvalidMemoryData
	}
	if _, err := model.CanonicalMemoryJSON(envelope.Runtime); err != nil {
		return false, err
	}
	return true, nil
}

func memorySaveHash(save *model.GameSave) (string, error) {
	snapshot, err := model.CanonicalMemoryJSON(save.RedisSnapshot)
	if err != nil {
		return "", err
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(save.RecentMessages, &messages); err != nil || messages == nil {
		return "", model.ErrInvalidMemoryData
	}
	for index, message := range messages {
		canonical, err := model.CanonicalMemoryJSON(message)
		if err != nil {
			return "", err
		}
		messages[index] = canonical
	}
	return model.MemoryHash(struct {
		RoomID     uint
		Round      int
		TimelineID *string
		Position   *uint64
		Summary    string
		Snapshot   json.RawMessage
		Messages   []json.RawMessage
	}{save.RoomID, save.RoundNumber, save.TimelineID, save.MemoryPosition, save.SummaryMemory, snapshot, messages})
}

func (r *GameRepo) createMemorySave(ctx context.Context, input *model.GameSave) (bool, error) {
	copy := *input
	if copy.CreatedAt.IsZero() {
		copy.CreatedAt = time.Now().UTC()
	} else {
		copy.CreatedAt = copy.CreatedAt.UTC()
	}
	hash, err := memorySaveHash(&copy)
	if err != nil {
		return false, err
	}
	inserted := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		timeline, err := findTimeline(tx, copy.RoomID, *copy.TimelineID, true)
		if err != nil {
			return err
		}
		if timeline.Status != "active" || *copy.MemoryPosition < timeline.ForkPosition || *copy.MemoryPosition > timeline.DurablePosition {
			return ErrMemoryGap
		}
		var memory struct {
			Memory struct {
				HistoryComplete bool `json:"history_complete"`
			} `json:"memory"`
		}
		if err := json.Unmarshal(copy.RedisSnapshot, &memory); err != nil {
			return err
		}
		if memory.Memory.HistoryComplete != timeline.HistoryComplete {
			return ErrMemoryBranch
		}
		if err := tx.Create(&copy).Error; err != nil {
			var duplicate *mysqlDriver.MySQLError
			if !copy.IsAuto || !errors.As(err, &duplicate) || duplicate.Number != 1062 {
				return err
			}
			var existing model.GameSave
			if err := tx.Where("room_id = ? AND timeline_id = ? AND is_auto = ? AND round_number = ?", copy.RoomID,
				*copy.TimelineID, true, copy.RoundNumber).First(&existing).Error; err != nil {
				return err
			}
			existingHash, err := memorySaveHash(&existing)
			if err != nil || existingHash != hash {
				return ErrMemoryConflict
			}
			copy = existing
			return nil
		}
		inserted = true
		return nil
	})
	if err == nil {
		*input = copy
	}
	return inserted && err == nil, err
}
