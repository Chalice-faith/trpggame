//go:build integration

package repo

import (
	"context"
	"encoding/binary"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
	"time"
)

func TestGameSummaryRedis72ProjectionFence(t *testing.T) {
	address := os.Getenv("TRPG_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("Redis not configured")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	id := uuid.New()
	room := uint(binary.BigEndian.Uint32(id[:4])) + 1000
	timeline := uuid.NewString()
	meta := gameArchiveKeys(room)[0]
	summaryKey := multiplayerSummaryKey(room)
	projectionKey := summaryKey + "_meta"
	t.Cleanup(func() { client.Del(ctx, meta, summaryKey, projectionKey) })
	client.HSet(ctx, meta, "timeline_id", timeline, "revision", 3, "durable_position", 20, "archive_state", "ready", "control_operation_id", "")
	client.Set(ctx, summaryKey, "old", time.Hour)
	runtime, _ := NewRedisGameStateRepo(client, time.Hour)
	if err := runtime.PublishSummary(ctx, room, timeline, 3, 1, 6, "first"); err != nil {
		t.Fatal(err)
	}
	if client.Get(ctx, summaryKey).Val() != "first" {
		t.Fatal("projection missing")
	}
	_ = runtime.PublishSummary(ctx, room, timeline, 3, 1, 11, "newer inherited-version position")
	_ = runtime.PublishSummary(ctx, room, timeline, 3, 1, 6, "stale")
	if client.Get(ctx, summaryKey).Val() != "newer inherited-version position" {
		t.Fatal("position regressed")
	}
	_ = runtime.PublishSummary(ctx, room, uuid.NewString(), 3, 10, 20, "wrong branch")
	_ = runtime.PublishSummary(ctx, room, timeline, 2, 10, 20, "wrong revision")
	client.HSet(ctx, meta, "control_operation_id", uuid.NewString())
	_ = runtime.PublishSummary(ctx, room, timeline, 3, 10, 20, "control occupied")
	if client.Get(ctx, summaryKey).Val() != "newer inherited-version position" {
		t.Fatal("fence failed")
	}
	if client.PTTL(ctx, summaryKey).Val() <= 0 || client.PTTL(ctx, projectionKey).Val() <= 0 {
		t.Fatal("projection TTL lost")
	}
}
