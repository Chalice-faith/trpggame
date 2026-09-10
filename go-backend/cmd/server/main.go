package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorm.io/gorm"

	"trpggame/internal/ai_client"
	"trpggame/internal/config"
	"trpggame/internal/handler"
	"trpggame/internal/imws"
	"trpggame/internal/realtime"
	"trpggame/internal/repo"
	"trpggame/internal/router"
	"trpggame/internal/service"
	"trpggame/internal/storage"
	"trpggame/internal/ws"
)

// roomAuthorizer 校验用户是否为房间房主，与 REST 行动链路的访问控制一致。
type roomAuthorizer struct {
	repo *repo.GameRepo
}

func gameActionErrorCode(err error) int {
	switch {
	case errors.Is(err, service.ErrInvalidGameAction):
		return 1310
	case errors.Is(err, service.ErrGameRoomNotFound):
		return 1311
	case errors.Is(err, service.ErrGamePlayerNotFound):
		return 1312
	case errors.Is(err, service.ErrGameRoomNotPlaying):
		return 1313
	case errors.Is(err, service.ErrGameActionConflict):
		return 1314
	case errors.Is(err, service.ErrActionRequestConflict):
		return 1315
	case errors.Is(err, service.ErrInsufficientItems):
		return 1316
	case errors.Is(err, service.ErrGameRuntimeUnavailable):
		return 1318
	case errors.Is(err, service.ErrInvalidActionEffects):
		return 1319
	default:
		return 1317
	}
}

func (a roomAuthorizer) Authorize(ctx context.Context, userID, roomID uint) error {
	_, err := a.repo.FindRoomByIDAndOwnerID(ctx, roomID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("room not found or not owned by user")
		}
		return fmt.Errorf("authorize room: %w", err)
	}
	return nil
}

func main() {
	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	allowedOrigins, err := realtime.ParseAllowedOrigins(cfg.WebSocket.AllowedOrigins)
	if err != nil {
		log.Fatalf("Invalid WebSocket allowed origins: %v", err)
	}

	// 初始化数据库连接
	db, err := config.InitDB(&cfg.Database)
	if err != nil {
		log.Fatalf("Failed to connect database: %v", err)
	}
	log.Println("Database connected")

	// 初始化外部依赖
	dependencyContext, cancelDependencies := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDependencies()

	redisClient, err := config.InitRedis(dependencyContext, &cfg.Redis)
	if err != nil {
		log.Fatalf("Failed to connect Redis: %v", err)
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Printf("Failed to close Redis: %v", err)
		}
	}()
	log.Println("Redis connected")

	scriptStorage, err := storage.NewMinIOStorage(dependencyContext, &cfg.MinIO)
	if err != nil {
		log.Fatalf("Failed to initialize MinIO storage: %v", err)
	}
	log.Println("MinIO storage connected")

	scriptRepo := repo.NewScriptRepo(db)
	aiClient := ai_client.NewClient(&cfg.AI, cfg.Internal.SharedSecret)
	scriptService := service.NewScriptService(scriptRepo, scriptStorage, aiClient, cfg)
	scriptHandler := handler.NewScriptHandler(scriptService, cfg.MinIO.MaxUploadSize)
	internalScriptHandler := handler.NewInternalScriptHandler(scriptService)
	gameRepo := repo.NewGameRepo(db)
	gameStateRepo, err := repo.NewRedisGameStateRepo(redisClient, repo.DefaultGameRuntimeTTL)
	if err != nil {
		log.Fatalf("Failed to initialize game runtime repository: %v", err)
	}
	gameService := service.NewGameService(gameRepo, scriptRepo, aiClient, gameStateRepo)
	gameHandler := handler.NewGameHandler(gameService)
	userRepo := repo.NewUserRepo(db)
	friendRepo := repo.NewFriendRepo(db)

	// 启动 WebSocket Hub
	hub := ws.NewHub()
	hub.SetGameActionHandler(func(ctx context.Context, client *ws.Client, request ws.GameActionData) {
		if request.ExpectedTurn == nil {
			hub.SendErrorToUser(client.RoomID, client.UserID, 1506, "invalid game action", request.RequestID)
			return
		}
		_, err := gameService.SubmitActionStream(
			ctx,
			&service.SubmitGameActionRequest{
				UserID:       client.UserID,
				RoomID:       client.RoomID,
				RequestID:    request.RequestID,
				ExpectedTurn: *request.ExpectedTurn,
				Action:       request.ActionText,
			},
			func(event service.GameActionStreamEvent) {
				switch event.Type {
				case "narrative_chunk":
					payload, marshalErr := json.Marshal(ws.NarrativeChunkData{
						Content: event.Content,
						IsFinal: false,
					})
					if marshalErr == nil {
						hub.SendToUserWithRequestID(client.RoomID, client.UserID, ws.MsgNarrativeChunk, payload, request.RequestID)
					}
				case "dice_roll":
					if event.Result == nil || event.Result.DiceRoll == nil {
						return
					}
					payload, marshalErr := json.Marshal(event.Result.DiceRoll)
					if marshalErr == nil {
						hub.SendToUserWithRequestID(client.RoomID, client.UserID, ws.MsgDiceRoll, payload, request.RequestID)
					}
				case "status_update":
					if event.Result == nil || event.Result.Effects == nil {
						return
					}
					payload, marshalErr := json.Marshal(ws.StatusUpdateData{
						PlayerID: client.UserID,
						Changes: map[string]any{
							"player_state_changes": event.Result.Effects.PlayerStateChanges,
							"items":                event.Result.Effects.Items,
							"buffs":                event.Result.Effects.Buffs,
							"events":               event.Result.Effects.Events,
						},
					})
					if marshalErr == nil {
						hub.SendToUserWithRequestID(client.RoomID, client.UserID, ws.MsgStatusUpdate, payload, request.RequestID)
					}
				case "narrative_complete":
					if event.Result == nil {
						return
					}
					payload, marshalErr := json.Marshal(ws.NarrativeCompleteData{
						Narrative:   event.Result.Narrative,
						CurrentTurn: event.Result.CurrentTurn,
						Duplicate:   event.Result.Duplicate,
					})
					if marshalErr == nil {
						hub.SendToUserWithRequestID(client.RoomID, client.UserID, ws.MsgNarrativeComplete, payload, request.RequestID)
					}
				}
			},
		)
		if err != nil {
			code := gameActionErrorCode(err)
			message := "AI action generation unavailable"
			if code != 1317 {
				message = "game action rejected"
			}
			hub.SendErrorToUser(client.RoomID, client.UserID, code, message, request.RequestID)
		}
	})
	go hub.Run()
	presenceRepo, err := repo.NewPresenceRepo(redisClient, repo.DefaultPresenceTTL)
	if err != nil {
		log.Fatalf("Failed to initialize presence repository: %v", err)
	}
	imHub := imws.NewHub()
	presenceCoordinator := service.NewPresenceCoordinator(presenceRepo, friendRepo, imHub)
	imHub.SetPresenceObserver(presenceCoordinator)
	go presenceCoordinator.Run()
	go imHub.Run()
	friendshipPublisher := service.NewRealtimeFriendshipPublisher(userRepo, imHub)
	friendService := service.NewFriendService(
		friendRepo,
		userRepo,
		service.NewRedisPresenceProvider(presenceRepo),
		friendshipPublisher,
	)
	friendHandler := handler.NewFriendHandler(friendService)

	// 初始化路由（游戏与 IM WebSocket 分别管理连接）
	wsHandlers := router.WebSocketHandlers{
		Game: ws.HandleWebSocket(hub, cfg.JWT.Secret, allowedOrigins, roomAuthorizer{repo: gameRepo}),
		IM:   imws.HandleWebSocket(imHub, cfg.JWT.Secret, allowedOrigins),
	}
	r := router.Setup(
		cfg,
		db,
		wsHandlers,
		scriptHandler,
		internalScriptHandler,
		gameHandler,
		friendHandler,
	)

	// 启动服务器
	addr := fmt.Sprintf(":%s", cfg.Server.Port)
	log.Printf("Go Backend starting on %s", addr)

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := r.Run(addr); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-quit
	log.Println("Shutting down server...")

	// 关闭 WebSocket Hub
	imHub.Stop()
	presenceCoordinator.Stop()
	hub.Stop()

	log.Println("Server stopped")
}
