package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"trpggame/internal/config"
	"trpggame/internal/model"
	"trpggame/internal/repo"
	"trpggame/internal/service"
)

// UserHandler 用户相关 HTTP 处理器
type UserHandler struct {
	svc *service.UserService
}

// NewUserHandler 创建 UserHandler
func NewUserHandler(db *gorm.DB, cfg *config.Config) *UserHandler {
	userRepo := repo.NewUserRepo(db)
	svc := service.NewUserService(userRepo, cfg)
	return &UserHandler{svc: svc}
}

// errMsg 根据预定义业务错误返回安全消息；未知错误记录日志后返回通用内部错误消息
func errMsg(err error) string {
	switch {
	case errors.Is(err, service.ErrUsernameAlreadyExists):
		return "username already exists"
	case errors.Is(err, service.ErrEmailAlreadyExists):
		return "email already exists"
	case errors.Is(err, service.ErrInvalidCredentials):
		return "invalid username or password"
	case errors.Is(err, service.ErrInvalidRefreshToken):
		return "invalid refresh token"
	case errors.Is(err, service.ErrUserNotFound):
		return "user not found"
	case errors.Is(err, service.ErrInternal):
		return "internal error"
	default:
		log.Printf("handler: unexpected error: %v", err)
		return "internal error"
	}
}

// Register 用户注册
// POST /api/v1/auth/register
func (h *UserHandler) Register(c *gin.Context) {
	var req service.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1000,
			"message": "invalid request parameters",
		})
		return
	}

	resp, err := h.svc.Register(&req)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{
			"code":    1100,
			"message": errMsg(err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data":    resp,
	})
}

// Login 用户登录
// POST /api/v1/auth/login
func (h *UserHandler) Login(c *gin.Context) {
	var req service.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1000,
			"message": "invalid request parameters",
		})
		return
	}

	resp, err := h.svc.Login(&req)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1101,
			"message": errMsg(err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data":    resp,
	})
}

// RefreshToken 刷新 Access Token
// POST /api/v1/auth/refresh
func (h *UserHandler) RefreshToken(c *gin.Context) {
	var req service.RefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1000,
			"message": "invalid request parameters",
		})
		return
	}

	resp, err := h.svc.RefreshToken(&req)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    1102,
			"message": errMsg(err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data":    resp,
	})
}

// GetProfile 获取个人信息
// GET /api/v1/users/me
func (h *UserHandler) GetProfile(c *gin.Context) {
	userID, _ := c.Get("user_id")

	user, err := h.svc.GetProfile(userID.(uint))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"code":    1103,
			"message": errMsg(err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data":    user,
	})
}

// UpdateProfile 更新个人信息
// PUT /api/v1/users/me
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	userID, _ := c.Get("user_id")

	var req service.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1000,
			"message": "invalid request parameters",
		})
		return
	}

	user, err := h.svc.UpdateProfile(userID.(uint), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1104,
			"message": errMsg(err),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "ok",
		"data":    user,
	})
}

// 确保 model 包被使用（后续阶段需要）
var _ = model.User{}
