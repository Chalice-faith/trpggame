package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"trpggame/internal/model"
	"trpggame/internal/service"
)

// ScriptParseStatusService 描述 Python 状态回调所需的业务能力。
type ScriptParseStatusService interface {
	UpdateParseResult(
		scriptID uint,
		status model.ScriptStatus,
		errorMessage string,
		chunkCount int,
	) error
}

// InternalScriptHandler 处理仅供受信任服务调用的剧本接口。
type InternalScriptHandler struct {
	svc ScriptParseStatusService
}

func NewInternalScriptHandler(svc ScriptParseStatusService) *InternalScriptHandler {
	return &InternalScriptHandler{svc: svc}
}

type updateScriptStatusRequest struct {
	Status       model.ScriptStatus `json:"status" binding:"required"`
	ChunkCount   int                `json:"chunk_count"`
	ErrorMessage string             `json:"error_message"`
}

// UpdateStatus 接收 Python 解析管线的最终状态。
func (h *InternalScriptHandler) UpdateStatus(c *gin.Context) {
	scriptID, err := positivePathID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1205,
			"message": "invalid script ID",
		})
		return
	}

	var request updateScriptStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1210,
			"message": "invalid script status payload",
		})
		return
	}

	err = h.svc.UpdateParseResult(
		scriptID,
		request.Status,
		request.ErrorMessage,
		request.ChunkCount,
	)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{
			"code":    0,
			"message": "ok",
		})
	case errors.Is(err, service.ErrInvalidScriptStatus),
		errors.Is(err, service.ErrInvalidParseResult):
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1210,
			"message": err.Error(),
		})
	case errors.Is(err, service.ErrScriptNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"code":    1206,
			"message": service.ErrScriptNotFound.Error(),
		})
	case errors.Is(err, service.ErrScriptStatusConflict):
		c.JSON(http.StatusConflict, gin.H{
			"code":    1211,
			"message": service.ErrScriptStatusConflict.Error(),
		})
	default:
		log.Printf("internal script status: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1203,
			"message": "internal error",
		})
	}
}
