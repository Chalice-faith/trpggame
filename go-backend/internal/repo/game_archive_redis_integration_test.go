//go:build integration

package repo

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"trpggame/internal/model"
)

func TestGameArchiveRedis72AtomicBoundaries(t *testing.T) {
	address := os.Getenv("TRPG_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("TRPG_TEST_REDIS_ADDR is not configured")
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"solo", "multiplayer", "response-loss", "write-error", "runtime-expiry", "skip-manual", "skip-timeout", "boundary", "multiplayer-boundary", "lease-expiry", "pause-ACK"} {
		t.Run(scenario, func(t *testing.T) {
			id := uuid.New()
			room := uint(binary.BigEndian.Uint32(id[:4])) + 1000
			mode := "solo"
			turn := 0
			if strings.HasPrefix(scenario, "skip") || strings.HasPrefix(scenario, "multiplayer") || scenario == "pause-ACK" {
				mode = "multiplayer"
			}
			if scenario == "boundary" || scenario == "multiplayer-boundary" {
				turn = 9
			}
			t.Cleanup(func() { cleanupArchiveIntegrationRoom(t, client, room) })
			f := setupArchiveFixture(t, client, room, mode, turn)
			switch scenario {
			case "solo":
				testArchiveSoloContract(t, f)
			case "multiplayer":
				testArchiveMultiplayerContract(t, f)
			case "response-loss", "write-error":
				testArchiveCommitFaults(t, f, scenario == "write-error")
			case "runtime-expiry":
				if _, err := f.repo.CommitAction(ctx, f.solo(0, 1)); err != nil {
					t.Fatal(err)
				}
				for _, key := range soloRuntimeCleanupKeys(room, 7) {
					if err := client.PExpire(ctx, key, time.Millisecond).Err(); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(20 * time.Millisecond)
				if client.Exists(ctx, runtimeTurnKey(room)).Val() != 0 {
					t.Fatal("runtime did not expire")
				}
				_, ack := f.claim(t, uuid.NewString())
				result, err := f.repo.AcknowledgeGameArchive(ctx, ack)
				if err != nil || result.RuntimeAvailable || client.Exists(ctx, runtimeTurnKey(room)).Val() != 0 {
					t.Fatalf("expired ACK=%#v %v", result, err)
				}
			case "skip-manual", "skip-timeout":
				request := f.skip(0, 1, scenario == "skip-timeout")
				result, err := f.repo.SkipMultiplayerTurn(ctx, request)
				if err != nil || result.Memory == nil || !result.DeadlineAt.IsZero() {
					t.Fatalf("skip=%#v %v", result, err)
				}
				_, ack := f.claim(t, uuid.NewString())
				if _, err := f.repo.AcknowledgeGameArchive(ctx, ack); err != nil {
					t.Fatal(err)
				}
			case "boundary", "multiplayer-boundary":
				var commitErr error
				if mode == "solo" {
					_, commitErr = f.repo.CommitAction(ctx, f.solo(9, 1))
				} else {
					_, commitErr = f.repo.CommitMultiplayerAction(ctx, f.multiplayerAction(t, 9, 1))
				}
				if commitErr != nil {
					t.Fatal(commitErr)
				}
				pending, ack := f.claim(t, uuid.NewString())
				if _, err := f.repo.AcknowledgeGameArchive(ctx, ack); err != nil {
					t.Fatal(err)
				}
				saves, err := f.repo.ListPendingMemoryAutoSaves(ctx, room)
				if err != nil || len(saves) != 1 || string(saves[0].Snapshot) != string(pending.BoundarySnapshot) {
					t.Fatalf("boundary saves=%#v %v", saves, err)
				}
				if err := f.repo.AcknowledgeMemoryAutoSave(ctx, room, saves[0]); err != nil {
					t.Fatal(err)
				}
			case "lease-expiry":
				if _, err := f.repo.CommitAction(ctx, f.solo(0, 1)); err != nil {
					t.Fatal(err)
				}
				_, old := f.claim(t, uuid.NewString())
				if err := client.PExpire(ctx, gameArchiveKeys(room)[2], time.Millisecond).Err(); err != nil {
					t.Fatal(err)
				}
				time.Sleep(20 * time.Millisecond)
				_, current := f.claim(t, uuid.NewString())
				if _, err := f.repo.AcknowledgeGameArchive(ctx, old); !errors.Is(err, ErrGameArchiveLeaseConflict) {
					t.Fatalf("expired owner ACK=%v", err)
				}
				if _, err := f.repo.AcknowledgeGameArchive(ctx, current); err != nil {
					t.Fatal(err)
				}
			case "pause-ACK":
				if _, err := f.repo.CommitMultiplayerAction(ctx, f.multiplayerAction(t, 0, 1)); err != nil {
					t.Fatal(err)
				}
				if _, err := f.repo.TransitionMultiplayerRoom(ctx, room, f.generation, uuid.NewString(), model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{}); err != nil {
					t.Fatal(err)
				}
				_, ack := f.claim(t, uuid.NewString())
				result, err := f.repo.AcknowledgeGameArchive(ctx, ack)
				if err != nil || result.DeadlineAt != nil {
					t.Fatalf("pause ACK=%#v %v", result, err)
				}
			}
		})
	}
}

func cleanupArchiveIntegrationRoom(t *testing.T, client *redis.Client, room uint) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, pattern := range []string{runtimeStatusKey(room)[:strings.LastIndex(runtimeStatusKey(room), ":")+1] + "*", gameArchiveKeys(room)[0][:strings.LastIndex(gameArchiveKeys(room)[0], ":")+1] + "*"} {
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
			if err != nil {
				t.Error(err)
				break
			}
			if len(keys) > 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					t.Error(err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	roomText := strings.TrimPrefix(strings.TrimSuffix(runtimeStatusKey(room), ":status"), "room:")
	for _, queue := range []string{gameArchiveKeys(room)[3], gameArchiveKeys(room)[5]} {
		if err := client.ZRem(ctx, queue, roomText).Err(); err != nil {
			t.Error(err)
		}
	}
	members, err := client.ZRange(ctx, multiplayerDeadlineQueueKey(), 0, -1).Result()
	if err != nil {
		t.Error(err)
		return
	}
	for _, member := range members {
		if strings.HasPrefix(member, roomText+":") {
			if err := client.ZRem(ctx, multiplayerDeadlineQueueKey(), member).Err(); err != nil {
				t.Error(err)
			}
		}
	}
}
