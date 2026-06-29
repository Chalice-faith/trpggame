package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"trpggame/internal/config"
	"trpggame/internal/router"
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

	// 启动 WebSocket Hub
	hub := ws.NewHub()
	go hub.Run()

	// 初始化路由（注入 DB + Hub）
	r := router.Setup(cfg, db, hub)

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
