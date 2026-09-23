package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"trpggame/internal/model"
)

func newMultiplayerRuntimeRepository(t *testing.T) (*miniredis.Miniredis, *RedisGameStateRepo) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repository, err := NewRedisGameStateRepo(client, time.Hour)
	if err != nil {
		t.Fatalf("NewRedisGameStateRepo() error = %v", err)
	}
	return server, repository
}

func validMultiplayerRuntimeState() *model.MultiplayerRuntimeState {
	return &model.MultiplayerRuntimeState{
		RoomID: 41, Generation: uuid.NewString(), TurnOrder: []uint{7, 8},
		Players: []model.MultiplayerRuntimePlayer{
			{UserID: 7, CharacterID: 101, PlayerState: map[string]string{"hp": "12", "location": "hall"}},
			{UserID: 8, CharacterID: 102, PlayerState: map[string]string{"hp": "9"}},
		},
		Opening:     model.RuntimeMessage{Role: "assistant", Content: "The door opens."},
		TurnTimeout: 2 * time.Minute,
	}
}

func TestRedisGameStateRepoMultiplayerStartLifecycle(t *testing.T) {
	server, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.AcquireMultiplayerStartLease(ctx, state.RoomID, "lease-a", time.Minute); err != nil {
		t.Fatalf("AcquireMultiplayerStartLease() error = %v", err)
	}
	if err := repository.AcquireMultiplayerStartLease(ctx, state.RoomID, "lease-b", time.Minute); !errors.Is(err, ErrMultiplayerStartInProgress) {
		t.Fatalf("second lease error = %v", err)
	}
	if err := repository.ReleaseMultiplayerStartLease(ctx, state.RoomID, "wrong"); err != nil {
		t.Fatalf("wrong release error = %v", err)
	}
	if !server.Exists(multiplayerStartLeaseKey(state.RoomID)) {
		t.Fatal("wrong token released start lease")
	}
	if err := repository.ReleaseMultiplayerStartLease(ctx, state.RoomID, "lease-a"); err != nil {
		t.Fatalf("ReleaseMultiplayerStartLease() error = %v", err)
	}

	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatalf("InitializeMultiplayerRoom() error = %v", err)
	}
	if _, err := repository.GetMultiplayerRoom(ctx, state.RoomID); !errors.Is(err, ErrGameRuntimeStatusConflict) {
		t.Fatalf("provisional GetMultiplayerRoom() error = %v", err)
	}
	deadline := time.Now().UTC().Add(state.TurnTimeout).Truncate(time.Millisecond)
	snapshot, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, deadline)
	if err != nil {
		t.Fatalf("ActivateMultiplayerRoom() error = %v", err)
	}
	if snapshot.Version != 2 || snapshot.Status != model.RoomStatusPlaying || snapshot.CurrentTurn != 0 ||
		snapshot.RoundNumber != 0 || snapshot.CurrentActorID != 7 || snapshot.DeadlineAt == nil ||
		!snapshot.DeadlineAt.Equal(deadline) || len(snapshot.Players) != 2 ||
		snapshot.Players[0].CharacterID != 101 || snapshot.Players[0].PlayerState["hp"] != "12" ||
		len(snapshot.RecentMessages) != 1 || snapshot.RecentMessages[0].Content != "The door opens." {
		t.Fatalf("activated snapshot = %#v", snapshot)
	}
	if members, err := server.ZMembers(multiplayerDeadlineQueueKey()); err != nil || len(members) != 1 {
		t.Fatalf("deadline members = %#v, error = %v", members, err)
	}
	if err := repository.PauseMultiplayerRoom(ctx, state.RoomID, state.Generation); err != nil {
		t.Fatalf("PauseMultiplayerRoom() error = %v", err)
	}
	paused, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil || paused.Status != model.RoomStatusPaused || paused.DeadlineAt != nil {
		t.Fatalf("paused snapshot = %#v, error = %v", paused, err)
	}
}

func TestRedisGameStateRepoDeletesOnlyMatchingProvisionalRuntime(t *testing.T) {
	server, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	wrong := *state
	wrong.Generation = uuid.NewString()
	if err := repository.DeleteProvisionalMultiplayerRoom(ctx, &wrong); err != nil {
		t.Fatal(err)
	}
	if !server.Exists(multiplayerRuntimeVersionKey(state.RoomID)) {
		t.Fatal("wrong generation deleted provisional runtime")
	}
	if err := repository.DeleteProvisionalMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	if server.Exists(multiplayerRuntimeVersionKey(state.RoomID)) {
		t.Fatal("matching provisional runtime still exists")
	}
}

func TestRedisGameStateRepoRejectsInvalidMultiplayerRoster(t *testing.T) {
	_, repository := newMultiplayerRuntimeRepository(t)
	state := validMultiplayerRuntimeState()
	state.TurnOrder = []uint{7, 7}
	if err := repository.InitializeMultiplayerRoom(context.Background(), state); !errors.Is(err, ErrInvalidGameRuntimeState) {
		t.Fatalf("InitializeMultiplayerRoom() error = %v", err)
	}
	state = validMultiplayerRuntimeState()
	state.Players[1].CharacterID = state.Players[0].CharacterID
	if err := repository.InitializeMultiplayerRoom(context.Background(), state); !errors.Is(err, ErrInvalidGameRuntimeState) {
		t.Fatalf("InitializeMultiplayerRoom(duplicate character) error = %v", err)
	}
}

func TestRedisGameStateRepoMultiplayerReaderDoesNotInterpretV1SoloRuntime(t *testing.T) {
	server, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	solo := &model.SoloRuntimeState{
		RoomID: 41, UserID: 7, Generation: uuid.NewString(),
		Status: model.RoomStatusPlaying, PlayerState: map[string]string{"hp": "10"},
		Opening: model.RuntimeMessage{Role: "assistant", Content: "solo opening"},
	}
	if err := repository.InitializeSoloRoom(ctx, solo); err != nil {
		t.Fatalf("InitializeSoloRoom() error = %v", err)
	}

	if _, err := repository.GetMultiplayerRoom(ctx, solo.RoomID); !errors.Is(err, ErrGameRuntimeUnavailable) {
		t.Fatalf("GetMultiplayerRoom(V1) error = %v", err)
	}
	v1, err := repository.CaptureSoloRoom(ctx, solo.RoomID, solo.UserID)
	if err != nil || v1.Version != model.SoloRuntimeSnapshotVersion || v1.PlayerState["hp"] != "10" {
		t.Fatalf("CaptureSoloRoom() = %#v, %v", v1, err)
	}
	if server.Exists(multiplayerRuntimeVersionKey(solo.RoomID)) {
		t.Fatal("multiplayer read mutated V1 runtime")
	}
}

func TestRedisGameStateRepoMultiplayerActionLifecycle(t *testing.T) {
	server, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	deadline := now.Add(time.Minute)
	if _, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, deadline); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.NewString()
	fingerprint := strings.Repeat("a", 64)
	acquired, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 7, state.Generation, 0, requestID, fingerprint, now)
	if err != nil || acquired.Generation != state.Generation || acquired.Duplicate {
		t.Fatalf("AcquireMultiplayerAction() = %#v, %v", acquired, err)
	}
	if suspended, err := server.Get(multiplayerDeadlineKey(state.RoomID)); err != nil || suspended != "" {
		t.Fatalf("action acquire did not suspend the deadline: %q, %v", suspended, err)
	}
	if members, _ := server.ZMembers(multiplayerDeadlineQueueKey()); len(members) != 1 {
		t.Fatalf("recovery queue after acquire = %#v", members)
	}
	if due, err := repository.ListDueMultiplayerDeadlines(ctx, now.Add(MultiplayerActionRecoveryTimeout-time.Millisecond), 10); err != nil || len(due) != 0 {
		t.Fatalf("premature recovery deadline = %#v, %v", due, err)
	}
	if inFlight, err := repository.GetMultiplayerRoom(ctx, state.RoomID); err != nil || inFlight.DeadlineAt != nil ||
		inFlight.ActionLease == nil || inFlight.ActionLease.RequestID != requestID {
		t.Fatalf("in-flight snapshot = %#v, %v", inFlight, err)
	}

	response := json.RawMessage(`{"narrative":"done","current_turn":1}`)
	nextDeadline := deadline.Add(time.Minute)
	commit, err := repository.CommitMultiplayerAction(ctx, &model.MultiplayerActionMutation{
		RoomID: state.RoomID, UserID: 7, Generation: state.Generation, ExpectedTurn: 0,
		RequestID: requestID, RequestFingerprint: fingerprint, ResponseJSON: response, NextDeadline: nextDeadline,
		PlayerMutations: []model.MultiplayerPlayerMutation{{
			UserID: 8, PlayerStateChanges: map[string]string{"hp": "11"},
			ItemMutations: []model.RuntimeItemMutation{{Name: "key", QuantityDelta: 1, Description: "brass"}},
			BuffMutations: []model.RuntimeBuffMutation{{Name: "brave", Duration: 2}},
		}},
		Messages: []model.RuntimeMessage{{Role: "user", Content: "inspect"}, {Role: "assistant", Content: "done"}},
	})
	if err != nil || commit.Duplicate || commit.CurrentTurn != 1 || commit.Snapshot == nil || commit.Snapshot.CurrentActorID != 8 {
		t.Fatalf("CommitMultiplayerAction() = %#v, %v", commit, err)
	}
	var player model.MultiplayerRuntimePlayer
	for _, candidate := range commit.Snapshot.Players {
		if candidate.UserID == 8 {
			player = candidate
		}
	}
	if player.PlayerState["hp"] != "11" || len(player.Items) != 1 || len(player.Buffs) != 1 {
		t.Fatalf("mutated player = %#v", player)
	}
	replayed, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 7, state.Generation, 0, requestID, fingerprint, now)
	if err != nil || !replayed.Duplicate || string(replayed.ResponseJSON) != string(response) {
		t.Fatalf("duplicate acquire = %#v, %v", replayed, err)
	}
	if _, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 7, state.Generation, 0, requestID, strings.Repeat("b", 64), now); !errors.Is(err, ErrActionIdempotencyConflict) {
		t.Fatalf("reused request ID error = %v", err)
	}
}

func TestRedisGameStateRepoMultiplayerActionReleaseRestoresDeadline(t *testing.T) {
	server, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	requestID, fingerprint := uuid.NewString(), strings.Repeat("c", 64)
	if _, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 7, state.Generation, 0, requestID, fingerprint, now); err != nil {
		t.Fatal(err)
	}
	restored := now.Add(2 * time.Minute)
	if err := repository.ReleaseMultiplayerAction(ctx, &model.MultiplayerActionMutation{
		RoomID: state.RoomID, UserID: 7, Generation: state.Generation, ExpectedTurn: 0,
		RequestID: requestID, RequestFingerprint: fingerprint,
	}, restored); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil || snapshot.DeadlineAt == nil || !snapshot.DeadlineAt.Equal(restored) || snapshot.CurrentTurn != 0 {
		t.Fatalf("restored snapshot = %#v, %v", snapshot, err)
	}
	if members, _ := server.ZMembers(multiplayerDeadlineQueueKey()); len(members) != 1 {
		t.Fatalf("deadline queue = %#v", members)
	}
}

func TestRedisGameStateRepoMultiplayerSkipAndDueTasks(t *testing.T) {
	_, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	deadline := now.Add(time.Minute)
	if _, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, deadline); err != nil {
		t.Fatal(err)
	}
	if tasks, err := repository.ListDueMultiplayerDeadlines(ctx, deadline.Add(-time.Millisecond), 10); err != nil || len(tasks) != 0 {
		t.Fatalf("early tasks = %#v, %v", tasks, err)
	}
	tasks, err := repository.ListDueMultiplayerDeadlines(ctx, deadline, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Turn != 0 || tasks[0].Generation != state.Generation {
		t.Fatalf("due tasks = %#v, %v", tasks, err)
	}
	result := model.MultiplayerSkipResult{
		Generation: state.Generation, SkippedUserID: 7, CurrentTurn: 1, RoundNumber: 0,
		CurrentActorID: 8, DeadlineAt: deadline.Add(time.Minute), Reason: "timeout",
	}
	response, _ := json.Marshal(result)
	request := &model.MultiplayerSkipRequest{
		RoomID: state.RoomID, Generation: state.Generation, ExpectedTurn: 0, RequestID: uuid.NewString(),
		RequestFingerprint: strings.Repeat("d", 64), ResponseJSON: response, Reason: "timeout",
		Now: deadline, NextDeadline: result.DeadlineAt, Timeout: true,
	}
	skipped, err := repository.SkipMultiplayerTurn(ctx, request)
	if err != nil || skipped.CurrentTurn != 1 || skipped.CurrentActorID != 8 || skipped.SkippedUserID != 7 {
		t.Fatalf("SkipMultiplayerTurn() = %#v, %v", skipped, err)
	}
	replayed, err := repository.SkipMultiplayerTurn(ctx, request)
	if err != nil || !replayed.Duplicate {
		t.Fatalf("duplicate skip = %#v, %v", replayed, err)
	}
}

func TestRedisMultiplayerConcurrentClaimsAndPauseFence(t *testing.T) {
	_, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 8, state.Generation, 0, uuid.NewString(), strings.Repeat("a", 64), now); !errors.Is(err, ErrMultiplayerNotCurrentActor) {
		t.Fatalf("non-actor claim error = %v", err)
	}
	type claim struct {
		id  string
		err error
	}
	claims := make(chan claim, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			id := uuid.NewString()
			_, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 7, state.Generation, 0, id, strings.Repeat("b", 64), now)
			claims <- claim{id: id, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(claims)
	winner := ""
	for result := range claims {
		if result.err == nil {
			if winner != "" {
				t.Fatal("two concurrent claims succeeded")
			}
			winner = result.id
		} else if !errors.Is(result.err, ErrMultiplayerActionInProgress) {
			t.Fatalf("losing claim error = %v", result.err)
		}
	}
	if winner == "" {
		t.Fatal("no claim succeeded")
	}
	skipResponse, _ := json.Marshal(model.MultiplayerSkipResult{
		Generation: state.Generation, SkippedUserID: 7, CurrentTurn: 1, CurrentActorID: 8,
		DeadlineAt: now.Add(2 * time.Minute), Reason: "manual",
	})
	if _, err := repository.SkipMultiplayerTurn(ctx, &model.MultiplayerSkipRequest{
		RoomID: state.RoomID, UserID: 7, Generation: state.Generation, ExpectedTurn: 0,
		RequestID: uuid.NewString(), RequestFingerprint: strings.Repeat("d", 64), ResponseJSON: skipResponse,
		Now: now, NextDeadline: now.Add(2 * time.Minute),
	}); !errors.Is(err, ErrMultiplayerActionInProgress) {
		t.Fatalf("skip during action error = %v", err)
	}
	if err := repository.PauseMultiplayerRoom(ctx, state.RoomID, state.Generation); err != nil {
		t.Fatal(err)
	}
	_, err := repository.CommitMultiplayerAction(ctx, &model.MultiplayerActionMutation{
		RoomID: state.RoomID, UserID: 7, Generation: state.Generation, ExpectedTurn: 0,
		RequestID: winner, RequestFingerprint: strings.Repeat("b", 64),
		ResponseJSON: json.RawMessage(`{"narrative":"late"}`), NextDeadline: now.Add(2 * time.Minute),
		Messages: []model.RuntimeMessage{{Role: "user", Content: "act"}, {Role: "assistant", Content: "late"}},
	})
	if !errors.Is(err, ErrGameRuntimeNotPlaying) {
		t.Fatalf("late commit after pause = %v", err)
	}
	snapshot, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil || snapshot.Status != model.RoomStatusPaused || snapshot.CurrentTurn != 0 {
		t.Fatalf("paused snapshot = %#v, %v", snapshot, err)
	}
}

func TestRedisMultiplayerOldGenerationCannotCommit(t *testing.T) {
	server, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	requestID, fingerprint := uuid.NewString(), strings.Repeat("e", 64)
	if _, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 7, state.Generation, 0, requestID, fingerprint, now); err != nil {
		t.Fatal(err)
	}
	server.Set(runtimeGenerationKey(state.RoomID), uuid.NewString())
	_, err := repository.CommitMultiplayerAction(ctx, &model.MultiplayerActionMutation{
		RoomID: state.RoomID, UserID: 7, Generation: state.Generation, ExpectedTurn: 0,
		RequestID: requestID, RequestFingerprint: fingerprint,
		ResponseJSON: json.RawMessage(`{"narrative":"late"}`), NextDeadline: now.Add(2 * time.Minute),
		Messages: []model.RuntimeMessage{{Role: "user", Content: "act"}, {Role: "assistant", Content: "late"}},
	})
	if !errors.Is(err, ErrGameRuntimeGenerationConflict) {
		t.Fatalf("old-generation commit = %v", err)
	}
	if current, getErr := server.Get(runtimeTurnKey(state.RoomID)); getErr != nil || current != "0" {
		t.Fatalf("turn after old-generation commit = %q, %v", current, getErr)
	}
}

func TestRedisMultiplayerLifecycleRotatesGenerationAndRestoresV2(t *testing.T) {
	server, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	requestID, fingerprint := uuid.NewString(), strings.Repeat("f", 64)
	if _, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 7, state.Generation, 0, requestID, fingerprint, now); err != nil {
		t.Fatal(err)
	}
	pausedGeneration := uuid.NewString()
	if turn, err := repository.TransitionMultiplayerRoom(ctx, state.RoomID, state.Generation, pausedGeneration,
		model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{}); err != nil || turn != 0 {
		t.Fatalf("pause transition=(%d,%v)", turn, err)
	}
	paused, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil || paused.Status != model.RoomStatusPaused || paused.Generation != pausedGeneration ||
		paused.DeadlineAt != nil || paused.ActionLease != nil {
		t.Fatalf("paused runtime=%#v error=%v", paused, err)
	}
	if members, _ := server.ZMembers(multiplayerDeadlineQueueKey()); len(members) != 0 {
		t.Fatalf("pause left deadline members: %#v", members)
	}

	playingGeneration := uuid.NewString()
	deadline := now.Add(3 * time.Minute)
	if turn, err := repository.TransitionMultiplayerRoom(ctx, state.RoomID, pausedGeneration, playingGeneration,
		model.RoomStatusPaused, model.RoomStatusPlaying, deadline); err != nil || turn != 0 {
		t.Fatalf("resume transition=(%d,%v)", turn, err)
	}
	playing, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil || playing.Generation != playingGeneration || playing.DeadlineAt == nil || !playing.DeadlineAt.Equal(deadline) {
		t.Fatalf("resumed runtime=%#v error=%v", playing, err)
	}
	finalPausedGeneration := uuid.NewString()
	if _, err := repository.TransitionMultiplayerRoom(ctx, state.RoomID, playingGeneration, finalPausedGeneration,
		model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.CurrentTurn, snapshot.RoundNumber, snapshot.CurrentActorID = 3, 1, 8
	snapshot.Players[1].PlayerState["hp"] = "4"
	snapshot.Players[1].Items = []model.RuntimeItem{{Name: "key", Quantity: 2, Description: "brass"}}
	snapshot.Players[0].Buffs = []model.RuntimeBuff{{Name: "focus", Duration: 3}}
	snapshot.RecentMessages = []model.RuntimeMessage{
		{Role: "assistant", Content: "opening"}, {Role: "user", Content: "open the door"}, {Role: "assistant", Content: "a key"},
	}
	newGeneration := uuid.NewString()
	snapshot.Generation = newGeneration
	if err := repository.RestoreMultiplayerRoom(ctx, finalPausedGeneration, snapshot); err != nil {
		t.Fatalf("RestoreMultiplayerRoom() error = %v", err)
	}
	restored, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil || restored.Status != model.RoomStatusPaused || restored.Generation != newGeneration ||
		restored.CurrentTurn != 3 || restored.RoundNumber != 1 || restored.CurrentActorID != 8 ||
		restored.Players[1].PlayerState["hp"] != "4" || len(restored.Players[1].Items) != 1 ||
		len(restored.Players[0].Buffs) != 1 || len(restored.RecentMessages) != 3 {
		t.Fatalf("restored runtime=%#v error=%v", restored, err)
	}
	if members, _ := server.ZMembers(multiplayerDeadlineQueueKey()); len(members) != 0 {
		t.Fatalf("restore unexpectedly activated a deadline: %#v", members)
	}
}

func TestRedisMultiplayerRoundBoundaryCreatesAndAcknowledgesAtomicAutoSave(t *testing.T) {
	server, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	if _, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, deadline); err != nil {
		t.Fatal(err)
	}
	for expectedTurn := 0; expectedTurn < 10; expectedTurn++ {
		snapshot, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
		if err != nil {
			t.Fatal(err)
		}
		nextDeadline := deadline.Add(time.Minute)
		result := model.MultiplayerSkipResult{
			Generation: snapshot.Generation, SkippedUserID: snapshot.CurrentActorID, CurrentTurn: expectedTurn + 1,
			RoundNumber:    (expectedTurn + 1) / len(snapshot.TurnOrder),
			CurrentActorID: snapshot.TurnOrder[(expectedTurn+1)%len(snapshot.TurnOrder)],
			DeadlineAt:     nextDeadline, Reason: "manual",
		}
		response, _ := json.Marshal(result)
		fingerprint := fmt.Sprintf("%064x", expectedTurn+1)
		_, err = repository.SkipMultiplayerTurn(ctx, &model.MultiplayerSkipRequest{
			RoomID: state.RoomID, UserID: snapshot.CurrentActorID, Generation: snapshot.Generation,
			ExpectedTurn: expectedTurn, RequestID: uuid.NewString(), RequestFingerprint: fingerprint,
			ResponseJSON: response, Reason: "manual", Now: deadline.Add(-time.Second), NextDeadline: nextDeadline,
		})
		if err != nil {
			t.Fatalf("SkipMultiplayerTurn(turn=%d): %v", expectedTurn, err)
		}
		deadline = nextDeadline
	}
	roomIDs, err := repository.ListPendingMultiplayerAutoSaveRooms(ctx, 16)
	if err != nil || len(roomIDs) != 1 || roomIDs[0] != state.RoomID {
		t.Fatalf("pending room IDs = %#v, error = %v", roomIDs, err)
	}
	pending, err := repository.ListPendingMultiplayerAutoSaves(ctx, state.RoomID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending snapshots = %#v, error = %v", pending, err)
	}
	snapshot := pending[0].Snapshot
	if snapshot.RoundNumber != 5 || snapshot.CurrentTurn != 10 || snapshot.Status != model.RoomStatusPlaying ||
		snapshot.CurrentActorID != 7 || snapshot.DeadlineAt == nil || len(snapshot.Players) != 2 ||
		snapshot.Players[0].Items == nil || snapshot.Players[0].Buffs == nil || len(snapshot.RecentMessages) != 1 {
		t.Fatalf("auto-save snapshot = %#v", snapshot)
	}
	generation := pending[0].Generation
	if err := repository.AcknowledgeMultiplayerAutoSave(ctx, state.RoomID, 5, generation); err != nil {
		t.Fatal(err)
	}
	pending, err = repository.ListPendingMultiplayerAutoSaves(ctx, state.RoomID)
	roomIDs, roomErr := repository.ListPendingMultiplayerAutoSaveRooms(ctx, 16)
	if err != nil || roomErr != nil || len(pending) != 0 || len(roomIDs) != 0 || server.Exists(pendingMultiplayerAutoSavesKey(state.RoomID)) {
		t.Fatalf("after ack pending=%#v rooms=%#v errors=(%v,%v)", pending, roomIDs, err, roomErr)
	}
	if err := repository.AcknowledgeMultiplayerAutoSave(ctx, state.RoomID, 5, generation); err != nil {
		t.Fatalf("idempotent acknowledge: %v", err)
	}
}

func TestRedisMultiplayerActionCommitCapturesRoundBoundaryAutoSave(t *testing.T) {
	_, repository := newMultiplayerRuntimeRepository(t)
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := repository.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	if _, err := repository.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, deadline); err != nil {
		t.Fatal(err)
	}
	for expectedTurn := 0; expectedTurn < 9; expectedTurn++ {
		snapshot, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
		if err != nil {
			t.Fatal(err)
		}
		nextDeadline := deadline.Add(time.Minute)
		result := model.MultiplayerSkipResult{
			Generation: snapshot.Generation, SkippedUserID: snapshot.CurrentActorID, CurrentTurn: expectedTurn + 1,
			RoundNumber:    (expectedTurn + 1) / len(snapshot.TurnOrder),
			CurrentActorID: snapshot.TurnOrder[(expectedTurn+1)%len(snapshot.TurnOrder)],
			DeadlineAt:     nextDeadline, Reason: "manual",
		}
		response, _ := json.Marshal(result)
		_, err = repository.SkipMultiplayerTurn(ctx, &model.MultiplayerSkipRequest{
			RoomID: state.RoomID, UserID: snapshot.CurrentActorID, Generation: snapshot.Generation,
			ExpectedTurn: expectedTurn, RequestID: uuid.NewString(), RequestFingerprint: fmt.Sprintf("%064x", expectedTurn+1),
			ResponseJSON: response, Reason: "manual", Now: deadline.Add(-time.Second), NextDeadline: nextDeadline,
		})
		if err != nil {
			t.Fatalf("SkipMultiplayerTurn(turn=%d): %v", expectedTurn, err)
		}
		deadline = nextDeadline
	}
	snapshot, err := repository.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil || snapshot.CurrentTurn != 9 || snapshot.CurrentActorID != 8 {
		t.Fatalf("before action snapshot=%#v error=%v", snapshot, err)
	}
	requestID, fingerprint := uuid.NewString(), strings.Repeat("a", 64)
	if _, err := repository.AcquireMultiplayerAction(ctx, state.RoomID, 8, snapshot.Generation, 9,
		requestID, fingerprint, deadline.Add(-time.Second)); err != nil {
		t.Fatalf("AcquireMultiplayerAction() error = %v", err)
	}
	nextDeadline := deadline.Add(time.Minute)
	response, _ := json.Marshal(map[string]any{
		"current_turn": 10, "round_number": 5, "current_actor_id": 7, "deadline_at": nextDeadline,
	})
	commit, err := repository.CommitMultiplayerAction(ctx, &model.MultiplayerActionMutation{
		RoomID: state.RoomID, UserID: 8, Generation: snapshot.Generation, ExpectedTurn: 9,
		RequestID: requestID, RequestFingerprint: fingerprint, ResponseJSON: response, NextDeadline: nextDeadline,
		Messages: []model.RuntimeMessage{{Role: "user", Content: "I open the gate."}, {Role: "assistant", Content: "The gate yields."}},
	})
	if err != nil || commit == nil || commit.CurrentTurn != 10 {
		t.Fatalf("CommitMultiplayerAction() = (%#v, %v)", commit, err)
	}
	pending, err := repository.ListPendingMultiplayerAutoSaves(ctx, state.RoomID)
	if err != nil || len(pending) != 1 || pending[0].Snapshot.CurrentTurn != 10 || pending[0].Snapshot.RoundNumber != 5 ||
		len(pending[0].Snapshot.RecentMessages) != 3 {
		t.Fatalf("pending action auto-save = %#v, error=%v", pending, err)
	}
}

func TestRedisMultiplayerTimeoutOnlyAdvancesOnceAcrossRepositories(t *testing.T) {
	server, first := newMultiplayerRuntimeRepository(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	second, err := NewRedisGameStateRepo(client, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state := validMultiplayerRuntimeState()
	if err := first.InitializeMultiplayerRoom(ctx, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	deadline := now.Add(time.Minute)
	if _, err := first.ActivateMultiplayerRoom(ctx, state.RoomID, state.Generation, deadline); err != nil {
		t.Fatal(err)
	}
	result := model.MultiplayerSkipResult{
		Generation: state.Generation, SkippedUserID: 7, CurrentTurn: 1, CurrentActorID: 8,
		DeadlineAt: deadline.Add(time.Minute), Reason: "timeout",
	}
	encoded, _ := json.Marshal(result)
	requestID := uuid.NewString()
	request := &model.MultiplayerSkipRequest{
		RoomID: state.RoomID, Generation: state.Generation, ExpectedTurn: 0, RequestID: requestID,
		RequestFingerprint: strings.Repeat("c", 64), ResponseJSON: encoded, Now: deadline,
		NextDeadline: result.DeadlineAt, Timeout: true,
	}
	type skipResult struct {
		value *model.MultiplayerSkipResult
		err   error
	}
	results := make(chan skipResult, 2)
	var wait sync.WaitGroup
	for _, repository := range []*RedisGameStateRepo{first, second} {
		wait.Add(1)
		go func(repository *RedisGameStateRepo) {
			defer wait.Done()
			value, err := repository.SkipMultiplayerTurn(ctx, request)
			results <- skipResult{value: value, err: err}
		}(repository)
	}
	wait.Wait()
	close(results)
	fresh, duplicate := 0, 0
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("competing skip error = %v", outcome.err)
		}
		if outcome.value.Duplicate {
			duplicate++
		} else {
			fresh++
		}
	}
	if fresh != 1 || duplicate != 1 {
		t.Fatalf("skip outcomes: fresh=%d duplicate=%d", fresh, duplicate)
	}
	snapshot, err := first.GetMultiplayerRoom(ctx, state.RoomID)
	if err != nil || snapshot.CurrentTurn != 1 {
		t.Fatalf("snapshot after competing skips = %#v, %v", snapshot, err)
	}
}
