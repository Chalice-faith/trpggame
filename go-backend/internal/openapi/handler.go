package openapi

import (
	"bytes"
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

const (
	SpecPath = "/api/openapi.yaml"
	UIPath   = "/api/docs/*any"
)

//go:embed openapi.yaml
var specification []byte

// Specification 返回内嵌 OpenAPI 规范的副本。
func Specification() []byte {
	return bytes.Clone(specification)
}

// RegisterRoutes 注册公共 OpenAPI 规范与 Swagger UI。
// 规范文件随 Go 二进制嵌入，不依赖运行目录或外部 CDN。
func RegisterRoutes(engine *gin.Engine) {
	engine.GET(SpecPath, func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", specification)
	})
	engine.GET(
		UIPath,
		ginSwagger.WrapHandler(
			swaggerFiles.Handler,
			ginSwagger.URL(SpecPath),
		),
	)
}
