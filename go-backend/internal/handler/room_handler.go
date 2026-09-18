package handler

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"trpggame/internal/repo"
	"trpggame/internal/service"
)

type RoomService interface {
	Create(context.Context, uint, uint, string, int) (*service.RoomSnapshot, error)
	List(context.Context, uint) ([]service.RoomSummary, error)
	Get(context.Context, uint, uint) (*service.RoomSnapshot, error)
	Join(context.Context, uint, string) (*service.RoomSnapshot, error)
	SelectCharacter(context.Context, uint, uint, uint, uint64) (*service.RoomSnapshot, error)
	SetReady(context.Context, uint, uint, bool, uint64) (*service.RoomSnapshot, error)
	Leave(context.Context, uint, uint, uint64) (*service.RoomSnapshot, error)
	Remove(context.Context, uint, uint, uint, uint64) (*service.RoomSnapshot, error)
	Transfer(context.Context, uint, uint, uint, uint64) (*service.RoomSnapshot, error)
	Start(context.Context, uint, uint, uint64) (*service.RoomSnapshot, error)
}

type RoomHandler struct{ svc RoomService }

func NewRoomHandler(svc RoomService) *RoomHandler { return &RoomHandler{svc: svc} }

type createRoomRequest struct {
	Name       string `json:"name" binding:"required"`
	ScriptID   uint   `json:"script_id" binding:"required"`
	MaxPlayers int    `json:"max_players" binding:"required"`
}

type joinRoomRequest struct {
	RoomCode string `json:"room_code" binding:"required"`
}

type selectRoomCharacterRequest struct {
	CharacterID     uint    `json:"character_id" binding:"required"`
	ExpectedVersion *uint64 `json:"expected_version" binding:"required"`
}
type readyRoomRequest struct {
	Ready           *bool   `json:"ready" binding:"required"`
	ExpectedVersion *uint64 `json:"expected_version" binding:"required"`
}
type roomVersionRequest struct {
	ExpectedVersion *uint64 `json:"expected_version" binding:"required"`
}
type transferRoomRequest struct {
	NewOwnerUserID  uint    `json:"new_owner_user_id" binding:"required"`
	ExpectedVersion *uint64 `json:"expected_version" binding:"required"`
}

func (h *RoomHandler) Create(c *gin.Context) {
	userID, ok := gameUserID(c)
	var request createRoomRequest
	if !ok || c.ShouldBindJSON(&request) != nil {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.Create(c.Request.Context(), userID, request.ScriptID, request.Name, request.MaxPlayers)
	roomResult(c, result, err)
}

func (h *RoomHandler) List(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.List(c.Request.Context(), userID)
	roomResult(c, result, err)
}

func (h *RoomHandler) Get(c *gin.Context) {
	userID, ok := gameUserID(c)
	roomID, valid := positiveUint(c.Param("roomId"))
	if !ok || !valid {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.Get(c.Request.Context(), userID, roomID)
	roomResult(c, result, err)
}

func (h *RoomHandler) Join(c *gin.Context) {
	userID, ok := gameUserID(c)
	var request joinRoomRequest
	if !ok || c.ShouldBindJSON(&request) != nil {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.Join(c.Request.Context(), userID, request.RoomCode)
	roomResult(c, result, err)
}

func roomPath(c *gin.Context) (uint, uint, bool) {
	actorID, authenticated := gameUserID(c)
	roomID, valid := positiveUint(c.Param("roomId"))
	return actorID, roomID, authenticated && valid
}

func (h *RoomHandler) SelectCharacter(c *gin.Context) {
	actorID, roomID, ok := roomPath(c)
	var request selectRoomCharacterRequest
	if !ok || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.SelectCharacter(c.Request.Context(), actorID, roomID, request.CharacterID, *request.ExpectedVersion)
	roomResult(c, result, err)
}

func (h *RoomHandler) SetReady(c *gin.Context) {
	actorID, roomID, ok := roomPath(c)
	var request readyRoomRequest
	if !ok || c.ShouldBindJSON(&request) != nil || request.Ready == nil || request.ExpectedVersion == nil {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.SetReady(c.Request.Context(), actorID, roomID, *request.Ready, *request.ExpectedVersion)
	roomResult(c, result, err)
}

func (h *RoomHandler) Leave(c *gin.Context) {
	actorID, roomID, ok := roomPath(c)
	var request roomVersionRequest
	if !ok || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.Leave(c.Request.Context(), actorID, roomID, *request.ExpectedVersion)
	roomResult(c, result, err)
}

func (h *RoomHandler) Remove(c *gin.Context) {
	actorID, roomID, ok := roomPath(c)
	targetID, valid := positiveUint(c.Param("userId"))
	var request roomVersionRequest
	if !ok || !valid || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.Remove(c.Request.Context(), actorID, roomID, targetID, *request.ExpectedVersion)
	roomResult(c, result, err)
}

func (h *RoomHandler) Transfer(c *gin.Context) {
	actorID, roomID, ok := roomPath(c)
	var request transferRoomRequest
	if !ok || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.Transfer(c.Request.Context(), actorID, roomID, request.NewOwnerUserID, *request.ExpectedVersion)
	roomResult(c, result, err)
}

func (h *RoomHandler) Start(c *gin.Context) {
	actorID, roomID, ok := roomPath(c)
	var request roomVersionRequest
	if !ok || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		roomError(c, service.ErrInvalidRoomRequest)
		return
	}
	result, err := h.svc.Start(c.Request.Context(), actorID, roomID, *request.ExpectedVersion)
	roomResult(c, result, err)
}

func roomResult(c *gin.Context, result any, err error) {
	if err != nil {
		roomError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": result})
}

func roomError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, 1907, "room unavailable"
	switch {
	case errors.Is(err, service.ErrInvalidRoomRequest):
		status, code, message = http.StatusBadRequest, 1900, "invalid room request"
	case errors.Is(err, service.ErrRoomNotFound):
		status, code, message = http.StatusNotFound, 1901, "room not found"
	case errors.Is(err, service.ErrRoomScriptUnavailable):
		status, code, message = http.StatusConflict, 1902, "room script unavailable"
	case errors.Is(err, service.ErrRoomCharactersInsufficient):
		status, code, message = http.StatusConflict, 1903, "not enough script characters"
	case errors.Is(err, service.ErrRoomCapacityReached):
		status, code, message = http.StatusConflict, 1904, "room capacity reached"
	case errors.Is(err, service.ErrRoomNotWaiting):
		status, code, message = http.StatusConflict, 1905, "room not waiting"
	case errors.Is(err, service.ErrRoomRejoinDenied):
		status, code, message = http.StatusForbidden, 1906, "room rejoin denied"
	case errors.Is(err, repo.ErrRoomVersionConflict):
		var conflict *repo.RoomVersionError
		if errors.As(err, &conflict) {
			c.JSON(http.StatusConflict, gin.H{"code": 1908, "message": "room version conflict", "current_version": conflict.Current})
			return
		}
		status, code, message = http.StatusConflict, 1908, "room version conflict"
	case errors.Is(err, service.ErrRoomPermissionDenied):
		status, code, message = http.StatusForbidden, 1909, "room permission denied"
	case errors.Is(err, service.ErrRoomInvalidCharacter):
		status, code, message = http.StatusBadRequest, 1910, "invalid room character"
	case errors.Is(err, service.ErrRoomCharacterTaken):
		status, code, message = http.StatusConflict, 1911, "room character taken"
	case errors.Is(err, service.ErrRoomStartConditions):
		status, code, message = http.StatusConflict, 1912, "room start conditions unmet"
	case errors.Is(err, service.ErrRoomOwnerLeave):
		status, code, message = http.StatusConflict, 1913, "owner must transfer before leaving"
	default:
		log.Printf("room handler: %v", err)
	}
	c.JSON(status, gin.H{"code": code, "message": message})
}
