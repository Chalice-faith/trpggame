package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"trpggame/internal/model"
	"trpggame/internal/service"
)

// GameService 描述游戏 Handler 所需的服务能力。
type GameService interface {
	StartSoloGame(
		ctx context.Context,
		req *service.StartSoloGameRequest,
	) (*service.StartSoloGameResult, error)
	SubmitAction(
		ctx context.Context,
		req *service.SubmitGameActionRequest,
	) (*service.SubmitGameActionResult, error)
}

// GameHandler 游戏相关 HTTP 处理器。
type GameHandler struct {
	svc GameService
}

// NewGameHandler 创建 GameHandler。
func NewGameHandler(svc GameService) *GameHandler {
	return &GameHandler{svc: svc}
}

type submitActionRequest struct {
	RequestID    string `json:"request_id" binding:"required"`
	ExpectedTurn *int   `json:"expected_turn" binding:"required,gte=0"`
	ActionText   string `json:"action_text" binding:"required"`
}

type submitActionResponse struct {
	Narrative   string                  `json:"narrative"`
	DiceRoll    *service.ActionDiceRoll `json:"dice_roll,omitempty"`
	Effects     *service.ActionEffects  `json:"effects"`
	CurrentTurn int                     `json:"current_turn"`
}

type startSoloGameRequest struct {
	ScriptID    uint `json:"script_id" binding:"required,gt=0"`
	CharacterID uint `json:"character_id" binding:"required,gt=0"`
}

type startSoloGameResponse struct {
	RoomID           uint             `json:"room_id"`
	GameStatus       model.RoomStatus `json:"game_status"`
	OpeningNarrative string           `json:"opening_narrative"`
}

// StartSoloGame 单人快速开始。
func (h *GameHandler) StartSoloGame(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}

	var request startSoloGameRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1300,
			"message": service.ErrInvalidGameRequest.Error(),
		})
		return
	}

	result, err := h.svc.StartSoloGame(
		c.Request.Context(),
		&service.StartSoloGameRequest{
			UserID:      userID,
			ScriptID:    request.ScriptID,
			CharacterID: request.CharacterID,
		},
	)
	if err != nil {
		writeStartSoloGameError(c, err)
		return
	}
	if result == nil || result.Room == nil || result.Room.ID == 0 ||
		result.Room.Status != model.RoomStatusPlaying ||
		strings.TrimSpace(result.OpeningNarrative) == "" {
		log.Print("start solo game: service returned an invalid result")
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1306,
			"message": "internal error",
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"code":    0,
		"message": "ok",
		"data": startSoloGameResponse{
			RoomID:           result.Room.ID,
			GameStatus:       result.Room.Status,
			OpeningNarrative: strings.TrimSpace(result.OpeningNarrative),
		},
	})
}

func writeStartSoloGameError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidGameRequest):
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1300,
			"message": service.ErrInvalidGameRequest.Error(),
		})
	case errors.Is(err, service.ErrScriptNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"code":    1301,
			"message": service.ErrScriptNotFound.Error(),
		})
	case errors.Is(err, service.ErrScriptNotReady):
		c.JSON(http.StatusConflict, gin.H{
			"code":    1302,
			"message": service.ErrScriptNotReady.Error(),
		})
	case errors.Is(err, service.ErrCharacterNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"code":    1303,
			"message": service.ErrCharacterNotFound.Error(),
		})
	case errors.Is(err, service.ErrAIUnavailable),
		errors.Is(err, service.ErrEmptyOpeningNarrative):
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":    1304,
			"message": "AI opening generation unavailable",
		})
	case errors.Is(err, service.ErrGameStartConflict):
		c.JSON(http.StatusConflict, gin.H{
			"code":    1305,
			"message": service.ErrGameStartConflict.Error(),
		})
	default:
		log.Printf("start solo game: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1306,
			"message": "internal error",
		})
	}
}

func gameUserID(c *gin.Context) (uint, bool) {
	value, exists := c.Get("user_id")
	if !exists {
		return 0, false
	}
	userID, ok := value.(uint)
	return userID, ok && userID > 0
}

// SubmitAction 提交玩家行动。
func (h *GameHandler) SubmitAction(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}
	roomID64, err := strconv.ParseUint(c.Param("roomId"), 10, 64)
	if err != nil || roomID64 == 0 || uint64(uint(roomID64)) != roomID64 {
		writeSubmitActionError(c, service.ErrInvalidGameAction)
		return
	}

	var request submitActionRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.ExpectedTurn == nil {
		writeSubmitActionError(c, service.ErrInvalidGameAction)
		return
	}

	result, err := h.svc.SubmitAction(c.Request.Context(), &service.SubmitGameActionRequest{
		UserID:       userID,
		RoomID:       uint(roomID64),
		RequestID:    request.RequestID,
		ExpectedTurn: *request.ExpectedTurn,
		Action:       request.ActionText,
	})
	if err != nil {
		writeSubmitActionError(c, err)
		return
	}
	if result == nil || strings.TrimSpace(result.Narrative) == "" ||
		result.Effects == nil || result.CurrentTurn <= 0 {
		log.Print("submit game action: service returned an invalid result")
		writeSubmitActionError(c, service.ErrInternal)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": submitActionResponse{
			Narrative:   strings.TrimSpace(result.Narrative),
			DiceRoll:    result.DiceRoll,
			Effects:     result.Effects,
			CurrentTurn: result.CurrentTurn,
		},
	})
}

func writeSubmitActionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidGameAction):
		c.JSON(http.StatusBadRequest, gin.H{"code": 1310, "message": service.ErrInvalidGameAction.Error()})
	case errors.Is(err, service.ErrGameRoomNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1311, "message": service.ErrGameRoomNotFound.Error()})
	case errors.Is(err, service.ErrGamePlayerNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1312, "message": service.ErrGamePlayerNotFound.Error()})
	case errors.Is(err, service.ErrGameRoomNotPlaying):
		c.JSON(http.StatusConflict, gin.H{"code": 1313, "message": service.ErrGameRoomNotPlaying.Error()})
	case errors.Is(err, service.ErrGameActionConflict):
		c.JSON(http.StatusConflict, gin.H{"code": 1314, "message": service.ErrGameActionConflict.Error()})
	case errors.Is(err, service.ErrActionRequestConflict):
		c.JSON(http.StatusConflict, gin.H{"code": 1315, "message": service.ErrActionRequestConflict.Error()})
	case errors.Is(err, service.ErrInsufficientItems):
		c.JSON(http.StatusConflict, gin.H{"code": 1316, "message": service.ErrInsufficientItems.Error()})
	case errors.Is(err, service.ErrAIUnavailable), errors.Is(err, service.ErrEmptyActionNarrative):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 1317, "message": "AI action generation unavailable"})
	case errors.Is(err, service.ErrGameRuntimeUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 1318, "message": service.ErrGameRuntimeUnavailable.Error()})
	case errors.Is(err, service.ErrInvalidActionEffects):
		c.JSON(http.StatusBadGateway, gin.H{"code": 1319, "message": "AI returned invalid action effects"})
	default:
		log.Printf("submit game action: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1320, "message": "internal error"})
	}
}

// ManualSave 手动存档
func ManualSave(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - ManualSave",
	})
}

// ListSaves 存档列表
func ListSaves(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - ListSaves",
	})
}

// LoadGame 读档恢复
func LoadGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - LoadGame",
	})
}

// PauseGame 暂停游戏
func PauseGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - PauseGame",
	})
}

// ResumeGame 恢复游戏
func ResumeGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - ResumeGame",
	})
}

// EndGame 结束游戏
func EndGame(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "Not implemented yet - EndGame",
	})
}
