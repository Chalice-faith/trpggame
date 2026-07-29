package service

import "errors"

// 预定义业务错误，handler 层据此返回安全的用户提示
var (
	ErrUsernameAlreadyExists = errors.New("username already exists")
	ErrEmailAlreadyExists    = errors.New("email already exists")
	ErrInvalidCredentials    = errors.New("invalid username or password")
	ErrInvalidRefreshToken   = errors.New("invalid refresh token")
	ErrUserNotFound          = errors.New("user not found")
	ErrScriptFileRequired    = errors.New("script PDF file is required")
	ErrScriptFileTooLarge    = errors.New("script PDF file is too large")
	ErrScriptFileExtension   = errors.New("script file must use the .pdf extension")
	ErrScriptFileContentType = errors.New("script file must have application/pdf content type")
	ErrScriptFileSignature   = errors.New("script file has an invalid PDF signature")
	ErrScriptTitleTooLong    = errors.New("script title is too long")
	ErrScriptNotFound        = errors.New("script not found")
	ErrInvalidPagination     = errors.New("invalid pagination parameters")
	ErrInternal              = errors.New("internal error")
)
