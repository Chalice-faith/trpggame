package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"trpggame/internal/ai_client"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type multiplayerTurnGameStub struct {
	GameRepository
	room    model.GameRoom
	players []model.RoomPlayer
	turn    int
	round   int
}

func (r *multiplayerTurnGameStub) FindRoomByID(_ context.Context, id uint) (*model.GameRoom, error) {
	if id != r.room.ID {
		return nil, gorm.ErrRecordNotFound
	}
	return &r.room, nil
}

func (r *multiplayerTurnGameStub) FindRoomByIDAndOwnerID(ctx context.Context, id, ownerID uint) (*model.GameRoom, error) {
	if ownerID != r.room.OwnerID {
		return nil, gorm.ErrRecordNotFound
	}
	return r.FindRoomByID(ctx, id)
}

func (r *multiplayerTurnGameStub) FindPlayer(_ context.Context, roomID, userID uint) (*model.RoomPlayer, error) {
	if roomID == r.room.ID {
		for index := range r.players {
			if r.players[index].UserID == userID {
				return &r.players[index], nil
			}
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *multiplayerTurnGameStub) FindPlayersByRoom(_ context.Context, roomID uint) ([]model.RoomPlayer, error) {
	if roomID != r.room.ID {
		return nil, gorm.ErrRecordNotFound
	}
	return r.players, nil
}

func (r *multiplayerTurnGameStub) AdvanceMultiplayerRoomProgress(_ context.Context, roomID uint, turn, round int) (bool, error) {
	if roomID != r.room.ID {
		return false, nil
	}
	if turn > r.turn {
		r.turn = turn
	}
	if round > r.round {
		r.round = round
	}
	return true, nil
}

type multiplayerTurnAIStub struct {
	GameInferenceClient
	calls    int
	request  *ai_client.GameActionRequest
	response *ai_client.GameActionResponse
	err      error
}

func (a *multiplayerTurnAIStub) SubmitActionStream(_ context.Context, request *ai_client.GameActionRequest, handler ai_client.ActionStreamHandler) (*ai_client.GameActionResponse, error) {
	a.calls++
	a.request = request
	if err := handler(ai_client.ActionStreamEvent{Type: "narrative_chunk", Content: "门开了"}); err != nil {
		return nil, err
	}
	if a.err != nil {
		return nil, a.err
	}
	return a.response, nil
}

type multiplayerTurnEventRecorder struct {
	mu     sync.Mutex
	events []GameActionStreamEvent
}

func (p *multiplayerTurnEventRecorder) PublishMultiplayerActionEvent(_ uint, _ string, event GameActionStreamEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

func (p *multiplayerTurnEventRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

func multiplayerTurnFixture(t *testing.T) (*GameService, *multiplayerTurnGameStub, *repo.RedisGameStateRepo, *multiplayerTurnAIStub, *multiplayerTurnEventRecorder, time.Time) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	runtime, err := repo.NewRedisGameStateRepo(client, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	generation := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	state := &model.MultiplayerRuntimeState{
		RoomID: 41, Generation: generation, TurnOrder: []uint{7, 8}, TurnTimeout: 2 * time.Minute,
		Players: []model.MultiplayerRuntimePlayer{
			{UserID: 7, CharacterID: 101, PlayerState: map[string]string{"hp": "12"}},
			{UserID: 8, CharacterID: 102, PlayerState: map[string]string{"hp": "9"}},
		},
		Opening: model.RuntimeMessage{Role: "assistant", Content: "opening"},
	}
	if err := runtime.InitializeMultiplayerRoom(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ActivateMultiplayerRoom(context.Background(), 41, generation, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	characterA, characterB := uint(101), uint(102)
	game := &multiplayerTurnGameStub{
		room: model.GameRoom{ID: 41, OwnerID: 7, ScriptID: 9, Status: model.RoomStatusPlaying, TurnTimeoutSeconds: 120},
		players: []model.RoomPlayer{
			{UserID: 7, CharacterID: &characterA, PlayerOrder: 0, Status: model.RoomPlayerStatusActive},
			{UserID: 8, CharacterID: &characterB, PlayerOrder: 1, Status: model.RoomPlayerStatusActive},
		},
	}
	ai := &multiplayerTurnAIStub{response: &ai_client.GameActionResponse{Narrative: "门开了", StatusChanges: effectChanges(effectCall("update_player_status", map[string]any{
		"player_id": 8, "field": "hp", "value": 5, "reason": "trap",
	}))}}
	publisher := &multiplayerTurnEventRecorder{}
	service := NewGameService(game, nil, ai, runtime)
	service.ConfigureMultiplayer(publisher)
	service.now = func() time.Time { return now }
	return service, game, runtime, ai, publisher, now
}

func TestMultiplayerTurnActionBroadcastAndReplay(t *testing.T) {
	service, game, runtime, ai, publisher, _ := multiplayerTurnFixture(t)
	ctx := context.Background()
	requestID := uuid.NewString()
	req := &SubmitGameActionRequest{UserID: 7, RoomID: 41, RequestID: requestID, ExpectedTurn: 0, Action: "开门"}
	if _, err := service.SubmitAction(ctx, &SubmitGameActionRequest{
		UserID: 8, RoomID: 41, RequestID: uuid.NewString(), ExpectedTurn: 0, Action: "插话",
	}); !errors.Is(err, ErrMultiplayerNotActor) {
		t.Fatalf("non-actor action error = %v", err)
	}
	result, err := service.SubmitAction(ctx, req)
	if err != nil || result.CurrentTurn != 1 || result.CurrentActorID != 8 || result.MultiplayerEffects == nil || game.turn != 1 {
		t.Fatalf("action result = %#v, game turn=%d, error=%v", result, game.turn, err)
	}
	if ai.calls != 1 || len(ai.request.Participants) != 2 {
		t.Fatalf("AI calls=%d, request=%#v", ai.calls, ai.request)
	}
	snapshot, err := runtime.GetMultiplayerRoom(ctx, 41)
	if err != nil || snapshot.CurrentTurn != 1 || snapshot.Players[1].PlayerState["hp"] != "5" {
		t.Fatalf("committed runtime = %#v, %v", snapshot, err)
	}
	if publisher.count() != 5 {
		t.Fatalf("broadcast count = %d, events = %#v", publisher.count(), publisher.events)
	}
	replayed, err := service.SubmitAction(ctx, req)
	if err != nil || !replayed.Duplicate || ai.calls != 1 || publisher.count() != 5 {
		t.Fatalf("replay=%#v error=%v calls=%d broadcasts=%d", replayed, err, ai.calls, publisher.count())
	}
	changed := *req
	changed.Action = "另一动作"
	if _, err := service.SubmitAction(ctx, &changed); !errors.Is(err, ErrMultiplayerRequestConflict) {
		t.Fatalf("reused request ID error = %v", err)
	}
	request := &SkipMultiplayerTurnRequest{UserID: 8, RoomID: 41, RequestID: uuid.NewString(), ExpectedTurn: 1}
	skipped, err := service.SkipMultiplayerTurn(ctx, request)
	if err != nil || skipped.CurrentTurn != 2 || skipped.CurrentActorID != 7 || game.turn != 2 {
		t.Fatalf("manual skip = %#v, game turn=%d, error=%v", skipped, game.turn, err)
	}
	again, err := service.SkipMultiplayerTurn(ctx, request)
	if err != nil || !again.Duplicate || publisher.count() != 7 {
		t.Fatalf("skip replay=%#v error=%v broadcasts=%d", again, err, publisher.count())
	}
}

func TestMultiplayerTurnAIErrorRestoresDeadline(t *testing.T) {
	service, _, runtime, ai, publisher, _ := multiplayerTurnFixture(t)
	ai.err = errors.New("stream failed")
	_, err := service.SubmitAction(context.Background(), &SubmitGameActionRequest{
		UserID: 7, RoomID: 41, RequestID: uuid.NewString(), ExpectedTurn: 0, Action: "开门",
	})
	if !errors.Is(err, ErrMultiplayerAIUnavailable) {
		t.Fatalf("AI error = %v", err)
	}
	snapshot, err := runtime.GetMultiplayerRoom(context.Background(), 41)
	if err != nil || snapshot.CurrentTurn != 0 || snapshot.DeadlineAt == nil || snapshot.ActionLease != nil {
		t.Fatalf("runtime after failed AI = %#v, %v", snapshot, err)
	}
	if publisher.count() != 3 || publisher.events[2].Type != "action_cancelled" {
		t.Fatalf("failure broadcasts = %#v", publisher.events)
	}
}

func TestMultiplayerDeadlineRecoversInterruptedActionThenSkips(t *testing.T) {
	service, game, runtime, _, publisher, now := multiplayerTurnFixture(t)
	ctx := context.Background()
	snapshot, err := runtime.GetMultiplayerRoom(ctx, 41)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AcquireMultiplayerAction(ctx, 41, 7, snapshot.Generation, 0, uuid.NewString(),
		multiplayerSkipFingerprint(41, 7, snapshot.Generation, 0, "claim"), now); err != nil {
		t.Fatal(err)
	}
	recoveryAt := now.Add(repo.MultiplayerActionRecoveryTimeout)
	tasks, err := runtime.ListDueMultiplayerDeadlines(ctx, recoveryAt, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("recovery tasks = %#v, %v", tasks, err)
	}
	if err := service.ProcessMultiplayerDeadline(ctx, tasks[0], recoveryAt); err != nil {
		t.Fatalf("recover interrupted action: %v", err)
	}
	recovered, err := runtime.GetMultiplayerRoom(ctx, 41)
	if err != nil || recovered.CurrentTurn != 0 || recovered.ActionLease != nil || recovered.DeadlineAt == nil {
		t.Fatalf("recovered runtime = %#v, %v", recovered, err)
	}
	if publisher.count() != 1 || publisher.events[0].Type != "action_cancelled" {
		t.Fatalf("recovery broadcasts = %#v", publisher.events)
	}
	skipAt := recoveryAt.Add(2 * time.Minute)
	tasks, err = runtime.ListDueMultiplayerDeadlines(ctx, skipAt, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("timeout tasks = %#v, %v", tasks, err)
	}
	if err := service.ProcessMultiplayerDeadline(ctx, tasks[0], skipAt); err != nil {
		t.Fatalf("timeout skip: %v", err)
	}
	if err := service.ProcessMultiplayerDeadline(ctx, tasks[0], skipAt); err != nil {
		t.Fatalf("duplicate timeout: %v", err)
	}
	advanced, err := runtime.GetMultiplayerRoom(ctx, 41)
	if err != nil || advanced.CurrentTurn != 1 || game.turn != 1 || publisher.count() != 3 {
		t.Fatalf("advanced runtime=%#v game turn=%d broadcasts=%d error=%v", advanced, game.turn, publisher.count(), err)
	}
}
