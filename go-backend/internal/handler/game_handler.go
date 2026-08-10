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
	CreateManualSave(
		ctx context.Context,
		req *service.CreateManualSaveRequest,
	) (*service.CreateManualSaveResult, error)
	ListGameSaves(
		ctx context.Context,
		req *service.ListGameSavesRequest,
	) (*service.ListGameSavesResult, error)
	PauseGame(
		ctx context.Context,
		req *service.PauseGameRequest,
	) (*service.PauseGameResult, error)
	ResumeGame(
		ctx context.Context,
		req *service.ResumeGameRequest,
	) (*service.ResumeGameResult, error)
	LoadGame(
		ctx context.Context,
		req *service.LoadGameRequest,
	) (*service.LoadGameResult, error)
	EndGame(
		ctx context.Context,
		req *service.EndGameRequest,
	) (*service.EndGameResult, error)
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

type manualSaveRequest struct {
	SaveName string `json:"save_name" binding:"required"`
}

type manualSaveResponse struct {
	SaveID uint `json:"save_id"`
}

type listGameSavesResponse struct {
	Items []service.GameSaveSummary `json:"items"`
	Total int                       `json:"total"`
}

type pauseGameResponse struct {
	RoomID uint             `json:"room_id"`
	Status model.RoomStatus `json:"status"`
}

type resumeGameResponse struct {
	RoomID uint             `json:"room_id"`
	Status model.RoomStatus `json:"status"`
}

type loadGameRequest struct {
	SaveID uint `json:"save_id" binding:"required,gt=0"`
}

type loadGameResponse struct {
	RoomID uint             `json:"room_id"`
	SaveID uint             `json:"save_id"`
	Status model.RoomStatus `json:"status"`
	Turn   int              `json:"turn"`
}

type endGameResponse struct {
	RoomID uint             `json:"room_id"`
	Status model.RoomStatus `json:"status"`
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
	roomID, ok := gameRoomID(c)
	if !ok {
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
		RoomID:       roomID,
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

// ManualSave 创建手动存档。
func (h *GameHandler) ManualSave(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}
	roomID, ok := gameRoomID(c)
	if !ok {
		writeManualSaveError(c, service.ErrInvalidGameSave)
		return
	}

	var request manualSaveRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeManualSaveError(c, service.ErrInvalidGameSave)
		return
	}
	result, err := h.svc.CreateManualSave(c.Request.Context(), &service.CreateManualSaveRequest{
		UserID:   userID,
		RoomID:   roomID,
		SaveName: request.SaveName,
	})
	if err != nil {
		writeManualSaveError(c, err)
		return
	}
	if result == nil || result.Save == nil || result.Save.ID == 0 ||
		result.Save.RoomID != roomID || result.Save.IsAuto {
		log.Print("create manual game save: service returned an invalid result")
		writeManualSaveError(c, service.ErrInternal)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"code":    0,
		"message": "ok",
		"data": manualSaveResponse{
			SaveID: result.Save.ID,
		},
	})
}

func writeManualSaveError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidGameSave):
		c.JSON(http.StatusBadRequest, gin.H{"code": 1321, "message": service.ErrInvalidGameSave.Error()})
	case errors.Is(err, service.ErrGameRoomNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1311, "message": service.ErrGameRoomNotFound.Error()})
	case errors.Is(err, service.ErrGameRoomNotSavable):
		c.JSON(http.StatusConflict, gin.H{"code": 1322, "message": service.ErrGameRoomNotSavable.Error()})
	case errors.Is(err, service.ErrGameRuntimeUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 1318, "message": service.ErrGameRuntimeUnavailable.Error()})
	default:
		log.Printf("create manual game save: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1323, "message": "internal error"})
	}
}

func gameRoomID(c *gin.Context) (uint, bool) {
	value, err := strconv.ParseUint(c.Param("roomId"), 10, 64)
	return uint(value), err == nil && value > 0 && uint64(uint(value)) == value
}

// ListSaves 返回当前用户拥有房间的存档摘要列表。
func (h *GameHandler) ListSaves(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}
	roomID, ok := gameRoomID(c)
	if !ok {
		writeListGameSavesError(c, service.ErrInvalidGameSaveQuery)
		return
	}

	result, err := h.svc.ListGameSaves(c.Request.Context(), &service.ListGameSavesRequest{
		UserID: userID,
		RoomID: roomID,
	})
	if err != nil {
		writeListGameSavesError(c, err)
		return
	}
	if !validListGameSavesResult(result) {
		log.Print("list game saves: service returned an invalid result")
		writeListGameSavesError(c, service.ErrInternal)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": listGameSavesResponse{
			Items: result.Items,
			Total: result.Total,
		},
	})
}

func validListGameSavesResult(result *service.ListGameSavesResult) bool {
	if result == nil || result.Items == nil || result.Total != len(result.Items) {
		return false
	}
	for _, item := range result.Items {
		if item.ID == 0 || strings.TrimSpace(item.SaveName) == "" ||
			item.RoundNumber < 0 || item.CreatedAt.IsZero() {
			return false
		}
	}
	return true
}

func writeListGameSavesError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidGameSaveQuery):
		c.JSON(http.StatusBadRequest, gin.H{"code": 1324, "message": service.ErrInvalidGameSaveQuery.Error()})
	case errors.Is(err, service.ErrGameRoomNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1311, "message": service.ErrGameRoomNotFound.Error()})
	default:
		log.Printf("list game saves: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1325, "message": "internal error"})
	}
}

// LoadGame 从房间存档恢复单人运行态，恢复完成后保持暂停。
func (h *GameHandler) LoadGame(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}
	roomID, ok := gameRoomID(c)
	if !ok {
		writeLoadGameError(c, service.ErrInvalidGameLoad)
		return
	}
	var request loadGameRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeLoadGameError(c, service.ErrInvalidGameLoad)
		return
	}

	result, err := h.svc.LoadGame(c.Request.Context(), &service.LoadGameRequest{
		UserID: userID,
		RoomID: roomID,
		SaveID: request.SaveID,
	})
	if err != nil {
		writeLoadGameError(c, err)
		return
	}
	if result == nil || result.RoomID != roomID || result.SaveID != request.SaveID ||
		result.Status != model.RoomStatusPaused || result.Turn < 0 {
		log.Print("load game: service returned an invalid result")
		writeLoadGameError(c, service.ErrInternal)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": loadGameResponse{
			RoomID: result.RoomID,
			SaveID: result.SaveID,
			Status: result.Status,
			Turn:   result.Turn,
		},
	})
}

func writeLoadGameError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidGameLoad):
		c.JSON(http.StatusBadRequest, gin.H{"code": 1332, "message": service.ErrInvalidGameLoad.Error()})
	case errors.Is(err, service.ErrGameRoomNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1311, "message": service.ErrGameRoomNotFound.Error()})
	case errors.Is(err, service.ErrGameSaveNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1333, "message": service.ErrGameSaveNotFound.Error()})
	case errors.Is(err, service.ErrGameSaveCorrupt):
		c.JSON(http.StatusConflict, gin.H{"code": 1334, "message": service.ErrGameSaveCorrupt.Error()})
	case errors.Is(err, service.ErrGameRoomNotLoadable):
		c.JSON(http.StatusConflict, gin.H{"code": 1335, "message": service.ErrGameRoomNotLoadable.Error()})
	case errors.Is(err, service.ErrGameRuntimeUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 1318, "message": service.ErrGameRuntimeUnavailable.Error()})
	default:
		log.Printf("load game: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1336, "message": "internal error"})
	}
}

// PauseGame 暂停单人游戏。
func (h *GameHandler) PauseGame(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}
	roomID, ok := gameRoomID(c)
	if !ok {
		writePauseGameError(c, service.ErrInvalidGamePause)
		return
	}

	result, err := h.svc.PauseGame(c.Request.Context(), &service.PauseGameRequest{
		UserID: userID,
		RoomID: roomID,
	})
	if err != nil {
		writePauseGameError(c, err)
		return
	}
	if result == nil || result.RoomID != roomID || result.Status != model.RoomStatusPaused {
		log.Print("pause game: service returned an invalid result")
		writePauseGameError(c, service.ErrInternal)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": pauseGameResponse{
			RoomID: result.RoomID,
			Status: result.Status,
		},
	})
}

func writePauseGameError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidGamePause):
		c.JSON(http.StatusBadRequest, gin.H{"code": 1326, "message": service.ErrInvalidGamePause.Error()})
	case errors.Is(err, service.ErrGameRoomNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1311, "message": service.ErrGameRoomNotFound.Error()})
	case errors.Is(err, service.ErrGameRoomNotPausable):
		c.JSON(http.StatusConflict, gin.H{"code": 1327, "message": service.ErrGameRoomNotPausable.Error()})
	case errors.Is(err, service.ErrGameRuntimeUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 1318, "message": service.ErrGameRuntimeUnavailable.Error()})
	default:
		log.Printf("pause game: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1328, "message": "internal error"})
	}
}

// ResumeGame 恢复单人游戏。
func (h *GameHandler) ResumeGame(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}
	roomID, ok := gameRoomID(c)
	if !ok {
		writeResumeGameError(c, service.ErrInvalidGameResume)
		return
	}

	result, err := h.svc.ResumeGame(c.Request.Context(), &service.ResumeGameRequest{
		UserID: userID,
		RoomID: roomID,
	})
	if err != nil {
		writeResumeGameError(c, err)
		return
	}
	if result == nil || result.RoomID != roomID || result.Status != model.RoomStatusPlaying {
		log.Print("resume game: service returned an invalid result")
		writeResumeGameError(c, service.ErrInternal)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": resumeGameResponse{
			RoomID: result.RoomID,
			Status: result.Status,
		},
	})
}

func writeResumeGameError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidGameResume):
		c.JSON(http.StatusBadRequest, gin.H{"code": 1329, "message": service.ErrInvalidGameResume.Error()})
	case errors.Is(err, service.ErrGameRoomNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1311, "message": service.ErrGameRoomNotFound.Error()})
	case errors.Is(err, service.ErrGameRoomNotResumable):
		c.JSON(http.StatusConflict, gin.H{"code": 1330, "message": service.ErrGameRoomNotResumable.Error()})
	case errors.Is(err, service.ErrGameRuntimeUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 1318, "message": service.ErrGameRuntimeUnavailable.Error()})
	default:
		log.Printf("resume game: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1331, "message": "internal error"})
	}
}

// EndGame 结束单人游戏并触发运行态清理。
func (h *GameHandler) EndGame(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1002,
			"message": "invalid authentication context",
		})
		return
	}
	roomID, ok := gameRoomID(c)
	if !ok {
		writeEndGameError(c, service.ErrInvalidGameEnd)
		return
	}

	result, err := h.svc.EndGame(c.Request.Context(), &service.EndGameRequest{
		UserID: userID,
		RoomID: roomID,
	})
	if err != nil {
		writeEndGameError(c, err)
		return
	}
	if result == nil || result.RoomID != roomID || result.Status != model.RoomStatusEnded {
		log.Print("end game: service returned an invalid result")
		writeEndGameError(c, service.ErrInternal)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data": endGameResponse{
			RoomID: result.RoomID,
			Status: result.Status,
		},
	})
}

func writeEndGameError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidGameEnd):
		c.JSON(http.StatusBadRequest, gin.H{"code": 1337, "message": service.ErrInvalidGameEnd.Error()})
	case errors.Is(err, service.ErrGameRoomNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": 1311, "message": service.ErrGameRoomNotFound.Error()})
	case errors.Is(err, service.ErrGameRoomNotEndable):
		c.JSON(http.StatusConflict, gin.H{"code": 1338, "message": service.ErrGameRoomNotEndable.Error()})
	case errors.Is(err, service.ErrGameRuntimeUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": 1318, "message": service.ErrGameRuntimeUnavailable.Error()})
	default:
		log.Printf("end game: service error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": 1339, "message": "internal error"})
	}
}
