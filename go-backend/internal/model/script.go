package model

import (
	"time"

	"gorm.io/gorm"
)

// ScriptStatus 剧本解析状态
type ScriptStatus string

const (
	ScriptStatusUploading ScriptStatus = "uploading"
	ScriptStatusParsing   ScriptStatus = "parsing"
	ScriptStatusReady     ScriptStatus = "ready"
	ScriptStatusFailed    ScriptStatus = "failed"
)

// Script 剧本模型
type Script struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	UserID      uint           `gorm:"index;not null" json:"user_id"`
	Title       string         `gorm:"size:200;not null" json:"title"`
	Description string         `gorm:"type:text" json:"description"`
	CoverURL    string         `gorm:"size:500" json:"cover_url"`
	FilePath    string         `gorm:"size:500;not null" json:"file_path"`    // MinIO 文件路径
	FileSize    int64          `json:"file_size"`                             // 文件大小（字节）
	Status      ScriptStatus   `gorm:"size:20;default:uploading" json:"status"`
	ParseError  string         `gorm:"type:text" json:"parse_error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// TableName 自定义表名
func (Script) TableName() string {
	return "scripts"
}

// ScriptCharacter 剧本预设角色
type ScriptCharacter struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	ScriptID    uint   `gorm:"index;not null" json:"script_id"`
	Name        string `gorm:"size:100;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	Attributes  string `gorm:"type:jsonb" json:"attributes"` // JSON 格式存储属性值
}
