package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"trpggame/internal/model"
	"trpggame/internal/service"
)

const multipartOverheadAllowance int64 = 1 << 20

// ScriptUploadService 描述上传 Handler 所需的服务能力。
type ScriptUploadService interface {
	Upload(ctx context.Context, req *service.UploadScriptRequest) (*model.Script, error)
	List(userID uint, page, pageSize int) (*service.ListScriptsResult, error)
	GetDetail(userID, scriptID uint) (*service.ScriptDetailResult, error)
	Delete(ctx context.Context, userID, scriptID uint) error
}

// ScriptHandler 剧本相关 HTTP 处理器。
type ScriptHandler struct {
	svc           ScriptUploadService
	maxUploadSize int64
}

// NewScriptHandler 创建 ScriptHandler。
func NewScriptHandler(svc ScriptUploadService, maxUploadSize int64) *ScriptHandler {
	return &ScriptHandler{
		svc:           svc,
		maxUploadSize: maxUploadSize,
	}
}

type uploadScriptResponse struct {
	ID          uint               `json:"id"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	FileSize    int64              `json:"file_size"`
	Status      model.ScriptStatus `json:"status"`
	CreatedAt   time.Time          `json:"created_at"`
}

type scriptListItemResponse struct {
	ID          uint               `json:"id"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	CoverURL    string             `json:"cover_url"`
	FileSize    int64              `json:"file_size"`
	Status      model.ScriptStatus `json:"status"`
	ParseError  string             `json:"parse_error,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type scriptCharacterResponse struct {
	ID          uint            `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Attributes  json.RawMessage `json:"attributes"`
}

type scriptDetailResponse struct {
	scriptListItemResponse
	Characters []scriptCharacterResponse `json:"characters"`
}

// UploadScript 上传 PDF 剧本。
func (h *ScriptHandler) UploadScript(c *gin.Context) {
	userID, ok := scriptUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}
	if h.maxUploadSize <= 0 {
		log.Printf("script upload: invalid max upload size: %d", h.maxUploadSize)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1203,
			"message": "internal error",
		})
		return
	}

	c.Request.Body = http.MaxBytesReader(
		c.Writer,
		c.Request.Body,
		h.maxUploadSize+multipartOverheadAllowance,
	)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"code":    1201,
				"message": service.ErrScriptFileTooLarge.Error(),
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1200,
			"message": service.ErrScriptFileRequired.Error(),
		})
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}

	file, err := fileHeader.Open()
	if err != nil {
		log.Printf("script upload: open multipart file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1203,
			"message": "internal error",
		})
		return
	}
	defer file.Close()

	script, err := h.svc.Upload(c.Request.Context(), &service.UploadScriptRequest{
		UserID:      userID,
		Title:       c.PostForm("title"),
		Description: c.PostForm("description"),
		File:        file,
		FileHeader:  fileHeader,
	})
	if err != nil {
		writeScriptUploadError(c, err)
		return
	}
	if script == nil {
		log.Print("script upload: service returned an empty result")
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1203,
			"message": "internal error",
		})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"code":    0,
		"message": "accepted",
		"data": uploadScriptResponse{
			ID:          script.ID,
			Title:       script.Title,
			Description: script.Description,
			FileSize:    script.FileSize,
			Status:      script.Status,
			CreatedAt:   script.CreatedAt,
		},
	})
}

// ListScripts 剧本列表
func (h *ScriptHandler) ListScripts(c *gin.Context) {
	userID, ok := scriptUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}

	page, err := positiveQueryInt(c, "page", 1)
	if err != nil {
		writePaginationError(c)
		return
	}
	pageSize, err := positiveQueryInt(c, "page_size", defaultHandlerPageSize)
	if err != nil {
		writePaginationError(c)
		return
	}

	result, err := h.svc.List(userID, page, pageSize)
	if err != nil {
		if errors.Is(err, service.ErrInvalidPagination) {
			writePaginationError(c)
			return
		}
		log.Printf("script list: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1203,
			"message": "internal error",
		})
		return
	}
	if result == nil {
		log.Print("script list: service returned an empty result")
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1203,
			"message": "internal error",
		})
		return
	}

	items := make([]scriptListItemResponse, 0, len(result.Items))
	for _, script := range result.Items {
		items = append(items, scriptListItemResponse{
			ID:          script.ID,
			Title:       script.Title,
			Description: script.Description,
			CoverURL:    script.CoverURL,
			FileSize:    script.FileSize,
			Status:      script.Status,
			ParseError:  script.ParseError,
			CreatedAt:   script.CreatedAt,
			UpdatedAt:   script.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": gin.H{
			"items":     items,
			"total":     result.Total,
			"page":      result.Page,
			"page_size": result.PageSize,
		},
	})
}

// GetScriptDetail 剧本详情
func (h *ScriptHandler) GetScriptDetail(c *gin.Context) {
	userID, ok := scriptUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}

	scriptID, err := positivePathID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1205,
			"message": "invalid script ID",
		})
		return
	}

	result, err := h.svc.GetDetail(userID, scriptID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrScriptNotFound):
			c.JSON(http.StatusNotFound, gin.H{
				"code":    1206,
				"message": service.ErrScriptNotFound.Error(),
			})
		default:
			log.Printf("script detail: service error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    1203,
				"message": "internal error",
			})
		}
		return
	}
	if result == nil || result.Script == nil {
		log.Print("script detail: service returned an empty result")
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1203,
			"message": "internal error",
		})
		return
	}

	characters := make([]scriptCharacterResponse, 0, len(result.Characters))
	for _, character := range result.Characters {
		attributes := json.RawMessage(character.Attributes)
		if !json.Valid(attributes) {
			attributes = json.RawMessage(`{}`)
		}
		characters = append(characters, scriptCharacterResponse{
			ID:          character.ID,
			Name:        character.Name,
			Description: character.Description,
			Attributes:  attributes,
		})
	}

	script := result.Script
	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": scriptDetailResponse{
			scriptListItemResponse: scriptListItemResponse{
				ID:          script.ID,
				Title:       script.Title,
				Description: script.Description,
				CoverURL:    script.CoverURL,
				FileSize:    script.FileSize,
				Status:      script.Status,
				ParseError:  script.ParseError,
				CreatedAt:   script.CreatedAt,
				UpdatedAt:   script.UpdatedAt,
			},
			Characters: characters,
		},
	})
}

// DeleteScript 删除剧本
func (h *ScriptHandler) DeleteScript(c *gin.Context) {
	userID, ok := scriptUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}

	scriptID, err := positivePathID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1205,
			"message": "invalid script ID",
		})
		return
	}

	if err := h.svc.Delete(c.Request.Context(), userID, scriptID); err != nil {
		switch {
		case errors.Is(err, service.ErrScriptNotFound):
			c.JSON(http.StatusNotFound, gin.H{
				"code":    1206,
				"message": service.ErrScriptNotFound.Error(),
			})
		default:
			log.Printf("script delete: service error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    1203,
				"message": "internal error",
			})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
	})
}

func scriptUserID(c *gin.Context) (uint, bool) {
	value, exists := c.Get("user_id")
	if !exists {
		return 0, false
	}
	userID, ok := value.(uint)
	return userID, ok && userID > 0
}

func writeScriptUploadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrScriptFileRequired):
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1200,
			"message": service.ErrScriptFileRequired.Error(),
		})
	case errors.Is(err, service.ErrScriptFileTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"code":    1201,
			"message": service.ErrScriptFileTooLarge.Error(),
		})
	case service.IsScriptFileValidationError(err), errors.Is(err, service.ErrScriptTitleTooLong):
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1202,
			"message": safeScriptUploadMessage(err),
		})
	default:
		log.Printf("script upload: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1203,
			"message": "internal error",
		})
	}
}

func safeScriptUploadMessage(err error) string {
	switch {
	case errors.Is(err, service.ErrScriptFileExtension):
		return service.ErrScriptFileExtension.Error()
	case errors.Is(err, service.ErrScriptFileContentType):
		return service.ErrScriptFileContentType.Error()
	case errors.Is(err, service.ErrScriptFileSignature):
		return service.ErrScriptFileSignature.Error()
	case errors.Is(err, service.ErrScriptTitleTooLong):
		return service.ErrScriptTitleTooLong.Error()
	default:
		return "invalid script upload"
	}
}

const defaultHandlerPageSize = 20

func positiveQueryInt(c *gin.Context, name string, defaultValue int) (int, error) {
	value := c.Query(name)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, service.ErrInvalidPagination
	}
	return parsed, nil
}

func writePaginationError(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"code":    1204,
		"message": service.ErrInvalidPagination.Error(),
	})
}

func positivePathID(value string) (uint, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || uint64(uint(parsed)) != parsed {
		return 0, service.ErrScriptNotFound
	}
	return uint(parsed), nil
}
