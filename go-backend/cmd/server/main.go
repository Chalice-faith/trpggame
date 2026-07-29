package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"trpggame/internal/ai_client"
	"trpggame/internal/config"
	"trpggame/internal/handler"
	"trpggame/internal/repo"
	"trpggame/internal/router"
	"trpggame/internal/service"
	"trpggame/internal/storage"
	"trpggame/internal/ws"
)

func main() {
	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 初始化数据库连接
	db, err := config.InitDB(&cfg.Database)
	if err != nil {
		log.Fatalf("Failed to connect database: %v", err)
	}
	log.Println("Database connected")

	// 初始化剧本模块依赖
	storageContext, cancelStorage := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStorage()

	scriptStorage, err := storage.NewMinIOStorage(storageContext, &cfg.MinIO)
	if err != nil {
		log.Fatalf("Failed to initialize MinIO storage: %v", err)
	}
	log.Println("MinIO storage connected")

	scriptRepo := repo.NewScriptRepo(db)
	aiClient := ai_client.NewClient(&cfg.AI, cfg.Internal.SharedSecret)
	scriptService := service.NewScriptService(scriptRepo, scriptStorage, aiClient, cfg)
	scriptHandler := handler.NewScriptHandler(scriptService, cfg.MinIO.MaxUploadSize)
	internalScriptHandler := handler.NewInternalScriptHandler(scriptService)

	// 启动 WebSocket Hub
	hub := ws.NewHub()
	go hub.Run()

	// 初始化路由
	r := router.Setup(cfg, db, hub, scriptHandler, internalScriptHandler)

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
	hub.Stop()

	log.Println("Server stopped")
}
