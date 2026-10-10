package service

import (
	"context"
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
	"trpggame/internal/config"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type archiveTestStore struct {
	mu      sync.Mutex
	records map[string]string
	err     error
	calls   int
	started chan struct{}
	wait    bool
}

type archiveStatusRecorder struct {
	states []string
}

func (r *archiveStatusRecorder) PublishGameArchiveStatus(_ uint, archive *model.GameArchiveRuntime) {
	r.states = append(r.states, archive.ArchiveState)
}

func TestGameArchiveServiceNotifiesFinalArchiveState(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%t", blocked), func(t *testing.T) {
			_, _, svc, store := unitArchiveFixture(t, "solo")
			if blocked {
				store.err = repo.ErrMemoryConflict
			}
			recorder := &archiveStatusRecorder{}
			svc.ConfigureNotifier(recorder)
			_, err := svc.ArchivePending(context.Background(), 41)
			if blocked && !errors.Is(err, repo.ErrMemoryConflict) {
				t.Fatalf("blocked err=%v", err)
			}
			if !blocked && err != nil {
				t.Fatal(err)
			}
			want := "ready"
			if blocked {
				want = "blocked"
			}
			if len(recorder.states) != 1 || recorder.states[0] != want {
				t.Fatalf("archive status notifications=%v, want %s", recorder.states, want)
			}
		})
	}
}

type archiveBarrierStore struct {
	GameArchivePersistence
	committed chan struct{}
	proceed   chan struct{}
}

func (s *archiveBarrierStore) ArchiveAndAdvance(ctx context.Context, r *model.GameActionRecord) (bool, error) {
	inserted, err := s.GameArchivePersistence.ArchiveAndAdvance(ctx, r)
	if err == nil {
		close(s.committed)
		<-s.proceed
	}
	return inserted, err
}

func (s *archiveTestStore) ArchiveAndAdvance(ctx context.Context, r *model.GameActionRecord) (bool, error) {
	s.mu.Lock()
	s.calls++
	issue, wait := s.err, s.wait
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
	if wait {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if issue != nil {
		return false, issue
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]string)
	}
	if hash, exists := s.records[r.CommitID]; exists {
		if hash != r.PayloadHash {
			return false, repo.ErrMemoryConflict
		}
		return false, nil
	}
	s.records[r.CommitID] = r.PayloadHash
	return true, nil
}

type archiveFaultRuntime struct {
	GameArchiveRuntimeRepository
	ackCalls        atomic.Int32
	claims          atomic.Int32
	lostACK         bool
	ackUnavailable  bool
	readUnavailable bool
}

func (r *archiveFaultRuntime) GetGameArchive(ctx context.Context, id uint) (*model.GameArchiveRuntime, error) {
	if r.readUnavailable {
		return nil, repo.ErrGameRuntimeUnavailable
	}
	return r.GameArchiveRuntimeRepository.GetGameArchive(ctx, id)
}

func (r *archiveFaultRuntime) ClaimGameArchive(ctx context.Context, id uint, owner string, ttl time.Duration) (*model.PendingGameArchive, error) {
	r.claims.Add(1)
	return r.GameArchiveRuntimeRepository.ClaimGameArchive(ctx, id, owner, ttl)
}

func (r *archiveFaultRuntime) AcknowledgeGameArchive(ctx context.Context, ack model.GameArchiveACK) (*model.GameArchiveACKResult, error) {
	call := r.ackCalls.Add(1)
	if r.ackUnavailable {
		return nil, repo.ErrGameRuntimeUnavailable
	}
	result, err := r.GameArchiveRuntimeRepository.AcknowledgeGameArchive(ctx, ack)
	if r.lostACK && call == 1 && err == nil {
		return nil, repo.ErrGameArchiveCommitUnknown
	}
	return result, err
}

// Reused by real MySQL/Redis integration tests: no private Redis keys are needed
// to create a pending record, and no AI inference client exists in this fixture.
func archiveRuntimeFixture(t *testing.T, client *redis.Client, room uint, timeline, generation string, revision, position uint64, mode string) *repo.RedisGameStateRepo {
	t.Helper()
	ctx := context.Background()
	runtime, err := repo.NewRedisGameStateRepo(client, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	timeout := time.Duration(0)
	if mode == "solo" {
		err = runtime.InitializeSoloRoom(ctx, &model.SoloRuntimeState{RoomID: room, UserID: 7, Generation: generation, Status: model.RoomStatusPlaying,
			PlayerState: map[string]string{"hp": "10"}, Opening: model.RuntimeMessage{Role: "assistant", Content: "opening"}})
	} else {
		timeout = 2 * time.Minute
		err = runtime.InitializeMultiplayerRoom(ctx, &model.MultiplayerRuntimeState{RoomID: room, Generation: generation, TurnOrder: []uint{7, 8}, TurnTimeout: timeout,
			Players: []model.MultiplayerRuntimePlayer{{UserID: 7, CharacterID: 101, PlayerState: map[string]string{"hp": "10"}}, {UserID: 8, CharacterID: 102, PlayerState: map[string]string{"hp": "10"}}},
			Opening: model.RuntimeMessage{Role: "assistant", Content: "opening"}})
		if err == nil {
			_, err = runtime.ActivateMultiplayerRoom(ctx, room, generation, time.Now().Add(timeout))
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.BindGameArchive(ctx, model.GameArchiveBinding{RoomID: room, TimelineID: timeline, Generation: generation, Mode: mode, Revision: revision, Position: position, TurnTimeout: timeout}); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func commitArchiveFixture(t *testing.T, runtime *repo.RedisGameStateRepo, room uint, timeline, generation string, revision, position uint64, mode string) {
	t.Helper()
	ctx := context.Background()
	actor := uint(7)
	request, fingerprint := uuid.NewString(), strings.Repeat("a", 64)
	round := 1
	if mode == "multiplayer" {
		round = 0
	}
	record := &model.GameActionRecord{CommitID: uuid.NewString(), RoomID: room, TimelineID: timeline, Position: position + 1, Kind: "action", ActorID: &actor,
		RequestNamespace: "client", RequestID: request, Fingerprint: fingerprint, SourceGeneration: generation, SourceRevision: revision, TurnAfter: 1, RoundAfter: round, PayloadVersion: 1}
	messages := []model.RuntimeMessage{{Role: "user", Content: "look"}, {Role: "assistant", Content: "found key"}}
	response := []byte(fmt.Sprintf(`{"narrative":"found key","current_turn":1,"round_number":%d,"generation":"%s"}`, round, generation))
	if mode == "solo" {
		_, err := runtime.CommitAction(ctx, &model.ActionRuntimeMutation{RoomID: room, UserID: 7, Generation: generation, RequestID: request, RequestFingerprint: fingerprint,
			ResponseJSON: response, Messages: messages, PlayerStateChanges: map[string]string{"hp": "8"}, Archive: record})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := runtime.AcquireMemoryMultiplayerAction(ctx, room, 7, 0, request, fingerprint, time.Now(), model.GameArchiveExpectation{TimelineID: timeline, Generation: generation}); err != nil {
			t.Fatal(err)
		}
		_, err := runtime.CommitMultiplayerAction(ctx, &model.MultiplayerActionMutation{RoomID: room, UserID: 7, Generation: generation, RequestID: request, RequestFingerprint: fingerprint,
			ResponseJSON: response, Messages: messages, NextDeadline: time.Now().Add(time.Minute), Archive: record,
			PlayerMutations: []model.MultiplayerPlayerMutation{{UserID: 7, PlayerStateChanges: map[string]string{"hp": "8"}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func unitArchiveFixture(t *testing.T, mode string) (*miniredis.Miniredis, *repo.RedisGameStateRepo, *GameArchiveService, *archiveTestStore) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { client.Close() })
	timeline, generation := uuid.NewString(), uuid.NewString()
	runtime := archiveRuntimeFixture(t, client, 41, timeline, generation, 1, 0, mode)
	commitArchiveFixture(t, runtime, 41, timeline, generation, 1, 0, mode)
	store := &archiveTestStore{}
	svc, err := NewGameArchiveService(runtime, store, config.DefaultGameArchiveConfig())
	if err != nil {
		t.Fatal(err)
	}
	return server, runtime, svc, store
}

func TestGameArchiveServiceConfirmedAndLostACK(t *testing.T) {
	for _, mode := range []string{"solo", "multiplayer"} {
		t.Run(mode, func(t *testing.T) {
			_, runtime, svc, store := unitArchiveFixture(t, mode)
			fault := &archiveFaultRuntime{GameArchiveRuntimeRepository: runtime, lostACK: true}
			svc.runtime = fault
			result, err := svc.ArchivePending(context.Background(), 41)
			if err != nil || result == nil || !result.Duplicate || result.Memory.DurablePosition != 1 || len(store.records) != 1 || fault.ackCalls.Load() != 2 {
				t.Fatalf("ACK replay: %#v %v", result, err)
			}
			if mode == "multiplayer" && result.DeadlineAt == nil {
				t.Fatal("missing authoritative timer")
			}
			if result, err = svc.ArchivePending(context.Background(), 41); err != nil || result != nil || store.calls != 1 {
				t.Fatalf("empty retry: %#v %v calls=%d", result, err, store.calls)
			}
		})
	}
}

func TestGameArchiveServiceStorageFailureBackoffAndRestart(t *testing.T) {
	_, runtime, svc, store := unitArchiveFixture(t, "solo")
	store.err = errors.New("storage unavailable")
	if _, err := svc.ArchivePending(context.Background(), 41); err == nil {
		t.Fatal("failure hidden")
	}
	memory, _ := runtime.GetGameArchive(context.Background(), 41)
	if memory.ArchiveState != "pending" || memory.DurablePosition != 0 {
		t.Fatalf("gate lost: %#v", memory)
	}
	rooms, err := runtime.ListPendingArchiveRooms(context.Background(), time.Now(), 32)
	if err != nil || len(rooms) != 0 {
		t.Fatalf("missing backoff: %v %v", rooms, err)
	}
	pending, err := runtime.ClaimGameArchive(context.Background(), 41, uuid.NewString(), time.Second)
	if err != nil || pending.Attempts != 1 {
		t.Fatalf("persistent attempts: %#v %v", pending, err)
	}
	ack := model.GameArchiveACK{RoomID: 41, TimelineID: pending.Record.TimelineID, CommitID: pending.Record.CommitID, PayloadHash: pending.Record.PayloadHash, Position: 1, OwnerToken: pending.OwnerToken}
	if err = runtime.RetryGameArchive(context.Background(), ack, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	store.err = nil
	restarted, _ := NewGameArchiveService(runtime, store, svc.options)
	w := NewGameArchiveWorker(restarted)
	w.processDue()
	memory, _ = runtime.GetGameArchive(context.Background(), 41)
	if memory.ArchiveState != "ready" || len(store.records) != 1 {
		t.Fatalf("restart: %#v", memory)
	}
	if svc.retryDelay(30) != 30*time.Second {
		t.Fatal("backoff unbounded")
	}
}

func TestGameArchiveServiceACKFailureReplaysMySQL(t *testing.T) {
	_, runtime, svc, store := unitArchiveFixture(t, "multiplayer")
	svc.runtime = &archiveFaultRuntime{GameArchiveRuntimeRepository: runtime, ackUnavailable: true}
	if _, err := svc.ArchivePending(context.Background(), 41); err == nil {
		t.Fatal("ACK failure hidden")
	}
	if len(store.records) != 1 {
		t.Fatal("fact absent")
	}
	svc.runtime = runtime
	if _, err := svc.ArchivePending(context.Background(), 41); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 1 || store.calls != 2 {
		t.Fatal("record duplicated or not replayed")
	}
}

func TestGameArchiveServiceConflictQuarantinesExactPending(t *testing.T) {
	_, runtime, svc, store := unitArchiveFixture(t, "solo")
	store.err = repo.ErrMemoryConflict
	if _, err := svc.ArchivePending(context.Background(), 41); !errors.Is(err, repo.ErrMemoryConflict) {
		t.Fatal(err)
	}
	memory, _ := runtime.GetGameArchive(context.Background(), 41)
	rooms, _ := runtime.ListPendingArchiveRooms(context.Background(), time.Now().Add(time.Hour), 32)
	if memory.ArchiveState != "blocked" || memory.HeadPosition != 1 || memory.DurablePosition != 0 || len(rooms) != 0 {
		t.Fatalf("quarantine: %#v %v", memory, rooms)
	}
}

func TestGameArchiveWorkerShutdownAndLeaseRenewal(t *testing.T) {
	_, runtime, svc, store := unitArchiveFixture(t, "solo")
	store.wait = true
	store.started = make(chan struct{}, 1)
	svc.options.LeaseMS = 1000
	svc.options.ShutdownTimeoutMS = 1500
	fault := &archiveFaultRuntime{GameArchiveRuntimeRepository: runtime}
	svc.runtime = fault
	w := NewGameArchiveWorker(svc)
	go w.Run()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	deadline := time.Now().Add(2 * time.Second)
	for fault.claims.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fault.claims.Load() < 2 {
		t.Fatal("lease not renewed")
	}
	start := time.Now()
	w.Stop()
	w.Stop()
	if time.Since(start) > time.Second {
		t.Fatal("shutdown not bounded")
	}
	memory, _ := runtime.GetGameArchive(context.Background(), 41)
	if memory.ArchiveState != "pending" || memory.DurablePosition != 0 {
		t.Fatalf("shutdown dropped fact: %#v", memory)
	}
	store.mu.Lock()
	store.wait = false
	store.mu.Unlock()
	if _, err := svc.ArchivePending(context.Background(), 41); err != nil {
		t.Fatalf("shutdown takeover: %v", err)
	}
}

func TestGameArchiveServiceCanceledSubmissionStillConfirms(t *testing.T) {
	_, _, svc, store := unitArchiveFixture(t, "solo")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.ArchivePending(ctx, 41); err != nil || len(store.records) != 1 {
		t.Fatalf("canceled submit: %v", err)
	}
}

func TestGameArchiveProgressBridgeReturnsPendingWithoutLegacyRepair(t *testing.T) {
	_, runtime, svc, store := unitArchiveFixture(t, "multiplayer")
	store.err = errors.New("MySQL unavailable")
	game := &GameService{archiveService: svc} // Nil legacy repository catches any bypass.
	if handled, err := game.confirmArchiveProgress(context.Background(), 41); !handled || err != nil {
		t.Fatalf("committed pending lost: %v %v", handled, err)
	}
	snapshot, _ := runtime.GetMultiplayerRoom(context.Background(), 41)
	stale := time.Now().Add(-time.Hour)
	result := &SubmitGameActionResult{Generation: snapshot.Generation, CurrentTurn: 1, DeadlineAt: &stale}
	game.refreshArchiveResult(context.Background(), 41, result)
	if result.Memory == nil || result.Memory.ArchiveState != "pending" || result.DeadlineAt != nil {
		t.Fatalf("pending result: %#v", result)
	}
	store.err = nil
	if handled, err := game.confirmArchiveProgress(context.Background(), 41); !handled || err != nil {
		t.Fatal(err)
	}
	game.refreshArchiveResult(context.Background(), 41, result)
	if result.Memory.ArchiveState != "ready" || result.DeadlineAt == nil || time.Until(*result.DeadlineAt) < 110*time.Second {
		t.Fatalf("confirmed result: %#v", result)
	}
	result.CurrentTurn = 0
	game.refreshArchiveResult(context.Background(), 41, result)
	if result.DeadlineAt != nil {
		t.Fatal("replayed older result advertised current deadline")
	}
}

func TestGameArchiveKnownCommitDoesNotBecomeFailureOnDeliveryRead(t *testing.T) {
	_, runtime, svc, _ := unitArchiveFixture(t, "solo")
	svc.runtime = &archiveFaultRuntime{GameArchiveRuntimeRepository: runtime, readUnavailable: true, ackUnavailable: true}
	game := &GameService{archiveService: svc}
	memory, _ := runtime.GetGameArchive(context.Background(), 41)
	if err := game.advancePersistentGameProgress(context.Background(), 41, 7, 1, memory); err != nil {
		t.Fatalf("known commit became failed action: %v", err)
	}
	result := &SubmitGameActionResult{CurrentTurn: 1, Memory: memory}
	game.refreshArchiveResult(context.Background(), 41, result)
	if result.Memory == nil || result.Memory.ArchiveState != "recovering" {
		t.Fatalf("uncertain delivery: %#v", result)
	}
}

func TestGameArchiveServiceCorruptClaimDoesNotStarveQueue(t *testing.T) {
	server, runtime, svc, _ := unitArchiveFixture(t, "solo")
	key := "game:archive:room:41:pending"
	server.HSet(key, "record_json", `{"corrupt":true}`)
	if _, err := svc.ArchivePending(context.Background(), 41); !errors.Is(err, repo.ErrGameArchiveCorrupt) {
		t.Fatalf("claim=%v", err)
	}
	rooms, err := runtime.ListPendingArchiveRooms(context.Background(), time.Now().Add(time.Hour), 32)
	if err != nil || len(rooms) != 0 || !server.Exists(key) || server.HGet("game:archive:room:41:meta", "archive_state") != "blocked" {
		t.Fatalf("corrupt queue: %v %v", rooms, err)
	}
	// A room already blocked by commit reconciliation must also leave the poll queue.
	server.ZAdd("game:archive:pending_rooms", 0, "41")
	if _, err = svc.ArchivePending(context.Background(), 41); !errors.Is(err, repo.ErrGameArchiveNotReady) {
		t.Fatal(err)
	}
	rooms, _ = runtime.ListPendingArchiveRooms(context.Background(), time.Now().Add(time.Hour), 32)
	if len(rooms) != 0 {
		t.Fatal("blocked commit starves polling")
	}
}

func TestGameArchiveServiceOldOwnerCannotACKAfterTakeover(t *testing.T) {
	server, runtime, svc, store := unitArchiveFixture(t, "multiplayer")
	barrier := &archiveBarrierStore{GameArchivePersistence: store, committed: make(chan struct{}), proceed: make(chan struct{})}
	svc.store = barrier
	finished := make(chan error, 1)
	go func() { _, err := svc.ArchivePending(context.Background(), 41); finished <- err }()
	select {
	case <-barrier.committed:
	case <-time.After(time.Second):
		t.Fatal("MySQL phase not reached")
	}
	server.FastForward(31 * time.Second)
	pending, err := runtime.ClaimGameArchive(context.Background(), 41, uuid.NewString(), time.Second)
	if err != nil || pending == nil {
		t.Fatalf("takeover: %#v %v", pending, err)
	}
	if _, err = store.ArchiveAndAdvance(context.Background(), pending.Record); err != nil {
		t.Fatal(err)
	}
	ack := model.GameArchiveACK{RoomID: 41, TimelineID: pending.Record.TimelineID, Position: 1, CommitID: pending.Record.CommitID, PayloadHash: pending.Record.PayloadHash, OwnerToken: pending.OwnerToken}
	first, err := runtime.AcknowledgeGameArchive(context.Background(), ack)
	if err != nil {
		t.Fatal(err)
	}
	close(barrier.proceed)
	if err = <-finished; err == nil {
		t.Fatal("old owner acknowledged takeover")
	}
	again, err := runtime.AcknowledgeGameArchive(context.Background(), ack)
	if err != nil || !again.Duplicate || !again.DeadlineAt.Equal(*first.DeadlineAt) || len(store.records) != 1 {
		t.Fatalf("takeover changed timer/fact: %#v %v", again, err)
	}
}

func TestGameArchiveMultiplayerPendingDecodeKeepsLegacyValidation(t *testing.T) {
	base := fmt.Sprintf(`{"narrative":"committed","generation":"%s","current_turn":1,"current_actor_id":8,"multiplayer_effects":{"players":[],"events":[]},"deadline_at":null`, uuid.NewString())
	if _, err := decodeMultiplayerActionResult([]byte(base+`}`), true); err == nil {
		t.Fatal("legacy null deadline accepted")
	}
	metadata := fmt.Sprintf(`,"memory":{"timeline_id":"%s","head_position":1,"durable_position":0,"archive_state":"pending","revision":2}}`, uuid.NewString())
	result, err := decodeMultiplayerActionResult([]byte(base+metadata), true)
	if err != nil || result.Memory == nil || result.DeadlineAt != nil {
		t.Fatalf("pending decode: %#v %v", result, err)
	}
	if _, err = decodeMultiplayerActionResult([]byte(base+strings.Replace(metadata, `"revision":2`, `"revision":0`, 1)), true); err == nil {
		t.Fatal("damaged metadata accepted")
	}
}
