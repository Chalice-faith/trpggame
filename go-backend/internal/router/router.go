package router

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"trpggame/internal/config"
	"trpggame/internal/handler"
	"trpggame/internal/middleware"
	"trpggame/internal/ws"
)

// Setup 初始化所有路由并返回 Gin Engine
func Setup(
	cfg *config.Config,
	db *gorm.DB,
	hub *ws.Hub,
	scriptHandler *handler.ScriptHandler,
	internalScriptHandler *handler.InternalScriptHandler,
) *gin.Engine {
	r := gin.Default()

	// 全局中间件
	r.Use(middleware.CORS())

	// 初始化 handlers（依赖注入）
	userHandler := handler.NewUserHandler(db, cfg)

	// WebSocket 端点
	r.GET("/ws", ws.HandleWebSocket(hub))

	// API v1
	v1 := r.Group("/api/v1")
	{
		// 公开端点（无需鉴权）
		auth := v1.Group("/auth")
		{
			auth.POST("/register", userHandler.Register)
			auth.POST("/login", userHandler.Login)
			auth.POST("/refresh", userHandler.RefreshToken)
		}

		// 需鉴权的端点
		internal := v1.Group("/internal")
		internal.Use(middleware.InternalAuth(cfg.Internal.SharedSecret))
		{
			internal.POST("/scripts/:id/status", internalScriptHandler.UpdateStatus)
		}

		authorized := v1.Group("")
		authorized.Use(middleware.AuthMiddleware(cfg))
		{
			// 用户
			users := authorized.Group("/users")
			{
				users.GET("/me", userHandler.GetProfile)
				users.PUT("/me", userHandler.UpdateProfile)
			}

			// 剧本 (Phase 1 M1.3 实现)
			scripts := authorized.Group("/scripts")
			{
				scripts.POST("/upload", scriptHandler.UploadScript)
				scripts.GET("", scriptHandler.ListScripts)
				scripts.GET("/:id", scriptHandler.GetScriptDetail)
				scripts.POST("/:id/retry", scriptHandler.RetryScript)
				scripts.DELETE("/:id", scriptHandler.DeleteScript)
			}

			// 游戏 (Phase 1 M1.5 实现)
			games := authorized.Group("/games")
			{
				games.POST("/solo/start", handler.StartSoloGame)
				games.POST("/:roomId/action", handler.SubmitAction)
				games.POST("/:roomId/save", handler.ManualSave)
				games.GET("/:roomId/saves", handler.ListSaves)
				games.POST("/:roomId/load", handler.LoadGame)
				games.POST("/:roomId/pause", handler.PauseGame)
				games.POST("/:roomId/resume", handler.ResumeGame)
				games.POST("/:roomId/end", handler.EndGame)
			}
		}
	}

	return r
}
