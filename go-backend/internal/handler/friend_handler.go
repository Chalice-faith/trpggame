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

type FriendService interface {
	SearchUsers(context.Context, uint, string) ([]service.UserSearchItem, error)
	SendRequest(context.Context, uint, uint) (*service.FriendRequestItem, error)
	ListRequests(context.Context, uint, string, model.FriendshipStatus, uint, int) (*service.FriendRequestPage, error)
	RespondRequest(context.Context, uint, uint, bool) (*service.FriendRequestItem, error)
	ListFriends(context.Context, uint, uint, int) (*service.FriendPage, error)
	DeleteFriend(context.Context, uint, uint) error
}

type FriendHandler struct{ svc FriendService }

func NewFriendHandler(svc FriendService) *FriendHandler { return &FriendHandler{svc: svc} }

type sendFriendRequest struct {
	TargetUserID uint `json:"target_user_id" binding:"required,gt=0"`
}

func (h *FriendHandler) SearchUsers(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		friendError(c, service.ErrInvalidFriendRequest)
		return
	}
	items, err := h.svc.SearchUsers(c.Request.Context(), userID, c.Query("keyword"))
	if err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": gin.H{"items": items}})
}

func (h *FriendHandler) SendRequest(c *gin.Context) {
	userID, ok := gameUserID(c)
	var request sendFriendRequest
	if !ok || c.ShouldBindJSON(&request) != nil {
		friendError(c, service.ErrInvalidFriendRequest)
		return
	}
	item, err := h.svc.SendRequest(c.Request.Context(), userID, request.TargetUserID)
	if err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": item})
}

func (h *FriendHandler) ListRequests(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		friendError(c, service.ErrInvalidFriendRequest)
		return
	}
	direction := strings.TrimSpace(c.DefaultQuery("direction", "incoming"))
	status := model.FriendshipStatus(strings.TrimSpace(c.DefaultQuery("status", "pending")))
	cursor, limit, err := friendPagination(c, 20)
	if err != nil {
		friendError(c, err)
		return
	}
	page, err := h.svc.ListRequests(c.Request.Context(), userID, direction, status, cursor, limit)
	if err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": page})
}

func (h *FriendHandler) AcceptRequest(c *gin.Context) { h.respondRequest(c, true) }
func (h *FriendHandler) RejectRequest(c *gin.Context) { h.respondRequest(c, false) }

func (h *FriendHandler) respondRequest(c *gin.Context, accept bool) {
	userID, ok := gameUserID(c)
	requestID, idOK := positiveUint(c.Param("requestId"))
	if !ok || !idOK {
		friendError(c, service.ErrInvalidFriendRequest)
		return
	}
	item, err := h.svc.RespondRequest(c.Request.Context(), userID, requestID, accept)
	if err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": item})
}

func (h *FriendHandler) ListFriends(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		friendError(c, service.ErrInvalidFriendRequest)
		return
	}
	cursor, limit, err := friendPagination(c, 50)
	if err != nil {
		friendError(c, err)
		return
	}
	page, err := h.svc.ListFriends(c.Request.Context(), userID, cursor, limit)
	if err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": page})
}

func (h *FriendHandler) DeleteFriend(c *gin.Context) {
	userID, ok := gameUserID(c)
	friendUserID, idOK := positiveUint(c.Param("friendUserId"))
	if !ok || !idOK {
		friendError(c, service.ErrInvalidFriendRequest)
		return
	}
	if err := h.svc.DeleteFriend(c.Request.Context(), userID, friendUserID); err != nil {
		friendError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok"})
}

func friendPagination(c *gin.Context, defaultLimit int) (uint, int, error) {
	cursor := uint(0)
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		parsed, ok := positiveUint(raw)
		if !ok {
			return 0, 0, service.ErrInvalidFriendQuery
		}
		cursor = parsed
	}
	limit := defaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, 0, service.ErrInvalidFriendQuery
		}
		limit = parsed
	}
	return cursor, limit, nil
}

func positiveUint(raw string) (uint, bool) {
	value, err := strconv.ParseUint(raw, 10, 64)
	return uint(value), err == nil && value > 0 && uint64(uint(value)) == value
}

func friendError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, 1699, "internal error"
	switch {
	case errors.Is(err, service.ErrInvalidFriendRequest):
		status, code, message = http.StatusBadRequest, 1600, "invalid friend request"
	case errors.Is(err, service.ErrFriendTargetNotFound):
		status, code, message = http.StatusNotFound, 1601, "friend target not found"
	case errors.Is(err, service.ErrCannotFriendSelf):
		status, code, message = http.StatusBadRequest, 1602, "cannot add yourself as a friend"
	case errors.Is(err, service.ErrFriendRequestMissing):
		status, code, message = http.StatusNotFound, 1603, "friend request not found"
	case errors.Is(err, service.ErrFriendForbidden):
		status, code, message = http.StatusForbidden, 1604, "friend operation forbidden"
	case errors.Is(err, service.ErrFriendStateConflict):
		status, code, message = http.StatusConflict, 1605, "friendship state conflict"
	case errors.Is(err, service.ErrFriendshipNotFound):
		status, code, message = http.StatusNotFound, 1606, "friendship not found"
	case errors.Is(err, service.ErrInvalidFriendQuery):
		status, code, message = http.StatusBadRequest, 1607, "invalid pagination or cursor"
	default:
		log.Printf("friend handler: %v", err)
	}
	c.JSON(status, gin.H{"code": code, "message": message})
}
