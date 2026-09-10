package router

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"trpggame/internal/config"
	"trpggame/internal/handler"
	"trpggame/internal/middleware"
	"trpggame/internal/openapi"
)

// WebSocketHandlers 由 main 组装，Router 只负责注册对应路径。
type WebSocketHandlers struct {
	Game gin.HandlerFunc
	IM   gin.HandlerFunc
}

var websocketLogSkipPaths = []string{"/ws", "/ws/im"}

// Setup 初始化所有路由并返回 Gin Engine
func Setup(
	cfg *config.Config,
	db *gorm.DB,
	wsHandlers WebSocketHandlers,
	scriptHandler *handler.ScriptHandler,
	internalScriptHandler *handler.InternalScriptHandler,
	gameHandler *handler.GameHandler,
	friendHandlers ...*handler.FriendHandler,
) *gin.Engine {
	r := gin.New()
	r.Use(
		gin.LoggerWithConfig(gin.LoggerConfig{SkipPaths: websocketLogSkipPaths}),
		gin.Recovery(),
	)

	// 全局中间件
	r.Use(middleware.CORS())

	// 公共 REST API 文档（规范与 UI 均嵌入 Go 二进制）
	openapi.RegisterRoutes(r)

	// 初始化 handlers（依赖注入）
	userHandler := handler.NewUserHandler(db, cfg)
	friendHandler := handler.NewFriendHandler(nil)
	if len(friendHandlers) > 0 && friendHandlers[0] != nil {
		friendHandler = friendHandlers[0]
	}

	// WebSocket 端点（鉴权与连接依赖由 main 组装后传入）
	if wsHandlers.Game != nil {
		r.GET("/ws", wsHandlers.Game)
	}
	if wsHandlers.IM != nil {
		r.GET("/ws/im", wsHandlers.IM)
	}

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
				users.GET("/search", friendHandler.SearchUsers)
			}

			friendRequests := authorized.Group("/friend-requests")
			{
				friendRequests.POST("", friendHandler.SendRequest)
				friendRequests.GET("", friendHandler.ListRequests)
				friendRequests.POST("/:requestId/accept", friendHandler.AcceptRequest)
				friendRequests.POST("/:requestId/reject", friendHandler.RejectRequest)
			}

			friends := authorized.Group("/friends")
			{
				friends.GET("", friendHandler.ListFriends)
				friends.DELETE("/:friendUserId", friendHandler.DeleteFriend)
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
				games.POST("/solo/start", gameHandler.StartSoloGame)
				games.POST("/:roomId/action", gameHandler.SubmitAction)
				games.POST("/:roomId/save", gameHandler.ManualSave)
				games.GET("/:roomId/saves", gameHandler.ListSaves)
				games.POST("/:roomId/load", gameHandler.LoadGame)
				games.POST("/:roomId/pause", gameHandler.PauseGame)
				games.POST("/:roomId/resume", gameHandler.ResumeGame)
				games.POST("/:roomId/end", gameHandler.EndGame)
			}
		}
	}

	return r
}
