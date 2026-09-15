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

type GroupService interface {
	Create(context.Context, uint, string, string) (*service.GroupSummary, error)
	List(context.Context, uint, string, int) (*service.GroupPage, error)
	Get(context.Context, uint, uint) (*service.GroupSummary, error)
	ListMembers(context.Context, uint, uint, string, int) (*service.GroupMemberPage, error)
	Update(context.Context, uint, uint, uint64, *string, *string) (*service.GroupSummary, error)
	Invite(context.Context, uint, uint, []uint, uint64) (*service.GroupSummary, error)
	SetRole(context.Context, uint, uint, uint, model.GroupRole, uint64) (*service.GroupSummary, error)
	Remove(context.Context, uint, uint, uint, uint64) (*service.GroupSummary, error)
	Transfer(context.Context, uint, uint, uint, uint64) (*service.GroupSummary, error)
}

type GroupHandler struct{ svc GroupService }

func NewGroupHandler(svc GroupService) *GroupHandler { return &GroupHandler{svc: svc} }

type createGroupRequest struct {
	Name      string `json:"name" binding:"required"`
	AvatarURL string `json:"avatar_url"`
}

type updateGroupRequest struct {
	Name            *string `json:"name"`
	AvatarURL       *string `json:"avatar_url"`
	ExpectedVersion *uint64 `json:"expected_version" binding:"required"`
}

type inviteGroupMembersRequest struct {
	UserIDs         []uint  `json:"user_ids" binding:"required"`
	ExpectedVersion *uint64 `json:"expected_version" binding:"required"`
}

type setGroupMemberRoleRequest struct {
	Role            model.GroupRole `json:"role" binding:"required"`
	ExpectedVersion *uint64         `json:"expected_version" binding:"required"`
}

type transferGroupRequest struct {
	NewOwnerUserID  uint    `json:"new_owner_user_id" binding:"required"`
	ExpectedVersion *uint64 `json:"expected_version" binding:"required"`
}

func (h *GroupHandler) Create(c *gin.Context) {
	userID, ok := gameUserID(c)
	var request createGroupRequest
	if !ok || c.ShouldBindJSON(&request) != nil {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.Create(c.Request.Context(), userID, request.Name, request.AvatarURL)
	groupResult(c, result, err)
}

func (h *GroupHandler) List(c *gin.Context) {
	userID, ok := gameUserID(c)
	limit, err := groupLimit(c, 20)
	if !ok || err != nil {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.List(c.Request.Context(), userID, strings.TrimSpace(c.Query("cursor")), limit)
	groupResult(c, result, err)
}

func (h *GroupHandler) Get(c *gin.Context) {
	userID, groupID, ok := groupPath(c)
	if !ok {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.Get(c.Request.Context(), userID, groupID)
	groupResult(c, result, err)
}

func (h *GroupHandler) Update(c *gin.Context) {
	userID, groupID, ok := groupPath(c)
	var request updateGroupRequest
	if !ok || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.Update(c.Request.Context(), userID, groupID, *request.ExpectedVersion, request.Name, request.AvatarURL)
	groupResult(c, result, err)
}

func (h *GroupHandler) ListMembers(c *gin.Context) {
	userID, groupID, ok := groupPath(c)
	limit, err := groupLimit(c, 50)
	if !ok || err != nil {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.ListMembers(c.Request.Context(), userID, groupID, strings.TrimSpace(c.Query("cursor")), limit)
	groupResult(c, result, err)
}

func (h *GroupHandler) Invite(c *gin.Context) {
	userID, groupID, ok := groupPath(c)
	var request inviteGroupMembersRequest
	if !ok || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.Invite(c.Request.Context(), userID, groupID, request.UserIDs, *request.ExpectedVersion)
	groupResult(c, result, err)
}

func (h *GroupHandler) SetRole(c *gin.Context) {
	userID, groupID, ok := groupPath(c)
	targetID, targetOK := positiveUint(c.Param("userId"))
	var request setGroupMemberRoleRequest
	if !ok || !targetOK || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.SetRole(c.Request.Context(), userID, groupID, targetID, request.Role, *request.ExpectedVersion)
	groupResult(c, result, err)
}

func (h *GroupHandler) Remove(c *gin.Context) {
	userID, groupID, ok := groupPath(c)
	targetID, targetOK := positiveUint(c.Param("userId"))
	version, versionOK := positiveUint64(c.Query("expected_version"))
	if !ok || !targetOK || !versionOK {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.Remove(c.Request.Context(), userID, groupID, targetID, version)
	groupResult(c, result, err)
}

func (h *GroupHandler) Transfer(c *gin.Context) {
	userID, groupID, ok := groupPath(c)
	var request transferGroupRequest
	if !ok || c.ShouldBindJSON(&request) != nil || request.ExpectedVersion == nil {
		groupError(c, service.ErrInvalidGroupRequest)
		return
	}
	result, err := h.svc.Transfer(c.Request.Context(), userID, groupID, request.NewOwnerUserID, *request.ExpectedVersion)
	groupResult(c, result, err)
}

func groupPath(c *gin.Context) (uint, uint, bool) {
	userID, userOK := gameUserID(c)
	groupID, groupOK := positiveUint(c.Param("groupId"))
	return userID, groupID, userOK && groupOK
}

func groupLimit(c *gin.Context, fallback int) (int, error) {
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return 0, service.ErrInvalidGroupRequest
		}
		return value, nil
	}
	return fallback, nil
}

func positiveUint64(raw string) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	return value, err == nil && value > 0
}

func groupResult(c *gin.Context, result any, err error) {
	if err != nil {
		groupError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": result})
}

func groupError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, 1807, "group unavailable"
	switch {
	case errors.Is(err, service.ErrInvalidGroupRequest):
		status, code, message = http.StatusBadRequest, 1800, "invalid group request"
	case errors.Is(err, service.ErrGroupNotFound):
		status, code, message = http.StatusNotFound, 1801, "group not found"
	case errors.Is(err, service.ErrGroupPermissionDenied):
		status, code, message = http.StatusForbidden, 1802, "group permission denied"
	case errors.Is(err, service.ErrGroupFriendshipRequired):
		status, code, message = http.StatusConflict, 1803, "group friendship required"
	case errors.Is(err, service.ErrGroupMemberLimitReached):
		status, code, message = http.StatusConflict, 1804, "group member limit reached"
	case errors.Is(err, service.ErrGroupOwnerConflict):
		status, code, message = http.StatusConflict, 1805, "group owner conflict"
	case errors.Is(err, service.ErrGroupVersionConflict):
		status, code, message = http.StatusConflict, 1806, "group version conflict"
	default:
		log.Printf("group handler: %v", err)
	}
	c.JSON(status, gin.H{"code": code, "message": message})
}
