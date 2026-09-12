package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"trpggame/internal/service"
)

type ChatService interface {
	CreateDirect(context.Context, uint, uint) (*service.ConversationSummary, error)
	ListConversations(context.Context, uint, string, int) (*service.ConversationPage, error)
	ListMessages(context.Context, uint, uint, uint64, int) (*service.MessagePage, error)
	MarkRead(context.Context, uint, uint, uint64) (*service.ReadResult, error)
}

type ChatHandler struct{ svc ChatService }

func NewChatHandler(svc ChatService) *ChatHandler { return &ChatHandler{svc: svc} }

type createDirectConversationRequest struct {
	PeerUserID uint `json:"peer_user_id" binding:"required,gt=0"`
}

type markConversationReadRequest struct {
	LastReadSeq *uint64 `json:"last_read_seq" binding:"required"`
}

func (h *ChatHandler) CreateDirect(c *gin.Context) {
	userID, ok := gameUserID(c)
	var request createDirectConversationRequest
	if !ok || c.ShouldBindJSON(&request) != nil {
		chatError(c, service.ErrInvalidConversationRequest)
		return
	}
	conversation, err := h.svc.CreateDirect(c.Request.Context(), userID, request.PeerUserID)
	if err != nil {
		chatError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": conversation})
}

func (h *ChatHandler) ListConversations(c *gin.Context) {
	userID, ok := gameUserID(c)
	if !ok {
		chatError(c, service.ErrInvalidConversationRequest)
		return
	}
	limit, err := chatLimit(c, 20)
	if err != nil {
		chatError(c, err)
		return
	}
	page, err := h.svc.ListConversations(c.Request.Context(), userID, strings.TrimSpace(c.Query("cursor")), limit)
	if err != nil {
		chatError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": page})
}

func (h *ChatHandler) ListMessages(c *gin.Context) {
	userID, ok := gameUserID(c)
	conversationID, idOK := positiveUint(c.Param("conversationId"))
	if !ok || !idOK {
		chatError(c, service.ErrInvalidMessageQuery)
		return
	}
	beforeSeq := uint64(0)
	if raw := strings.TrimSpace(c.Query("before_seq")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || value == 0 {
			chatError(c, service.ErrInvalidMessageQuery)
			return
		}
		beforeSeq = value
	}
	limit, err := chatLimit(c, 50)
	if err != nil {
		chatError(c, service.ErrInvalidMessageQuery)
		return
	}
	page, err := h.svc.ListMessages(c.Request.Context(), userID, conversationID, beforeSeq, limit)
	if err != nil {
		chatError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": page})
}

func (h *ChatHandler) MarkRead(c *gin.Context) {
	userID, ok := gameUserID(c)
	conversationID, idOK := positiveUint(c.Param("conversationId"))
	var request markConversationReadRequest
	if !ok || !idOK || c.ShouldBindJSON(&request) != nil || request.LastReadSeq == nil {
		chatError(c, service.ErrInvalidConversationRequest)
		return
	}
	result, err := h.svc.MarkRead(c.Request.Context(), userID, conversationID, *request.LastReadSeq)
	if err != nil {
		chatError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": result})
}

func chatLimit(c *gin.Context, defaultLimit int) (int, error) {
	limit := defaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, service.ErrInvalidConversationRequest
		}
		limit = parsed
	}
	return limit, nil
}

func chatError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, 1716, "chat unavailable"
	switch {
	case errors.Is(err, service.ErrInvalidConversationRequest):
		status, code, message = http.StatusBadRequest, 1708, "invalid conversation request"
	case errors.Is(err, service.ErrConversationNotFound):
		status, code, message = http.StatusNotFound, 1709, "conversation not found"
	case errors.Is(err, service.ErrFriendshipRequired):
		status, code, message = http.StatusConflict, 1710, "friendship required"
	case errors.Is(err, service.ErrInvalidMessageQuery):
		status, code, message = http.StatusBadRequest, 1713, "invalid message query"
	case errors.Is(err, service.ErrReadSequenceConflict):
		status, code, message = http.StatusConflict, 1714, "read sequence conflict"
	default:
		log.Printf("chat handler: %v", err)
	}
	c.JSON(status, gin.H{"code": code, "message": message})
}
