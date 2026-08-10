package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ListGameSavesRequest 是查询房间存档列表的服务层请求。
type ListGameSavesRequest struct {
	UserID uint
	RoomID uint
}

// GameSaveSummary 是对客户端安全的存档摘要，不包含运行态快照和消息正文。
type GameSaveSummary struct {
	ID          uint      `json:"id"`
	SaveName    string    `json:"save_name"`
	RoundNumber int       `json:"round_number"`
	IsAuto      bool      `json:"is_auto"`
	CreatedAt   time.Time `json:"created_at"`
}

// ListGameSavesResult 是房间存档列表结果。
type ListGameSavesResult struct {
	Items []GameSaveSummary `json:"items"`
	Total int               `json:"total"`
}

// ListGameSaves 校验房间所有权并返回不含完整快照的存档摘要。
func (s *GameService) ListGameSaves(
	ctx context.Context,
	req *ListGameSavesRequest,
) (*ListGameSavesResult, error) {
	if req == nil || req.UserID == 0 || req.RoomID == 0 {
		return nil, ErrInvalidGameSaveQuery
	}

	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, req.RoomID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find room for save list: %v", ErrInternal, err)
	}
	if room == nil || room.ID != req.RoomID || room.OwnerID != req.UserID {
		return nil, fmt.Errorf("%w: invalid room repository result", ErrInternal)
	}

	saves, err := s.gameRepo.ListSaves(ctx, room.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: list game saves: %v", ErrInternal, err)
	}
	items := make([]GameSaveSummary, len(saves))
	for index, save := range saves {
		saveName := strings.TrimSpace(save.SaveName)
		if save.ID == 0 || save.RoomID != room.ID || saveName == "" ||
			save.RoundNumber < 0 || save.CreatedAt.IsZero() {
			return nil, fmt.Errorf("%w: invalid game save repository result", ErrInternal)
		}
		items[index] = GameSaveSummary{
			ID:          save.ID,
			SaveName:    saveName,
			RoundNumber: save.RoundNumber,
			IsAuto:      save.IsAuto,
			CreatedAt:   save.CreatedAt,
		}
	}
	return &ListGameSavesResult{Items: items, Total: len(items)}, nil
}
