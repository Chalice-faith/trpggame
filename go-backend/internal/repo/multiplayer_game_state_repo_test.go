package repo

import (
	"context"
	"errors"
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
