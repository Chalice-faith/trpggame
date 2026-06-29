package service

import "errors"

// 预定义业务错误，handler 层据此返回安全的用户提示
var (
	ErrUsernameAlreadyExists  = errors.New("username already exists")
	ErrEmailAlreadyExists     = errors.New("email already exists")
	ErrInvalidCredentials     = errors.New("invalid username or password")
	ErrInvalidRefreshToken    = errors.New("invalid refresh token")
	ErrUserNotFound           = errors.New("user not found")
	ErrInternal               = errors.New("internal error")
)
