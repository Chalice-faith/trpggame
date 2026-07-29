package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"trpggame/internal/ai_client"
	"trpggame/internal/config"
	"trpggame/internal/model"
)

const (
	maxScriptTitleLength  = 200
	pdfContentType        = "application/pdf"
	defaultScriptPageSize = 20
	maxScriptPageSize     = 100
)

// ScriptRepository 描述剧本上传流程需要的数据访问能力。
type ScriptRepository interface {
	Create(script *model.Script) error
	FindByIDAndUserID(id, userID uint) (*model.Script, error)
	FindCharactersByScriptID(scriptID uint) ([]model.ScriptCharacter, error)
	FindByUserID(userID uint, offset, limit int) ([]model.Script, int64, error)
	UpdateFile(id uint, filePath string, fileSize int64) error
	UpdateStatus(id uint, status model.ScriptStatus, errMsg string) error
}

// ScriptObjectStorage 描述剧本上传流程需要的对象存储能力。
type ScriptObjectStorage interface {
	PutObject(ctx context.Context, objectName string, reader io.Reader, size int64, contentType string) error
	RemoveObject(ctx context.Context, objectName string) error
}

// ScriptParserClient 描述触发 Python 剧本解析所需的能力。
type ScriptParserClient interface {
	ParseScript(ctx context.Context, req *ai_client.ParseScriptRequest) (*ai_client.ParseScriptResponse, error)
}

// ScriptService 剧本业务逻辑。
type ScriptService struct {
	repo     ScriptRepository
	storage  ScriptObjectStorage
	aiClient ScriptParserClient
	cfg      *config.Config
}

// UploadScriptRequest 是服务层剧本上传请求。
// File 的生命周期由调用方管理，服务不会关闭它。
type UploadScriptRequest struct {
	UserID      uint
	Title       string
	Description string
	File        multipart.File
	FileHeader  *multipart.FileHeader
}

// ListScriptsResult 是分页剧本列表。
type ListScriptsResult struct {
	Items    []model.Script
	Total    int64
	Page     int
	PageSize int
}

// ScriptDetailResult 包含剧本详情及其预设角色。
type ScriptDetailResult struct {
	Script     *model.Script
	Characters []model.ScriptCharacter
}

// NewScriptService 创建 ScriptService。
func NewScriptService(
	repository ScriptRepository,
	objectStorage ScriptObjectStorage,
	aiClient ScriptParserClient,
	cfg *config.Config,
) *ScriptService {
	return &ScriptService{
		repo:     repository,
		storage:  objectStorage,
		aiClient: aiClient,
		cfg:      cfg,
	}
}

// Upload 校验并保存 PDF，然后触发 Python 后台解析任务。
func (s *ScriptService) Upload(ctx context.Context, req *UploadScriptRequest) (*model.Script, error) {
	if req == nil || req.UserID == 0 {
		return nil, fmt.Errorf("%w: invalid upload request", ErrInternal)
	}
	if err := ValidatePDFUpload(req.File, req.FileHeader, s.cfg.MinIO.MaxUploadSize); err != nil {
		return nil, err
	}

	title := normalizeScriptTitle(req.Title, req.FileHeader.Filename)
	if utf8.RuneCountInString(title) > maxScriptTitleLength {
		return nil, ErrScriptTitleTooLong
	}

	script := &model.Script{
		UserID:      req.UserID,
		Title:       title,
		Description: strings.TrimSpace(req.Description),
		FilePath:    "",
		FileSize:    req.FileHeader.Size,
		Status:      model.ScriptStatusUploading,
	}
	if err := s.repo.Create(script); err != nil {
		return nil, fmt.Errorf("%w: create script record: %v", ErrInternal, err)
	}

	objectName := fmt.Sprintf(
		"scripts/%d/%d/%s.pdf",
		req.UserID,
		script.ID,
		uuid.NewString(),
	)
	if err := s.storage.PutObject(
		ctx,
		objectName,
		req.File,
		req.FileHeader.Size,
		pdfContentType,
	); err != nil {
		s.markFailed(script.ID, "failed to store script file")
		return nil, fmt.Errorf("%w: store script file: %v", ErrInternal, err)
	}

	if err := s.repo.UpdateFile(script.ID, objectName, req.FileHeader.Size); err != nil {
		_ = s.storage.RemoveObject(ctx, objectName)
		s.markFailed(script.ID, "failed to save script file metadata")
		return nil, fmt.Errorf("%w: update script file metadata: %v", ErrInternal, err)
	}
	script.FilePath = objectName

	if err := s.repo.UpdateStatus(script.ID, model.ScriptStatusParsing, ""); err != nil {
		return nil, fmt.Errorf("%w: update script parsing status: %v", ErrInternal, err)
	}
	script.Status = model.ScriptStatusParsing

	parseResult, err := s.aiClient.ParseScript(ctx, &ai_client.ParseScriptRequest{
		ScriptID: script.ID,
		FilePath: objectName,
	})
	if err != nil {
		s.markFailed(script.ID, "failed to start script parsing")
		return nil, fmt.Errorf("%w: start script parsing: %v", ErrInternal, err)
	}
	if parseResult == nil || !parseResult.Success {
		s.markFailed(script.ID, "script parser rejected the task")
		return nil, fmt.Errorf("%w: script parser rejected the task", ErrInternal)
	}

	return script, nil
}

// List 返回指定用户拥有的剧本，不允许跨用户查询。
func (s *ScriptService) List(userID uint, page, pageSize int) (*ListScriptsResult, error) {
	if userID == 0 {
		return nil, fmt.Errorf("%w: invalid user ID", ErrInternal)
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = defaultScriptPageSize
	}
	if pageSize > maxScriptPageSize {
		pageSize = maxScriptPageSize
	}
	if page-1 > int(^uint(0)>>1)/pageSize {
		return nil, ErrInvalidPagination
	}

	offset := (page - 1) * pageSize
	items, total, err := s.repo.FindByUserID(userID, offset, pageSize)
	if err != nil {
		return nil, fmt.Errorf("%w: list scripts: %v", ErrInternal, err)
	}
	if items == nil {
		items = make([]model.Script, 0)
	}

	return &ListScriptsResult{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// GetDetail 返回用户拥有的剧本详情及预设角色。
func (s *ScriptService) GetDetail(userID, scriptID uint) (*ScriptDetailResult, error) {
	if userID == 0 || scriptID == 0 {
		return nil, ErrScriptNotFound
	}

	script, err := s.repo.FindByIDAndUserID(scriptID, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrScriptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: get script detail: %v", ErrInternal, err)
	}

	characters, err := s.repo.FindCharactersByScriptID(scriptID)
	if err != nil {
		return nil, fmt.Errorf("%w: list script characters: %v", ErrInternal, err)
	}
	if characters == nil {
		characters = make([]model.ScriptCharacter, 0)
	}

	return &ScriptDetailResult{
		Script:     script,
		Characters: characters,
	}, nil
}

func (s *ScriptService) markFailed(scriptID uint, message string) {
	_ = s.repo.UpdateStatus(scriptID, model.ScriptStatusFailed, message)
}

func normalizeScriptTitle(title, filename string) string {
	title = strings.TrimSpace(title)
	if title != "" {
		return title
	}

	base := filepath.Base(filename)
	title = strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	if title == "" {
		return "Untitled Script"
	}
	return title
}
