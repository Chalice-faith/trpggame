package config

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

var ErrInvalidRedisConfig = errors.New("invalid Redis config")

// InitRedis 创建 Redis 客户端并确认启动时连接可用。
func InitRedis(ctx context.Context, cfg *RedisConfig) (*redis.Client, error) {
	if cfg == nil || strings.TrimSpace(cfg.Addr) == "" || cfg.DB < 0 {
		return nil, ErrInvalidRedisConfig
	}
	client := redis.NewClient(&redis.Options{
		Addr:     strings.TrimSpace(cfg.Addr),
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect Redis: %w", err)
	}
	return client, nil
}
