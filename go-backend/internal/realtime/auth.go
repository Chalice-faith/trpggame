package realtime

import (
	"fmt"
	"strings"

	"trpggame/internal/middleware"
)

// AuthFailure 查询参数 JWT 鉴权失败类型。
type AuthFailure string

const (
	AuthMissingToken AuthFailure = "missing_token"
	AuthInvalidToken AuthFailure = "invalid_token"
)

// AuthError 是不包含原始 Token 或 JWT 内部错误的安全鉴权错误。
type AuthError struct {
	Failure AuthFailure
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("websocket authentication failed: %s", e.Failure)
}

// AuthenticateQueryToken 校验浏览器 WebSocket 查询参数中的 Access Token。
// HTTP 状态与业务错误码由具体的游戏或 IM Handler 映射。
func AuthenticateQueryToken(rawToken, secret string) (*middleware.Claims, error) {
	token := strings.TrimSpace(rawToken)
	if token == "" {
		return nil, &AuthError{Failure: AuthMissingToken}
	}

	claims, err := middleware.ValidateToken(token, secret)
	if err != nil {
		return nil, &AuthError{Failure: AuthInvalidToken}
	}
	return claims, nil
}
