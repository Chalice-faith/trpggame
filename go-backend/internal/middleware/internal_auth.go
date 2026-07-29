package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

const internalSecretHeader = "X-Internal-Secret"

// InternalAuth 使用常量时间比较保护服务间内部接口。
func InternalAuth(sharedSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if sharedSecret == "" {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"code":    1213,
				"message": "internal authentication is not configured",
			})
			return
		}

		providedHash := sha256.Sum256([]byte(c.GetHeader(internalSecretHeader)))
		expectedHash := sha256.Sum256([]byte(sharedSecret))
		if subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    1212,
				"message": "invalid internal credentials",
			})
			return
		}

		c.Next()
	}
}
