package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"trpggame/internal/ai_client"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type multiplayerRoomRepoStub struct {
	candidate    *repo.RoomRecord
	started      *repo.RoomRecord
	startErr     error
	paused       bool
	pauseVersion uint64
	startCalled  func()
}

func (r *multiplayerRoomRepoStub) Create(context.Context, uint, uint, string, string, int, time.Time) (*repo.RoomRecord, error) {
	return nil, nil
}
func (r *multiplayerRoomRepoStub) List(context.Context, uint) ([]model.GameRoom, error) {
	return nil, nil
}
func (r *multiplayerRoomRepoStub) Get(context.Context, uint, uint) (*repo.RoomRecord, error) {
	return r.candidate, nil
}
func (r *multiplayerRoomRepoStub) Join(context.Context, uint, string, time.Time) (*repo.RoomRecord, error) {
	return nil, nil
}
func (r *multiplayerRoomRepoStub) SelectCharacter(context.Context, uint, uint, uint, uint64) (*repo.RoomRecord, error) {
	return nil, nil
}
func (r *multiplayerRoomRepoStub) SetReady(context.Context, uint, uint, bool, uint64) (*repo.RoomRecord, error) {
	return nil, nil
}
func (r *multiplayerRoomRepoStub) Leave(context.Context, uint, uint, uint64, time.Time) (*repo.RoomRecord, error) {
	return nil, nil
}
func (r *multiplayerRoomRepoStub) Remove(context.Context, uint, uint, uint, uint64, time.Time) (*repo.RoomRecord, error) {
	return nil, nil
}
func (r *multiplayerRoomRepoStub) Transfer(context.Context, uint, uint, uint, uint64) (*repo.RoomRecord, error) {
	return nil, nil
}
func (r *multiplayerRoomRepoStub) Start(context.Context, uint, uint, uint64) (*repo.RoomRecord, error) {
	if r.startCalled != nil {
		r.startCalled()
	}
	return r.started, r.startErr
}
func (r *multiplayerRoomRepoStub) PauseStarted(_ context.Context, _, _ uint, expectedVersion uint64) (*repo.RoomRecord, error) {
	r.paused = true
	r.pauseVersion = expectedVersion
	return r.started, nil
}

type multiplayerRuntimeStub struct {
	leaseErr      error
	initializeErr error
	activateErr   error
	deleteErr     error
	state         *model.MultiplayerRuntimeState
	snapshot      *model.MultiplayerRuntimeSnapshot
	initialized   bool
	deleted       bool
	paused        bool
	released      bool
}

func (r *multiplayerRuntimeStub) AcquireMultiplayerStartLease(context.Context, uint, string, time.Duration) error {
	return r.leaseErr
}
func (r *multiplayerRuntimeStub) ReleaseMultiplayerStartLease(context.Context, uint, string) error {
	r.released = true
	return nil
}
func (r *multiplayerRuntimeStub) InitializeMultiplayerRoom(_ context.Context, state *model.MultiplayerRuntimeState) error {
	r.initialized, r.state = true, state
	return r.initializeErr
}
func (r *multiplayerRuntimeStub) ActivateMultiplayerRoom(context.Context, uint, string, time.Time) (*model.MultiplayerRuntimeSnapshot, error) {
	return r.snapshot, r.activateErr
}
func (r *multiplayerRuntimeStub) PauseMultiplayerRoom(context.Context, uint, string) error {
	r.paused = true
	return nil
}
func (r *multiplayerRuntimeStub) DeleteProvisionalMultiplayerRoom(context.Context, *model.MultiplayerRuntimeState) error {
	r.deleted = true
	return r.deleteErr
}
func (r *multiplayerRuntimeStub) GetMultiplayerRoom(context.Context, uint) (*model.MultiplayerRuntimeSnapshot, error) {
	return r.snapshot, nil
}

type multiplayerAIStub struct {
	request  *ai_client.StartGameRequest
	response *ai_client.StartGameResponse
	err      error
}

func (a *multiplayerAIStub) StartGame(_ context.Context, request *ai_client.StartGameRequest) (*ai_client.StartGameResponse, error) {
	a.request = request
	return a.response, a.err
}

type multiplayerPublisherRecorder struct {
	mutations []RoomMutation
	runtimes  []*model.MultiplayerRuntimeSnapshot
}

func (p *multiplayerPublisherRecorder) PublishRoomMutation(mutation RoomMutation) {
	p.mutations = append(p.mutations, mutation)
}
func (p *multiplayerPublisherRecorder) PublishMultiplayerRuntime(snapshot *model.MultiplayerRuntimeSnapshot) {
	p.runtimes = append(p.runtimes, snapshot)
}

type fixedRoomSequence int64

func (s fixedRoomSequence) CurrentRoomSequence(context.Context, uint) (int64, error) {
	return int64(s), nil
}

func multiplayerStartRecords() (*repo.RoomRecord, *repo.RoomRecord) {
	characterA, characterB := uint(101), uint(102)
	members := []repo.RoomMemberRecord{
		{Player: model.RoomPlayer{UserID: 7, CharacterID: &characterA, IsReady: true, PlayerOrder: 0}},
		{Player: model.RoomPlayer{UserID: 8, CharacterID: &characterB, IsReady: true, PlayerOrder: 1}},
	}
	characters := []model.ScriptCharacter{
		{ID: characterA, ScriptID: 9, Attributes: `{"hp":12,"location":"hall"}`},
		{ID: characterB, ScriptID: 9, Attributes: `{"hp":9}`},
	}
	waiting := &repo.RoomRecord{
		Room:    model.GameRoom{ID: 41, ScriptID: 9, OwnerID: 7, Status: model.RoomStatusWaiting, Version: 3, TurnTimeoutSeconds: 120},
		Members: members, Characters: characters,
	}
	playing := &repo.RoomRecord{
		Room:    model.GameRoom{ID: 41, ScriptID: 9, OwnerID: 7, Status: model.RoomStatusPlaying, Version: 4, TurnOrder: json.RawMessage(`[7,8]`), TurnTimeoutSeconds: 120},
		Members: members, Characters: characters, Mutation: "game_started", AffectedUserID: 7,
	}
	return waiting, playing
}

func TestRoomServiceCoordinatesMultiplayerStartAndStateRead(t *testing.T) {
	waiting, playing := multiplayerStartRecords()
	generation := uuid.NewString()
	deadline := time.Date(2026, 9, 22, 8, 2, 0, 0, time.UTC)
	runtimeSnapshot := &model.MultiplayerRuntimeSnapshot{
		Version: 2, RoomID: 41, Status: model.RoomStatusPlaying, Generation: generation,
		TurnOrder: []uint{7, 8}, CurrentActorID: 7, DeadlineAt: &deadline,
		Players:        []model.MultiplayerRuntimePlayer{{UserID: 7, CharacterID: 101}, {UserID: 8, CharacterID: 102}},
		RecentMessages: []model.RuntimeMessage{{Role: "assistant", Content: "opening"}},
	}
	runtime := &multiplayerRuntimeStub{snapshot: runtimeSnapshot}
	rooms := &multiplayerRoomRepoStub{candidate: waiting, started: playing}
	rooms.startCalled = func() {
		if !runtime.initialized {
			t.Fatal("MySQL start ran before provisional runtime initialization")
		}
	}
	ai := &multiplayerAIStub{response: &ai_client.StartGameResponse{Narrative: "  opening  "}}
	publisher := &multiplayerPublisherRecorder{}
	svc := NewRoomService(rooms)
	svc.ConfigureMultiplayer(runtime, ai, fixedRoomSequence(9))
	svc.SetMutationPublisher(publisher)
	svc.now = func() time.Time { return deadline.Add(-120 * time.Second) }

	result, err := svc.Start(context.Background(), 7, 41, 3)
	if err != nil || result.Status != model.RoomStatusPlaying {
		t.Fatalf("Start() = %#v, %v", result, err)
	}
	if runtime.state == nil || runtime.state.Opening.Content != "opening" || len(runtime.state.Players) != 2 ||
		runtime.state.Players[0].PlayerState["hp"] != "12" || !runtime.released {
		t.Fatalf("runtime state = %#v, released=%v", runtime.state, runtime.released)
	}
	if ai.request == nil || len(ai.request.Participants) != 2 || ai.request.Participants[1].UserID != 8 {
		t.Fatalf("AI request = %#v", ai.request)
	}
	if len(publisher.mutations) != 1 || len(publisher.runtimes) != 1 {
		t.Fatalf("published mutations=%d runtimes=%d", len(publisher.mutations), len(publisher.runtimes))
	}

	rooms.candidate = playing
	state, err := svc.GetMultiplayerState(context.Background(), 8, 41)
	if err != nil || state.Seq != 9 || state.Generation != generation {
		t.Fatalf("GetMultiplayerState() = %#v, %v", state, err)
	}
}

func TestRoomServiceDeletesProvisionalRuntimeWhenMySQLStartFails(t *testing.T) {
	waiting, _ := multiplayerStartRecords()
	runtime := &multiplayerRuntimeStub{}
	rooms := &multiplayerRoomRepoStub{candidate: waiting, startErr: repo.ErrRoomVersionConflict}
	svc := NewRoomService(rooms)
	svc.ConfigureMultiplayer(runtime, &multiplayerAIStub{response: &ai_client.StartGameResponse{Narrative: "opening"}}, nil)

	if _, err := svc.Start(context.Background(), 7, 41, 3); !errors.Is(err, repo.ErrRoomVersionConflict) {
		t.Fatalf("Start() error = %v", err)
	}
	if !runtime.initialized || !runtime.deleted || !runtime.released {
		t.Fatalf("runtime compensation initialized=%v deleted=%v released=%v", runtime.initialized, runtime.deleted, runtime.released)
	}
}

func TestRoomServiceDeletesPossiblyWrittenRuntimeWhenInitializeIsUncertain(t *testing.T) {
	waiting, _ := multiplayerStartRecords()
	runtime := &multiplayerRuntimeStub{initializeErr: errors.New("connection reset after write")}
	rooms := &multiplayerRoomRepoStub{candidate: waiting}
	svc := NewRoomService(rooms)
	svc.ConfigureMultiplayer(runtime, &multiplayerAIStub{response: &ai_client.StartGameResponse{Narrative: "opening"}}, nil)

	if _, err := svc.Start(context.Background(), 7, 41, 3); !errors.Is(err, ErrMultiplayerRuntimeUnavailable) {
		t.Fatalf("Start() error = %v", err)
	}
	if !runtime.initialized || !runtime.deleted || !runtime.released {
		t.Fatalf("runtime compensation initialized=%v deleted=%v released=%v", runtime.initialized, runtime.deleted, runtime.released)
	}
}

func TestRoomServiceRejectsConcurrentMultiplayerStartBeforeAI(t *testing.T) {
	waiting, _ := multiplayerStartRecords()
	runtime := &multiplayerRuntimeStub{leaseErr: repo.ErrMultiplayerStartInProgress}
	ai := &multiplayerAIStub{response: &ai_client.StartGameResponse{Narrative: "opening"}}
	svc := NewRoomService(&multiplayerRoomRepoStub{candidate: waiting})
	svc.ConfigureMultiplayer(runtime, ai, nil)

	if _, err := svc.Start(context.Background(), 7, 41, 3); !errors.Is(err, ErrMultiplayerStartInProgress) {
		t.Fatalf("Start() error = %v", err)
	}
	if ai.request != nil || runtime.initialized || runtime.released {
		t.Fatalf("AI request=%#v initialized=%v released=%v", ai.request, runtime.initialized, runtime.released)
	}
}

func TestRoomServiceDoesNotInitializeRuntimeWhenOpeningFails(t *testing.T) {
	waiting, _ := multiplayerStartRecords()
	runtime := &multiplayerRuntimeStub{}
	svc := NewRoomService(&multiplayerRoomRepoStub{candidate: waiting})
	svc.ConfigureMultiplayer(runtime, &multiplayerAIStub{err: errors.New("AI unavailable")}, nil)

	if _, err := svc.Start(context.Background(), 7, 41, 3); !errors.Is(err, ErrMultiplayerAIUnavailable) {
		t.Fatalf("Start() error = %v", err)
	}
	if runtime.initialized || runtime.deleted || !runtime.released {
		t.Fatalf("runtime initialized=%v deleted=%v released=%v", runtime.initialized, runtime.deleted, runtime.released)
	}
}

func TestRoomServiceSafePausesAfterActivationFailure(t *testing.T) {
	waiting, playing := multiplayerStartRecords()
	runtime := &multiplayerRuntimeStub{activateErr: repo.ErrGameRuntimeUnavailable}
	rooms := &multiplayerRoomRepoStub{candidate: waiting, started: playing}
	svc := NewRoomService(rooms)
	svc.ConfigureMultiplayer(runtime, &multiplayerAIStub{response: &ai_client.StartGameResponse{Narrative: "opening"}}, nil)

	if _, err := svc.Start(context.Background(), 7, 41, 3); !errors.Is(err, ErrMultiplayerRuntimeUnavailable) {
		t.Fatalf("Start() error = %v", err)
	}
	if !runtime.paused || !rooms.paused || rooms.pauseVersion != playing.Room.Version || !runtime.released {
		t.Fatalf("safe pause runtime=%v room=%v version=%d released=%v", runtime.paused, rooms.paused, rooms.pauseVersion, runtime.released)
	}
}
