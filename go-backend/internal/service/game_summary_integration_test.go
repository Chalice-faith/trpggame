//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"trpggame/internal/ai_client"
	"trpggame/internal/config"
	"trpggame/internal/model"
)

// Real MySQL/Redis and the actual authenticated HTTP client; model output is a
// deterministic fixture, explicitly not evidence of provider narrative quality.
func TestGameSummaryIntegrationHTTPRetryProjectionAndSave(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			f, _, game := memoryLifecycleFixture(t, mode)
			ctx := context.Background()
			if _, err := f.service.ArchivePending(ctx, f.room.ID); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			candidate := strings.Repeat("已经发生的正式剧情事实。", 20)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/ai/memory/summary" || r.Header.Get("X-Internal-Secret") != "fixture-secret" {
					w.WriteHeader(401)
					return
				}
				var body struct {
					Messages []model.RuntimeMessage `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) < 2 {
					w.WriteHeader(400)
					return
				}
				if calls.Add(1) == 1 {
					w.WriteHeader(503)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"summary": candidate})
			}))
			defer provider.Close()
			client := ai_client.NewClient(&config.AIConfig{BaseURL: provider.URL, Timeout: 5}, "fixture-secret")
			options := config.GameSummaryConfig{Enabled: true, TriggerActions: 1, PollIntervalMS: 100, TimeoutSeconds: 5, LeaseSeconds: 10}
			worker := NewGameSummaryWorker(f.store, client, f.runtime, options)
			worker.processRoom(f.room.ID)
			var work model.GameSummaryWork
			if err := f.db.Where("room_id = ?", f.room.ID).First(&work).Error; err != nil {
				t.Fatal(err)
			}
			if work.Status != "pending" || work.Attempts != 1 {
				t.Fatalf("failed work not durable: %+v", work)
			}
			f.db.Model(&work).Update("next_retry_at", time.Now().Add(-time.Second))
			restarted := NewGameSummaryWorker(f.store, client, f.runtime, options)
			restarted.processRoom(f.room.ID)
			if calls.Load() != 2 {
				t.Fatalf("calls: %d", calls.Load())
			}
			if text := f.client.Get(ctx, fmt.Sprintf("room:%d:summary", f.room.ID)).Val(); text != candidate {
				t.Fatal("summary projection missing")
			}
			request := &ai_client.GameActionRequest{RoomID: f.room.ID}
			game.ConfigureInferenceMemory(f.store)
			if err := game.prepareInferenceMemory(ctx, request); err != nil {
				t.Fatal(err)
			}
			if request.MemoryContext == nil || request.MemoryContext.Summary != candidate || request.MemoryContext.SummaryThroughPosition != 2 {
				t.Fatalf("context: %+v", request.MemoryContext)
			}
			save, err := game.CreateManualSave(ctx, &CreateManualSaveRequest{UserID: 7, RoomID: f.room.ID, SaveName: "summary snapshot"})
			if err != nil {
				t.Fatal(err)
			}
			if save.Save.SummaryThroughPosition == nil || *save.Save.SummaryThroughPosition != 2 || save.Save.SummaryVersion == nil || *save.Save.SummaryVersion != 1 || save.Save.SummaryMemory != candidate {
				t.Fatalf("save watermark missing: %+v", save.Save)
			}
		})
	}
}
