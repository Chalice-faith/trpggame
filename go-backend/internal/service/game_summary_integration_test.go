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

	"github.com/google/uuid"
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
			nextCandidate := strings.Repeat("新的行动与此前事实共同构成剧情。", 18)
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
				call := calls.Add(1)
				if call == 1 || call == 3 {
					w.WriteHeader(503)
					return
				}
				text := candidate
				if call >= 4 {
					text = nextCandidate
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"summary": text})
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
			if _, err := game.ResumeGame(ctx, &ResumeGameRequest{UserID: 7, RoomID: f.room.ID}); err != nil {
				t.Fatal(err)
			}
			commitNextSummaryAction(t, f, mode)
			if _, err := f.service.ArchivePending(ctx, f.room.ID); err != nil {
				t.Fatal(err)
			}
			summaryKey := fmt.Sprintf("room:%d:summary", f.room.ID)
			if err := f.client.Del(ctx, summaryKey, summaryKey+"_meta").Err(); err != nil {
				t.Fatal(err)
			}
			// New model failure must still rebuild the prior durable summary.
			restarted.processRoom(f.room.ID)
			if calls.Load() != 3 || f.client.Get(ctx, summaryKey).Val() != candidate || f.client.PTTL(ctx, summaryKey).Val() <= 0 {
				t.Fatal("model failure blocked recovery of prior summary")
			}
			if err := game.prepareInferenceMemory(ctx, request); err != nil {
				t.Fatal(err)
			}
			if request.MemoryContext.SummaryThroughPosition != 2 || request.MemoryContext.ThroughPosition != 3 || len(request.MemoryContext.Messages) < 4 {
				t.Fatalf("uncovered facts lost after failure: %+v", request.MemoryContext)
			}
			var nextWork model.GameSummaryWork
			if err := f.db.Where("room_id = ? AND expected_version = ?", f.room.ID, 1).First(&nextWork).Error; err != nil {
				t.Fatal(err)
			}
			if nextWork.Status != "pending" || nextWork.Attempts != 1 {
				t.Fatal("second cycle failure was not retryable")
			}
			if err := f.db.Model(&nextWork).Update("next_retry_at", time.Now().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			NewGameSummaryWorker(f.store, client, f.runtime, options).processRoom(f.room.ID)
			if calls.Load() != 4 || f.client.Get(ctx, summaryKey).Val() != nextCandidate {
				t.Fatal("second cycle did not publish updated summary")
			}
			if err := f.db.First(&save.Save, save.Save.ID).Error; err != nil {
				t.Fatal(err)
			}
			if save.Save.SummaryMemory != candidate || *save.Save.SummaryVersion != 1 || *save.Save.SummaryThroughPosition != 2 {
				t.Fatal("new cycle changed frozen saved baseline")
			}
		})
	}
}

func commitNextSummaryAction(t *testing.T, f *archiveIntegrationFixture, mode string) {
	t.Helper()
	ctx := context.Background()
	source, err := f.runtime.GetMemoryControlSource(ctx, f.room.ID, 7, mode)
	if err != nil || source == nil || source.Memory == nil {
		t.Fatalf("next action source=%+v err=%v", source, err)
	}
	generation, turn := source.Generation, source.Turn
	actor, round := uint(7), 2
	if mode == "multiplayer" {
		actor, round = 8, 1
	}
	request, fingerprint := uuid.NewString(), strings.Repeat("b", 64)
	record := &model.GameActionRecord{CommitID: uuid.NewString(), RoomID: f.room.ID, TimelineID: f.timeline, Position: source.Memory.HeadPosition + 1, Kind: "action", ActorID: &actor,
		RequestNamespace: "client", RequestID: request, Fingerprint: fingerprint, SourceGeneration: generation, SourceRevision: source.Memory.Revision,
		TurnBefore: turn, TurnAfter: turn + 1, RoundBefore: source.Round, RoundAfter: round, PayloadVersion: 1}
	messages := []model.RuntimeMessage{{Role: "user", Content: "打开石门"}, {Role: "assistant", Content: "石门后的道路出现"}}
	response := []byte(fmt.Sprintf(`{"narrative":"石门后的道路出现","current_turn":%d,"round_number":%d,"generation":"%s"}`, turn+1, round, generation))
	if mode == "solo" {
		_, err = f.runtime.CommitAction(ctx, &model.ActionRuntimeMutation{RoomID: f.room.ID, UserID: actor, Generation: generation,
			ExpectedTurn: turn, RequestID: request, RequestFingerprint: fingerprint, ResponseJSON: response, Messages: messages, Archive: record})
	} else {
		if _, err := f.runtime.AcquireMemoryMultiplayerAction(ctx, f.room.ID, actor, turn, request, fingerprint, time.Now(), model.GameArchiveExpectation{TimelineID: f.timeline, Generation: generation}); err != nil {
			t.Fatal(err)
		}
		_, err = f.runtime.CommitMultiplayerAction(ctx, &model.MultiplayerActionMutation{RoomID: f.room.ID, UserID: actor, Generation: generation,
			ExpectedTurn: turn, RequestID: request, RequestFingerprint: fingerprint, ResponseJSON: response, Messages: messages, Archive: record,
			NextDeadline: time.Now().Add(time.Minute), PlayerMutations: []model.MultiplayerPlayerMutation{{UserID: actor, PlayerStateChanges: map[string]string{"hp": "9"}}}})
	}
	if err != nil {
		t.Fatal(err)
	}
}
