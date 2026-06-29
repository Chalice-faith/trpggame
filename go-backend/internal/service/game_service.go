package service

import (
	"trpggame/internal/config"
	"trpggame/internal/repo"
)

// GameService 游戏业务逻辑
type GameService struct {
	gameRepo   *repo.GameRepo
	scriptRepo *repo.ScriptRepo
	cfg        *config.Config
}

// NewGameService 创建 GameService
func NewGameService(gr *repo.GameRepo, sr *repo.ScriptRepo, cfg *config.Config) *GameService {
	return &GameService{
		gameRepo:   gr,
		scriptRepo: sr,
		cfg:        cfg,
	}
}

// TODO: Phase 1 M1.5 实现快速开始、行动处理、存档读档等核心逻辑
