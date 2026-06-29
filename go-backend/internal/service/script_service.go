package service

import (
	"trpggame/internal/config"
	"trpggame/internal/repo"
)

// ScriptService 剧本业务逻辑
type ScriptService struct {
	repo *repo.ScriptRepo
	cfg  *config.Config
}

// NewScriptService 创建 ScriptService
func NewScriptService(r *repo.ScriptRepo, cfg *config.Config) *ScriptService {
	return &ScriptService{repo: r, cfg: cfg}
}

// TODO: Phase 1 M1.3 实现上传、解析、列表等业务逻辑
