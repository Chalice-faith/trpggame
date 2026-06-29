package service

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"trpggame/internal/config"
	"trpggame/internal/middleware"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

// UserService 用户业务逻辑
type UserService struct {
	repo *repo.UserRepo
	cfg  *config.Config
}

// NewUserService 创建 UserService
func NewUserService(r *repo.UserRepo, cfg *config.Config) *UserService {
	return &UserService{repo: r, cfg: cfg}
}

// RegisterRequest 注册请求
type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=50"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6,max=100"`
	Nickname string `json:"nickname"`
}

// RegisterResponse 注册响应
type RegisterResponse struct {
	UserID       uint   `json:"user_id"`
	Username     string `json:"username"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Register 用户注册
func (s *UserService) Register(req *RegisterRequest) (*RegisterResponse, error) {
	// 检查用户名是否已存在
	if _, err := s.repo.FindByUsername(req.Username); err == nil {
		return nil, ErrUsernameAlreadyExists
	}

	// 检查邮箱是否已存在
	if _, err := s.repo.FindByEmail(req.Email); err == nil {
		return nil, ErrEmailAlreadyExists
	}

	// 生成密码哈希
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return nil, fmt.Errorf("bcrypt hash: %w", ErrInternal)
	}

	user := &model.User{
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: string(hash),
		Nickname:     req.Nickname,
	}

	if err := s.repo.Create(user); err != nil {
		return nil, fmt.Errorf("create user: %w", ErrInternal)
	}

	// 生成 Token
	accessToken, err := middleware.GenerateToken(user.ID, user.Username, s.cfg.JWT.Secret, s.cfg.JWT.AccessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("generate access token: %w", ErrInternal)
	}

	refreshToken, err := middleware.GenerateRefreshToken(user.ID, user.Username, s.cfg.JWT.Secret, s.cfg.JWT.RefreshTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", ErrInternal)
	}

	return &RegisterResponse{
		UserID:       user.ID,
		Username:     user.Username,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}

// LoginRequest 登录请求
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	UserID       uint   `json:"user_id"`
	Username     string `json:"username"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Login 用户登录
func (s *UserService) Login(req *LoginRequest) (*LoginResponse, error) {
	user, err := s.repo.FindByUsername(req.Username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("find user: %w", ErrInternal)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	accessToken, err := middleware.GenerateToken(user.ID, user.Username, s.cfg.JWT.Secret, s.cfg.JWT.AccessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("generate access token: %w", ErrInternal)
	}

	refreshToken, err := middleware.GenerateRefreshToken(user.ID, user.Username, s.cfg.JWT.Secret, s.cfg.JWT.RefreshTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", ErrInternal)
	}

	return &LoginResponse{
		UserID:       user.ID,
		Username:     user.Username,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}

// RefreshTokenRequest 刷新 Token 请求
type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// UpdateProfileRequest 更新个人信息请求
type UpdateProfileRequest struct {
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url"`
}

// GetProfile 获取用户个人信息
func (s *UserService) GetProfile(userID uint) (*model.User, error) {
	user, err := s.repo.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("find user by id: %w", ErrInternal)
	}
	return user, nil
}

// UpdateProfile 更新用户个人信息
func (s *UserService) UpdateProfile(userID uint, req *UpdateProfileRequest) (*model.User, error) {
	user, err := s.repo.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("find user by id: %w", ErrInternal)
	}

	if req.Nickname != "" {
		user.Nickname = req.Nickname
	}
	if req.AvatarURL != "" {
		user.AvatarURL = req.AvatarURL
	}

	if err := s.repo.Update(user); err != nil {
		return nil, fmt.Errorf("update user: %w", ErrInternal)
	}

	return user, nil
}

// RefreshToken 刷新 Access Token
func (s *UserService) RefreshToken(req *RefreshTokenRequest) (*LoginResponse, error) {
	claims, err := middleware.ValidateToken(req.RefreshToken, s.cfg.JWT.Secret)
	if err != nil {
		return nil, ErrInvalidRefreshToken
	}

	accessToken, err := middleware.GenerateToken(claims.UserID, claims.Username, s.cfg.JWT.Secret, s.cfg.JWT.AccessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("generate access token: %w", ErrInternal)
	}

	refreshToken, err := middleware.GenerateRefreshToken(claims.UserID, claims.Username, s.cfg.JWT.Secret, s.cfg.JWT.RefreshTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("generate refresh token: %w", ErrInternal)
	}

	return &LoginResponse{
		UserID:       claims.UserID,
		Username:     claims.Username,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}
