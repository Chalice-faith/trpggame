package repo

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"trpggame/internal/model"
)

type archiveFixture struct {
	repo                 *RedisGameStateRepo
	client               *redis.Client
	room                 uint
	generation, timeline string
}

func newArchiveFixture(t *testing.T, mode string, turn int) (*miniredis.Miniredis, *archiveFixture) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, setupArchiveFixture(t, client, 41, mode, turn)
}

func setupArchiveFixture(t *testing.T, client *redis.Client, room uint, mode string, turn int) *archiveFixture {
	t.Helper()
	ctx := context.Background()
	repository, err := NewRedisGameStateRepo(client, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f := &archiveFixture{repository, client, room, uuid.NewString(), uuid.NewString()}
	if mode == "solo" {
		state := validSoloRuntimeState()
		state.RoomID = room
		state.Generation = f.generation
		state.Turn = turn
		if err := repository.InitializeSoloRoom(ctx, state); err != nil {
			t.Fatal(err)
		}
	} else {
		state := validMultiplayerRuntimeState()
		state.RoomID = room
		state.Generation = f.generation
		if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ActivateMultiplayerRoom(ctx, room, f.generation, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if turn != 0 {
			if err := client.Set(ctx, runtimeTurnKey(room), turn, time.Hour).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
	timeout := time.Duration(0)
	if mode == "multiplayer" {
		timeout = 2 * time.Minute
	}
	if err := repository.BindGameArchive(ctx, model.GameArchiveBinding{RoomID: room, TimelineID: f.timeline, Generation: f.generation, Mode: mode, Revision: 1, ExpectedTurn: turn, TurnTimeout: timeout}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *archiveFixture) record(kind string, turn, roundBefore, roundAfter int, actor uint, request, fingerprint string, position uint64) *model.GameActionRecord {
	namespace := "client"
	if kind == "skip_timeout" {
		namespace = "timeout"
	}
	return &model.GameActionRecord{CommitID: uuid.NewString(), RoomID: f.room, TimelineID: f.timeline, Position: position, Kind: kind, ActorID: &actor,
		RequestNamespace: namespace, RequestID: request, Fingerprint: fingerprint, SourceGeneration: f.generation, SourceRevision: 1,
		TurnBefore: turn, TurnAfter: turn + 1, RoundBefore: roundBefore, RoundAfter: roundAfter, PayloadVersion: 1}
}

func (f *archiveFixture) solo(turn int, position uint64) *model.ActionRuntimeMutation {
	m := validActionMutation(turn, uuid.NewString(), fmt.Sprint(turn))
	m.RoomID = f.room
	m.Generation = f.generation
	m.PlayerStateChanges = map[string]string{"hp": "8"}
	m.ItemMutations = []model.RuntimeItemMutation{{Name: "key", QuantityDelta: 1}}
	m.ResponseJSON = []byte(`{"narrative":"found key","large_number":9007199254740993}`)
	m.Archive = f.record("action", turn, turn, turn+1, 7, m.RequestID, m.RequestFingerprint, position)
	return m
}

func (f *archiveFixture) claim(t *testing.T, owner string) (*model.PendingGameArchive, model.GameArchiveACK) {
	t.Helper()
	pending, err := f.repo.ClaimGameArchive(context.Background(), f.room, owner, time.Second)
	if err != nil || pending == nil {
		t.Fatalf("claim: %#v %v", pending, err)
	}
	r := pending.Record
	return pending, model.GameArchiveACK{RoomID: f.room, TimelineID: r.TimelineID, CommitID: r.CommitID, PayloadHash: r.PayloadHash, OwnerToken: owner, Position: r.Position}
}

func testArchiveSoloContract(t *testing.T, f *archiveFixture) {
	t.Helper()
	ctx := context.Background()
	m := f.solo(0, 1)
	if _, err := f.repo.BeginSoloAction(ctx, f.room, 7, 0); !errors.Is(err, ErrGameArchiveBranchChanged) {
		t.Fatalf("legacy preflight=%v", err)
	}
	expected := model.GameArchiveExpectation{TimelineID: f.timeline, Generation: f.generation}
	if _, err := f.repo.BeginMemorySoloAction(ctx, f.room, 7, 0, expected); err != nil {
		t.Fatal(err)
	}
	result, err := f.repo.CommitAction(ctx, m)
	if err != nil || result.Memory == nil || result.Memory.ArchiveState != "pending" {
		t.Fatalf("commit: %#v %v", result, err)
	}
	if _, err := f.repo.BeginMemorySoloAction(ctx, f.room, 7, 1, expected); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("pending action=%v", err)
	}
	if _, err := f.repo.CaptureMemorySoloRoom(ctx, f.room, 7, expected); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("pending save=%v", err)
	}
	if err := f.repo.DeleteSoloRoom(ctx, f.room, 7); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("cleanup=%v", err)
	}
	if err := f.repo.InitializeSoloRoom(ctx, &model.SoloRuntimeState{RoomID: f.room, UserID: 7, Generation: f.generation, Status: model.RoomStatusPlaying, PlayerState: map[string]string{"hp": "10"}, Opening: model.RuntimeMessage{Role: "assistant", Content: "opening"}}); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("reinitialize=%v", err)
	}
	replay, err := f.repo.CommitAction(ctx, m)
	if err != nil || !replay.Duplicate {
		t.Fatalf("replay=%#v %v", replay, err)
	}
	changed := *m
	changed.ResponseJSON = []byte(`{"narrative":"different"}`)
	if _, err := f.repo.CommitAction(ctx, &changed); !errors.Is(err, ErrActionIdempotencyConflict) {
		t.Fatalf("different body=%v", err)
	}
	if got := f.client.HGet(ctx, runtimePlayerKey(f.room, 7), "hp").Val(); got != "8" {
		t.Fatalf("hp=%s", got)
	}
	if got := f.client.SCard(ctx, itemStateKey(f.room, 7)).Val(); got != 1 {
		t.Fatalf("items=%d", got)
	}
	owner := uuid.NewString()
	pending, ack := f.claim(t, owner)
	if !strings.Contains(string(pending.Record.Payload), "9007199254740993") || !strings.Contains(string(pending.ResponseJSON), "9007199254740993") {
		t.Fatal("JSON precision lost")
	}
	if _, err := f.repo.ClaimGameArchive(ctx, f.room, uuid.NewString(), time.Second); !errors.Is(err, ErrGameArchiveLeaseConflict) {
		t.Fatalf("second owner=%v", err)
	}
	first, err := f.repo.AcknowledgeGameArchive(ctx, ack)
	if err != nil || first.Memory.DurablePosition != 1 || first.DeadlineAt != nil {
		t.Fatalf("ACK=%#v %v", first, err)
	}
	duplicate, err := f.repo.AcknowledgeGameArchive(ctx, ack)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate ACK=%#v %v", duplicate, err)
	}
	if _, err := f.repo.BeginMemorySoloAction(ctx, f.room, 7, 1, expected); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.repo.FindMemoryActionResult(ctx, f.room, m.RequestID, m.RequestFingerprint, expected); err != nil || !found {
		t.Fatalf("preflight replay=%v %v", found, err)
	}
	stale := expected
	stale.TimelineID = uuid.NewString()
	if _, _, err := f.repo.FindMemoryActionResult(ctx, f.room, m.RequestID, m.RequestFingerprint, stale); !errors.Is(err, ErrGameArchiveBranchChanged) {
		t.Fatalf("stale replay=%v", err)
	}
}

func TestGameArchiveSoloContract(t *testing.T) {
	_, f := newArchiveFixture(t, "solo", 0)
	testArchiveSoloContract(t, f)
}

func TestGameArchiveLeaseExpiryPauseAndRuntimeExpiry(t *testing.T) {
	for _, expireRuntime := range []bool{false, true} {
		t.Run(fmt.Sprint(expireRuntime), func(t *testing.T) {
			server, f := newArchiveFixture(t, "solo", 0)
			ctx := context.Background()
			if _, err := f.repo.CommitAction(ctx, f.solo(0, 1)); err != nil {
				t.Fatal(err)
			}
			owner := uuid.NewString()
			_, old := f.claim(t, owner)
			if _, err := f.repo.ClaimGameArchive(ctx, f.room, owner, 2*time.Second); err != nil {
				t.Fatal(err)
			}
			server.FastForward(3 * time.Second)
			_, fresh := f.claim(t, uuid.NewString())
			if err := f.repo.ReleaseGameArchive(ctx, old); !errors.Is(err, ErrGameArchiveLeaseConflict) {
				t.Fatalf("old release=%v", err)
			}
			if _, err := f.repo.AcknowledgeGameArchive(ctx, old); !errors.Is(err, ErrGameArchiveLeaseConflict) {
				t.Fatalf("old ACK=%v", err)
			}
			if !expireRuntime {
				if _, err := f.repo.TransitionSoloRoomStatus(ctx, f.room, 7, []model.RoomStatus{model.RoomStatusPlaying}, model.RoomStatusPaused); err != nil {
					t.Fatal(err)
				}
			} else {
				server.FastForward(2 * time.Hour)
				_, fresh = f.claim(t, uuid.NewString())
			}
			result, err := f.repo.AcknowledgeGameArchive(ctx, fresh)
			if err != nil || result.DeadlineAt != nil || result.RuntimeAvailable == expireRuntime {
				t.Fatalf("ACK=%#v %v", result, err)
			}
			if expireRuntime && server.Exists(runtimeTurnKey(f.room)) {
				t.Fatal("ACK rebuilt expired runtime")
			}
			if server.TTL(gameArchiveKeys(f.room)[0]) != 0 {
				t.Fatal("archive metadata has TTL")
			}
		})
	}
}

func (f *archiveFixture) multiplayerAction(t *testing.T, turn int, position uint64) *model.MultiplayerActionMutation {
	t.Helper()
	actor := uint(7 + turn%2)
	request, fp := uuid.NewString(), strings.Repeat("b", 64)
	expected := model.GameArchiveExpectation{TimelineID: f.timeline, Generation: f.generation}
	if _, err := f.repo.AcquireMemoryMultiplayerAction(context.Background(), f.room, actor, turn, request, fp, time.Now(), expected); err != nil {
		t.Fatal(err)
	}
	m := &model.MultiplayerActionMutation{RoomID: f.room, UserID: actor, Generation: f.generation, ExpectedTurn: turn, RequestID: request, RequestFingerprint: fp,
		Messages: []model.RuntimeMessage{{Role: "user", Content: "look"}, {Role: "assistant", Content: "found key"}}, ResponseJSON: []byte(fmt.Sprintf(`{"current_turn":%d,"round_number":%d,"current_actor_id":%d,"deadline_at":"2026-09-29T23:00:00Z","narrative":"found key"}`, turn+1, (turn+1)/2, 7+(turn+1)%2)), NextDeadline: time.Now().Add(time.Minute),
		PlayerMutations: []model.MultiplayerPlayerMutation{{UserID: actor, PlayerStateChanges: map[string]string{"hp": "8"}, ItemMutations: []model.RuntimeItemMutation{{Name: "key", QuantityDelta: 1}}}}}
	m.Archive = f.record("action", turn, turn/2, (turn+1)/2, actor, request, fp, position)
	return m
}

func testArchiveMultiplayerContract(t *testing.T, f *archiveFixture) {
	t.Helper()
	ctx := context.Background()
	m := f.multiplayerAction(t, 0, 1)
	result, err := f.repo.CommitMultiplayerAction(ctx, m)
	if err != nil || result.Snapshot == nil || result.Snapshot.Memory == nil || result.Snapshot.DeadlineAt != nil || result.Snapshot.ActionLease != nil {
		t.Fatalf("commit=%#v %v", result, err)
	}
	expected := model.GameArchiveExpectation{TimelineID: f.timeline, Generation: f.generation}
	if _, err := f.repo.AcquireMemoryMultiplayerAction(ctx, f.room, 8, 1, uuid.NewString(), strings.Repeat("a", 64), time.Now(), expected); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("pending acquire=%v", err)
	}
	if _, err := f.repo.TransitionMemoryMultiplayerRoom(ctx, f.room, expected, uuid.NewString(), model.RoomStatusPlaying, model.RoomStatusPlaying, time.Now().Add(time.Minute)); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("pending resume=%v", err)
	}
	if err := f.repo.ReleaseMultiplayerAction(ctx, m, time.Now().Add(time.Minute)); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("release reopened pending timer=%v", err)
	}
	due, err := f.repo.ListDueMultiplayerDeadlines(ctx, time.Now().Add(time.Hour), 256)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range due {
		if task.RoomID == f.room {
			t.Fatal("pending deadline queued")
		}
	}
	_, ack := f.claim(t, uuid.NewString())
	before := time.Now()
	first, err := f.repo.AcknowledgeGameArchive(ctx, ack)
	if err != nil || first.DeadlineAt == nil || first.DeadlineAt.Before(before.Add(119*time.Second)) || first.DeadlineAt.After(time.Now().Add(121*time.Second)) {
		t.Fatalf("deadline ACK=%#v %v", first, err)
	}
	again, err := f.repo.AcknowledgeGameArchive(ctx, ack)
	if err != nil || !again.Duplicate || !again.DeadlineAt.Equal(*first.DeadlineAt) {
		t.Fatalf("duplicate timer=%#v %v", again, err)
	}
	replay, err := f.repo.CommitMultiplayerAction(ctx, m)
	if err != nil || !replay.Duplicate || !strings.Contains(string(replay.ResponseJSON), `"deadline_at":null`) {
		t.Fatalf("replay=%#v %v", replay, err)
	}
}

func TestGameArchiveMultiplayerContract(t *testing.T) {
	_, f := newArchiveFixture(t, "multiplayer", 0)
	testArchiveMultiplayerContract(t, f)
}

func TestGameArchiveBoundarySnapshotsAreExactAndPersistent(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			server, f := newArchiveFixture(t, mode, 9)
			ctx := context.Background()
			if mode == "solo" {
				if _, err := f.repo.CommitAction(ctx, f.solo(9, 1)); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.repo.CommitMultiplayerAction(ctx, f.multiplayerAction(t, 9, 1)); err != nil {
					t.Fatal(err)
				}
			}
			pending, ack := f.claim(t, uuid.NewString())
			if !json.Valid(pending.BoundarySnapshot) || !strings.Contains(string(pending.BoundarySnapshot), `"hp":"8"`) {
				t.Fatalf("boundary=%s", pending.BoundarySnapshot)
			}
			var boundary struct {
				RecentMessages []model.RuntimeMessage `json:"recent_messages"`
			}
			if json.Unmarshal(pending.BoundarySnapshot, &boundary) != nil || len(boundary.RecentMessages) != 3 {
				t.Fatal("boundary messages invalid")
			}
			if mode == "solo" && boundary.RecentMessages[0].Role != "assistant" {
				t.Fatal("solo snapshot lost newest-first order")
			}
			if mode == "multiplayer" && boundary.RecentMessages[0].Content != "The door opens." {
				t.Fatal("multiplayer snapshot lost chronological order")
			}
			if _, err := f.repo.AcknowledgeGameArchive(ctx, ack); err != nil {
				t.Fatal(err)
			}
			server.FastForward(2 * time.Hour)
			saves, err := f.repo.ListPendingMemoryAutoSaves(ctx, f.room)
			if err != nil || len(saves) != 1 || string(saves[0].Snapshot) != string(pending.BoundarySnapshot) {
				t.Fatalf("saves=%#v %v", saves, err)
			}
			altered := saves[0]
			altered.Snapshot = []byte(`{"wrong":true}`)
			if err := f.repo.AcknowledgeMemoryAutoSave(ctx, f.room, altered); !errors.Is(err, ErrGameArchiveCorrupt) {
				t.Fatalf("wrong snapshot ACK=%v", err)
			}
			if err := f.repo.AcknowledgeMemoryAutoSave(ctx, f.room, saves[0]); err != nil {
				t.Fatal(err)
			}
			if err := f.repo.AcknowledgeMemoryAutoSave(ctx, f.room, saves[0]); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type archiveFaultClient struct {
	redis.Scripter
	target        string
	fired         atomic.Bool
	partial       bool
	partialScript string
}

func (c *archiveFaultClient) EvalSha(ctx context.Context, sha string, keys []string, args ...any) *redis.Cmd {
	if sha != c.target || c.fired.Load() {
		return c.Scripter.EvalSha(ctx, sha, keys, args...)
	}
	if c.partial {
		c.fired.Store(true)
		script := c.partialScript
		if script == "" {
			script = `redis.call('HSET', KEYS[6], 'hp', '1'); return redis.error_reply('injected write failure')`
		}
		return c.Scripter.Eval(ctx, script, keys)
	}
	cmd := c.Scripter.EvalSha(ctx, sha, keys, args...)
	if cmd.Err() == nil {
		c.fired.Store(true)
		cmd.SetErr(errors.New("injected lost response"))
	}
	return cmd
}

func (c *archiveFaultClient) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	cmd := c.Scripter.Eval(ctx, script, keys, args...)
	if fmt.Sprintf("%x", sha1.Sum([]byte(script))) == c.target && !c.fired.Swap(true) && cmd.Err() == nil {
		cmd.SetErr(errors.New("injected lost response"))
	}
	return cmd
}

func testArchiveCommitFaults(t *testing.T, f *archiveFixture, partial bool) {
	t.Helper()
	ctx := context.Background()
	fault := &archiveFaultClient{Scripter: f.client, target: commitActionRuntimeScript.Hash(), partial: partial}
	repository, _ := NewRedisGameStateRepo(fault, time.Hour)
	m := f.solo(0, 1)
	result, err := repository.CommitAction(ctx, m)
	if partial {
		if !errors.Is(err, ErrGameArchiveCommitUnknown) || result != nil {
			t.Fatalf("partial commit=%#v %v", result, err)
		}
		meta, err := f.repo.GetGameArchive(ctx, f.room)
		if err != nil || meta.ArchiveState != "blocked" {
			t.Fatalf("partial ledger=%#v %v", meta, err)
		}
		if _, err := f.repo.BeginMemorySoloAction(ctx, f.room, 7, 0, model.GameArchiveExpectation{TimelineID: f.timeline, Generation: f.generation}); !errors.Is(err, ErrGameArchiveNotReady) {
			t.Fatalf("partial gate=%v", err)
		}
		if f.client.HGet(ctx, runtimePlayerKey(f.room, 7), "hp").Val() != "1" {
			t.Fatal("test did not expose Lua's partial write")
		}
	} else {
		if err != nil || result.Memory == nil || result.CurrentTurn != 1 {
			t.Fatalf("lost response=%#v %v", result, err)
		}
		pending, ack := f.claim(t, uuid.NewString())
		if pending.Record.TurnAfter != 1 {
			t.Fatal("reconciled wrong commit")
		}
		if _, err := f.repo.AcknowledgeGameArchive(ctx, ack); err != nil {
			t.Fatal(err)
		}
		replay, err := repository.CommitAction(ctx, m)
		if err != nil || !replay.Duplicate {
			t.Fatalf("lost response retry=%#v %v", replay, err)
		}
	}
}

func TestGameArchiveCommitResponseLossAndWriteError(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) { _, f := newArchiveFixture(t, "solo", 0); testArchiveCommitFaults(t, f, partial) })
	}
}

func TestGameArchivePrevalidationAndConcurrentCommit(t *testing.T) {
	for _, keyIndex := range []int{2, 3, 4, 5} {
		t.Run(fmt.Sprint(keyIndex), func(t *testing.T) {
			server, f := newArchiveFixture(t, "solo", 0)
			key := gameArchiveKeys(f.room)[keyIndex]
			server.Set(key, "wrong-type")
			if _, err := f.repo.CommitAction(context.Background(), f.solo(0, 1)); !errors.Is(err, ErrGameArchiveCorrupt) {
				t.Fatalf("key type=%v", err)
			}
			assertRedisString(t, server, runtimeTurnKey(f.room), "0")
			if server.HGet(runtimePlayerKey(f.room, 7), "hp") != "10" {
				t.Fatal("prevalidation changed HP")
			}
		})
	}
	server, f := newArchiveFixture(t, "solo", 0)
	ctx := context.Background()
	var wait sync.WaitGroup
	var wins atomic.Int32
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			m := f.solo(0, 1)
			if _, err := f.repo.CommitAction(ctx, m); err == nil {
				wins.Add(1)
			}
		}()
	}
	wait.Wait()
	if wins.Load() != 1 {
		t.Fatalf("commits=%d", wins.Load())
	}
	assertRedisString(t, server, runtimeTurnKey(f.room), "1")
}

func (f *archiveFixture) skip(turn int, position uint64, timeout bool) *model.MultiplayerSkipRequest {
	actor := uint(7 + turn%2)
	reason := "manual"
	namespaceKind := "skip_manual"
	now := time.Now()
	if timeout {
		reason = "timeout"
		namespaceKind = "skip_timeout"
		now = now.Add(time.Hour)
	}
	result := model.MultiplayerSkipResult{Generation: f.generation, SkippedUserID: actor, CurrentTurn: turn + 1, RoundNumber: (turn + 1) / 2, CurrentActorID: uint(7 + (turn+1)%2), Reason: reason, DeadlineAt: time.Now().Add(time.Minute)}
	response, _ := json.Marshal(result)
	request, fp := uuid.NewString(), strings.Repeat("c", 64)
	user := actor
	if timeout {
		user = 0
	}
	r := &model.MultiplayerSkipRequest{RoomID: f.room, UserID: user, Generation: f.generation, ExpectedTurn: turn, RequestID: request, RequestFingerprint: fp, ResponseJSON: response, Now: now, NextDeadline: result.DeadlineAt, Timeout: timeout}
	r.Archive = f.record(namespaceKind, turn, turn/2, (turn+1)/2, actor, request, fp, position)
	return r
}

func TestGameArchiveManualAndTimeoutSkipGate(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			_, f := newArchiveFixture(t, "multiplayer", 0)
			ctx := context.Background()
			request := f.skip(0, 1, timeout)
			result, err := f.repo.SkipMultiplayerTurn(ctx, request)
			if err != nil || result.Memory == nil || !result.DeadlineAt.IsZero() {
				t.Fatalf("skip=%#v %v", result, err)
			}
			encoded, _ := json.Marshal(result)
			if !strings.Contains(string(encoded), `"deadline_at":null`) {
				t.Fatalf("pending skip deadline=%s", encoded)
			}
			replay, err := f.repo.SkipMultiplayerTurn(ctx, request)
			if err != nil || !replay.Duplicate || !replay.DeadlineAt.IsZero() {
				t.Fatalf("skip replay=%#v %v", replay, err)
			}
			if _, err := f.repo.SkipMultiplayerTurn(ctx, f.skip(1, 2, true)); !errors.Is(err, ErrGameArchiveNotReady) {
				t.Fatalf("timeout while pending=%v", err)
			}
			snapshot, err := f.repo.GetMultiplayerRoom(ctx, f.room)
			if err != nil || snapshot.CurrentTurn != 1 || snapshot.DeadlineAt != nil {
				t.Fatalf("pending snapshot=%#v %v", snapshot, err)
			}
			_, ack := f.claim(t, uuid.NewString())
			if _, err := f.repo.AcknowledgeGameArchive(ctx, ack); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGameArchiveMultiplayerPauseACKAndResumeFence(t *testing.T) {
	_, f := newArchiveFixture(t, "multiplayer", 0)
	ctx := context.Background()
	if _, err := f.repo.CommitMultiplayerAction(ctx, f.multiplayerAction(t, 0, 1)); err != nil {
		t.Fatal(err)
	}
	pausedGeneration := uuid.NewString()
	if _, err := f.repo.TransitionMultiplayerRoom(ctx, f.room, f.generation, pausedGeneration, model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{}); err != nil {
		t.Fatal(err)
	}
	expected := model.GameArchiveExpectation{TimelineID: f.timeline, Generation: pausedGeneration}
	if _, err := f.repo.TransitionMemoryMultiplayerRoom(ctx, f.room, expected, uuid.NewString(), model.RoomStatusPaused, model.RoomStatusPlaying, time.Now().Add(time.Minute)); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("resume pending=%v", err)
	}
	_, ack := f.claim(t, uuid.NewString())
	result, err := f.repo.AcknowledgeGameArchive(ctx, ack)
	if err != nil || result.DeadlineAt != nil {
		t.Fatalf("paused ACK=%#v %v", result, err)
	}
	newGeneration := uuid.NewString()
	if _, err := f.repo.TransitionMemoryMultiplayerRoom(ctx, f.room, expected, newGeneration, model.RoomStatusPaused, model.RoomStatusPlaying, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	after, err := f.repo.GetMultiplayerRoom(ctx, f.room)
	if err != nil || after.Generation != newGeneration || after.DeadlineAt == nil {
		t.Fatalf("resume=%#v %v", after, err)
	}
	again, err := f.repo.AcknowledgeGameArchive(ctx, ack)
	if err != nil || !again.DeadlineAt.Equal(*after.DeadlineAt) {
		t.Fatalf("late ACK replaced resume timer=%#v %v", again, err)
	}
}

func TestGameArchiveRetryQueueAndIndependentRooms(t *testing.T) {
	_, first := newArchiveFixture(t, "solo", 0)
	second := setupArchiveFixture(t, first.client, 42, "solo", 0)
	ctx := context.Background()
	for _, f := range []*archiveFixture{first, second} {
		if _, err := f.repo.CommitAction(ctx, f.solo(0, 1)); err != nil {
			t.Fatal(err)
		}
	}
	_, ack := first.claim(t, uuid.NewString())
	retry := time.Now().Add(time.Minute)
	if err := first.repo.RetryGameArchive(ctx, ack, retry); err != nil {
		t.Fatal(err)
	}
	if err := first.repo.RetryGameArchive(ctx, ack, retry); !errors.Is(err, ErrGameArchiveLeaseConflict) {
		t.Fatalf("old retry token=%v", err)
	}
	rooms, err := first.repo.ListPendingArchiveRooms(ctx, time.Now(), 32)
	if err != nil || len(rooms) != 1 || rooms[0] != second.room {
		t.Fatalf("due rooms=%v %v", rooms, err)
	}
	_, otherACK := second.claim(t, uuid.NewString())
	if _, err := second.repo.AcknowledgeGameArchive(ctx, otherACK); err != nil {
		t.Fatal(err)
	}
	if first.client.TTL(ctx, gameArchiveKeys(first.room)[1]).Val() != -1 || first.client.TTL(ctx, gameArchiveKeys(first.room)[3]).Val() != -1 {
		t.Fatal("outbox or queue has TTL")
	}
}

func TestGameArchivePrevalidationRejectsStaleOversizeAndCorruptBoundary(t *testing.T) {
	for _, failure := range []string{"timeline", "generation", "payload", "boundary", "position"} {
		t.Run(failure, func(t *testing.T) {
			server, f := newArchiveFixture(t, "solo", 9)
			ctx := context.Background()
			m := f.solo(9, 1)
			switch failure {
			case "timeline":
				m.Archive.TimelineID = uuid.NewString()
			case "generation":
				m.Generation = uuid.NewString()
				m.Archive.SourceGeneration = m.Generation
			case "payload":
				m.Archive.Payload = []byte(`{"text":"` + strings.Repeat("a", model.GameMemoryMaxPayloadBytes) + `"}`)
			case "boundary":
				server.Set(runtimeKeys(f.room, 7)[3], "summary")
				server.Push(runtimeKeys(f.room, 7)[4], "invalid-json")
			case "position":
				m.Archive.Position = 3
			}
			if _, err := f.repo.CommitAction(ctx, m); err == nil {
				t.Fatal("invalid commit accepted")
			}
			assertRedisString(t, server, runtimeTurnKey(f.room), "9")
			if server.HGet(runtimePlayerKey(f.room, 7), "hp") != "10" {
				t.Fatal("prevalidation changed HP")
			}
			if server.Exists(gameArchiveKeys(f.room)[1]) {
				t.Fatal("rejection created outbox")
			}
		})
	}
}

func TestGameArchiveOneHundredCommitsKeepFullRecordsAndBoundedReplay(t *testing.T) {
	_, f := newArchiveFixture(t, "solo", 0)
	ctx := context.Background()
	records := make(map[string]bool)
	for turn := 0; turn < 100; turn++ {
		m := f.solo(turn, uint64(turn+1))
		if _, err := f.repo.CommitAction(ctx, m); err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		pending, ack := f.claim(t, uuid.NewString())
		if pending.Record.Position != uint64(turn+1) || records[pending.Record.CommitID] {
			t.Fatal("record identity or position incorrect")
		}
		records[pending.Record.CommitID] = true
		if _, err := f.repo.AcknowledgeGameArchive(ctx, ack); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := f.repo.GetGameArchive(ctx, f.room)
	if err != nil || meta.HeadPosition != 100 || meta.DurablePosition != 100 || meta.ArchiveState != "ready" {
		t.Fatalf("head=%#v %v", meta, err)
	}
	if f.client.LLen(ctx, runtimeKeys(f.room, 7)[4]).Val() != 10 {
		t.Fatal("runtime replay grew beyond 10 messages")
	}
	saves, err := f.repo.ListPendingMemoryAutoSaves(ctx, f.room)
	if err != nil || len(saves) != 10 {
		t.Fatalf("boundary saves=%d %v", len(saves), err)
	}
}

func TestGameArchiveTwoWorkersAndConcurrentACK(t *testing.T) {
	_, f := newArchiveFixture(t, "multiplayer", 0)
	ctx := context.Background()
	if _, err := f.repo.CommitMultiplayerAction(ctx, f.multiplayerAction(t, 0, 1)); err != nil {
		t.Fatal(err)
	}
	otherClient := redis.NewClient(&redis.Options{Addr: f.client.Options().Addr})
	defer otherClient.Close()
	otherRepo, _ := NewRedisGameStateRepo(otherClient, time.Hour)
	type claimResult struct {
		pending *model.PendingGameArchive
		err     error
	}
	results := make(chan claimResult, 2)
	var wait sync.WaitGroup
	for _, repository := range []*RedisGameStateRepo{f.repo, otherRepo} {
		wait.Add(1)
		go func(r *RedisGameStateRepo) {
			defer wait.Done()
			pending, err := r.ClaimGameArchive(ctx, f.room, uuid.NewString(), time.Second)
			results <- claimResult{pending, err}
		}(repository)
	}
	wait.Wait()
	close(results)
	var pending *model.PendingGameArchive
	conflicts := 0
	for result := range results {
		if result.err == nil {
			if pending != nil {
				t.Fatal("two owners acquired same record")
			}
			pending = result.pending
		} else if errors.Is(result.err, ErrGameArchiveLeaseConflict) {
			conflicts++
		} else {
			t.Fatal(result.err)
		}
	}
	if pending == nil || conflicts != 1 {
		t.Fatal("lease ownership did not converge")
	}
	record := pending.Record
	ack := model.GameArchiveACK{RoomID: f.room, TimelineID: f.timeline, CommitID: record.CommitID, PayloadHash: record.PayloadHash, OwnerToken: pending.OwnerToken, Position: record.Position}
	type ackResult struct {
		result *model.GameArchiveACKResult
		err    error
	}
	acks := make(chan ackResult, 2)
	for _, repository := range []*RedisGameStateRepo{f.repo, otherRepo} {
		wait.Add(1)
		go func(r *RedisGameStateRepo) {
			defer wait.Done()
			result, err := r.AcknowledgeGameArchive(ctx, ack)
			acks <- ackResult{result, err}
		}(repository)
	}
	wait.Wait()
	close(acks)
	var deadline *time.Time
	duplicates := 0
	for response := range acks {
		if response.err != nil || response.result.DeadlineAt == nil {
			t.Fatalf("ACK=%#v %v", response.result, response.err)
		}
		if response.result.Duplicate {
			duplicates++
		}
		if deadline != nil && !deadline.Equal(*response.result.DeadlineAt) {
			t.Fatal("concurrent ACK changed deadline")
		}
		deadline = response.result.DeadlineAt
	}
	if duplicates != 1 {
		t.Fatalf("duplicate ACKs=%d", duplicates)
	}
}

func TestGameArchiveLargePositionAndLostMetadataFailClosed(t *testing.T) {
	server, f := newArchiveFixture(t, "solo", 0)
	ctx := context.Background()
	keys := gameArchiveKeys(f.room)
	const base uint64 = 9007199254740989
	server.HSet(keys[0], "head_position", fmt.Sprint(base), "durable_position", fmt.Sprint(base))
	if _, err := f.repo.CommitAction(ctx, f.solo(0, base+1)); err != nil {
		t.Fatal(err)
	}
	pending, ack := f.claim(t, uuid.NewString())
	if pending.Record.Position != base+1 {
		t.Fatal("position precision lost")
	}
	if _, err := f.repo.AcknowledgeGameArchive(ctx, ack); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.CommitAction(ctx, f.solo(1, base+2)); !errors.Is(err, model.ErrInvalidMemoryData) {
		t.Fatalf("unsafe position=%v", err)
	}
	server.HSet(keys[1], "position", "1")
	server.Del(keys[0])
	if _, err := f.repo.BeginSoloAction(ctx, f.room, 7, 1); !errors.Is(err, ErrGameArchiveCorrupt) {
		t.Fatalf("lost metadata=%v", err)
	}
	if err := f.repo.DeleteSoloRoom(ctx, f.room, 7); !errors.Is(err, ErrGameArchiveNotReady) {
		t.Fatalf("lost metadata cleanup=%v", err)
	}
}

func TestGameArchiveMultiplayerAndSkipFaultReconciliation(t *testing.T) {
	for _, kind := range []string{"action", "skip"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", kind, partial), func(t *testing.T) {
				_, f := newArchiveFixture(t, "multiplayer", 0)
				ctx := context.Background()
				target := commitMultiplayerActionScript.Hash()
				if kind == "skip" {
					target = skipMultiplayerTurnScript.Hash()
				}
				fault := &archiveFaultClient{Scripter: f.client, target: target, partial: partial, partialScript: `redis.call('SET', KEYS[4], '1'); return redis.error_reply('injected write failure')`}
				repository, _ := NewRedisGameStateRepo(fault, time.Hour)
				var err error
				if kind == "action" {
					_, err = repository.CommitMultiplayerAction(ctx, f.multiplayerAction(t, 0, 1))
				} else {
					_, err = repository.SkipMultiplayerTurn(ctx, f.skip(0, 1, false))
				}
				meta, readErr := f.repo.GetGameArchive(ctx, f.room)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if partial {
					if !errors.Is(err, ErrGameArchiveCommitUnknown) || meta.ArchiveState != "blocked" {
						t.Fatalf("partial commit=%#v %v", meta, err)
					}
				} else {
					if err != nil || meta.HeadPosition != 1 || meta.ArchiveState != "pending" {
						t.Fatalf("lost response=%#v %v", meta, err)
					}
					_, ack := f.claim(t, uuid.NewString())
					if _, err := f.repo.AcknowledgeGameArchive(ctx, ack); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestGameArchiveLostACKResponseDoesNotExtendDeadline(t *testing.T) {
	_, f := newArchiveFixture(t, "multiplayer", 0)
	ctx := context.Background()
	if _, err := f.repo.CommitMultiplayerAction(ctx, f.multiplayerAction(t, 0, 1)); err != nil {
		t.Fatal(err)
	}
	_, ack := f.claim(t, uuid.NewString())
	fault := &archiveFaultClient{Scripter: f.client, target: acknowledgeArchiveScript.Hash()}
	repository, _ := NewRedisGameStateRepo(fault, time.Hour)
	if _, err := repository.AcknowledgeGameArchive(ctx, ack); !errors.Is(err, ErrGameArchiveCommitUnknown) {
		t.Fatalf("lost ACK=%v", err)
	}
	deadline := f.client.Get(ctx, multiplayerDeadlineKey(f.room)).Val()
	retry, err := f.repo.AcknowledgeGameArchive(ctx, ack)
	if err != nil || !retry.Duplicate || f.client.Get(ctx, multiplayerDeadlineKey(f.room)).Val() != deadline {
		t.Fatalf("ACK retry=%#v %v", retry, err)
	}
}
