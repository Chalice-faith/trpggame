package repo

import (
	"gorm.io/gorm"

	"trpggame/internal/model"
)

// ScriptRepo 剧本数据访问层
type ScriptRepo struct {
	db *gorm.DB
}

// NewScriptRepo 创建 ScriptRepo
func NewScriptRepo(db *gorm.DB) *ScriptRepo {
	return &ScriptRepo{db: db}
}

// Create 创建剧本记录
func (r *ScriptRepo) Create(script *model.Script) error {
	return r.db.Create(script).Error
}

// FindByID 按 ID 查询
func (r *ScriptRepo) FindByID(id uint) (*model.Script, error) {
	var script model.Script
	err := r.db.First(&script, id).Error
	if err != nil {
		return nil, err
	}
	return &script, nil
}

// FindByIDAndUserID 按剧本 ID 与所有者查询，避免跨用户访问。
func (r *ScriptRepo) FindByIDAndUserID(id, userID uint) (*model.Script, error) {
	var script model.Script
	err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&script).Error
	if err != nil {
		return nil, err
	}
	return &script, nil
}

// FindCharactersByScriptID 返回剧本的预设角色列表。
func (r *ScriptRepo) FindCharactersByScriptID(scriptID uint) ([]model.ScriptCharacter, error) {
	var characters []model.ScriptCharacter
	err := r.db.Where("script_id = ?", scriptID).Order("id ASC").Find(&characters).Error
	return characters, err
}

// FindByUserID 按用户 ID 分页查询
func (r *ScriptRepo) FindByUserID(userID uint, offset, limit int) ([]model.Script, int64, error) {
	var scripts []model.Script
	var total int64

	query := r.db.Where("user_id = ?", userID)

	if err := query.Model(&model.Script{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&scripts).Error
	return scripts, total, err
}

// UpdateStatus 更新剧本解析状态
func (r *ScriptRepo) UpdateStatus(id uint, status model.ScriptStatus, errMsg string) error {
	updates := map[string]interface{}{
		"status":      status,
		"parse_error": errMsg,
	}
	return r.db.Model(&model.Script{}).Where("id = ?", id).Updates(updates).Error
}

// UpdateFile 更新剧本对应的对象存储路径与文件大小。
func (r *ScriptRepo) UpdateFile(id uint, filePath string, fileSize int64) error {
	return r.db.Model(&model.Script{}).Where("id = ?", id).Updates(map[string]interface{}{
		"file_path": filePath,
		"file_size": fileSize,
	}).Error
}

// Delete 软删除剧本
func (r *ScriptRepo) Delete(id uint) error {
	return r.db.Delete(&model.Script{}, id).Error
}
