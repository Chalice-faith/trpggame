package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"trpggame/internal/ai_client"
	"trpggame/internal/model"
)

type gameTransitionCall struct {
	contextErr error
	roomID     uint
	ownerID    uint
	from       []model.RoomStatus
	to         model.RoomStatus
}

type gameTransitionResult struct {
	updated bool
	err     error
}

type fakeGameRepository struct {
	createErr         error
	createdRoom       *model.GameRoom
	createdPlayer     *model.RoomPlayer
	transitions       []gameTransitionCall
	transitionResults []gameTransitionResult
	room              *model.GameRoom
	roomErr           error
	player            *model.RoomPlayer
	playerErr         error
	roomQueryID       uint
	roomQueryOwnerID  uint
	createSaveErr     error
	createdSave       *model.GameSave
	assignSaveID      uint
}

func (r *fakeGameRepository) FindRoomByIDAndOwnerID(
	_ context.Context,
	roomID, ownerID uint,
) (*model.GameRoom, error) {
	r.roomQueryID = roomID
	r.roomQueryOwnerID = ownerID
	return r.room, r.roomErr
}

func (r *fakeGameRepository) CreateSave(
	_ context.Context,
	save *model.GameSave,
) error {
	if r.createSaveErr != nil {
		return r.createSaveErr
	}
	if r.assignSaveID != 0 {
		save.ID = r.assignSaveID
	}
	r.createdSave = save
	return nil
}

func (r *fakeGameRepository) FindPlayer(
	_ context.Context,
	_, _ uint,
) (*model.RoomPlayer, error) {
	return r.player, r.playerErr
}

func (r *fakeGameRepository) CreateRoomWithPlayer(
	_ context.Context,
	room *model.GameRoom,
	player *model.RoomPlayer,
) error {
	if r.createErr != nil {
		return r.createErr
	}
	room.ID = 41
	player.ID = 51
	player.RoomID = room.ID
	r.createdRoom = room
	r.createdPlayer = player
	return nil
}

func (r *fakeGameRepository) TransitionRoomStatus(
	ctx context.Context,
	roomID uint,
	ownerID uint,
	from []model.RoomStatus,
	to model.RoomStatus,
) (bool, error) {
	r.transitions = append(r.transitions, gameTransitionCall{
		contextErr: ctx.Err(),
		roomID:     roomID,
		ownerID:    ownerID,
		from:       append([]model.RoomStatus(nil), from...),
		to:         to,
	})
	if len(r.transitionResults) == 0 {
		return true, nil
	}
	result := r.transitionResults[0]
	r.transitionResults = r.transitionResults[1:]
	return result.updated, result.err
}

type fakeGameScriptRepository struct {
	script         *model.Script
	scriptErr      error
	characters     []model.ScriptCharacter
	charactersErr  error
	scriptID       uint
	userID         uint
	characterQuery uint
}

func (r *fakeGameScriptRepository) FindByIDAndUserID(
	id uint,
	userID uint,
) (*model.Script, error) {
	r.scriptID = id
	r.userID = userID
	return r.script, r.scriptErr
}

func (r *fakeGameScriptRepository) FindCharactersByScriptID(
	scriptID uint,
) ([]model.ScriptCharacter, error) {
	r.characterQuery = scriptID
	return r.characters, r.charactersErr
}

type fakeGameInferenceClient struct {
	response       *ai_client.StartGameResponse
	err            error
	request        *ai_client.StartGameRequest
	onStart        func()
	actionResponse *ai_client.GameActionResponse
	actionErr      error
	actionRequest  *ai_client.GameActionRequest
}

func (c *fakeGameInferenceClient) SubmitAction(
	_ context.Context,
	req *ai_client.GameActionRequest,
) (*ai_client.GameActionResponse, error) {
	c.actionRequest = req
	return c.actionResponse, c.actionErr
}

func (c *fakeGameInferenceClient) StartGame(
	_ context.Context,
	req *ai_client.StartGameRequest,
) (*ai_client.StartGameResponse, error) {
	c.request = req
	if c.onStart != nil {
		c.onStart()
	}
	return c.response, c.err
}

type runtimeDeleteCall struct {
	contextErr error
	roomID     uint
	userID     uint
}

type fakeGameRuntimeRepository struct {
	initializeErr        error
	initializeContextErr error
	deleteErr            error
	initialized          *model.SoloRuntimeState
	deleteCalls          []runtimeDeleteCall
	findResult           *model.ActionCommitResult
	findFound            bool
	findErr              error
	findCalls            int
	commitResult         *model.ActionCommitResult
	commitErr            error
	committed            *model.ActionRuntimeMutation
	captureResult        *model.SoloRuntimeSnapshot
	captureErr           error
	captureRoomID        uint
	captureUserID        uint
}

func (r *fakeGameRuntimeRepository) CaptureSoloRoom(
	_ context.Context,
	roomID uint,
	userID uint,
) (*model.SoloRuntimeSnapshot, error) {
	r.captureRoomID = roomID
	r.captureUserID = userID
	return r.captureResult, r.captureErr
}

func (r *fakeGameRuntimeRepository) FindActionResult(
	_ context.Context,
	_ uint,
	_ string,
	_ string,
) (*model.ActionCommitResult, bool, error) {
	r.findCalls++
	return r.findResult, r.findFound, r.findErr
}

func (r *fakeGameRuntimeRepository) CommitAction(
	_ context.Context,
	mutation *model.ActionRuntimeMutation,
) (*model.ActionCommitResult, error) {
	r.committed = mutation
	if r.commitResult == nil && r.commitErr == nil {
		return &model.ActionCommitResult{
			CurrentTurn:  mutation.ExpectedTurn + 1,
			ResponseJSON: append(json.RawMessage(nil), mutation.ResponseJSON...),
		}, nil
	}
	return r.commitResult, r.commitErr
}

func (r *fakeGameRuntimeRepository) InitializeSoloRoom(
	ctx context.Context,
	state *model.SoloRuntimeState,
) error {
	r.initializeContextErr = ctx.Err()
	if state != nil {
		copyState := *state
		copyState.PlayerState = make(map[string]string, len(state.PlayerState))
		for field, value := range state.PlayerState {
			copyState.PlayerState[field] = value
		}
		r.initialized = &copyState
	}
	return r.initializeErr
}

func (r *fakeGameRuntimeRepository) DeleteSoloRoom(
	ctx context.Context,
	roomID uint,
	userID uint,
) error {
	r.deleteCalls = append(r.deleteCalls, runtimeDeleteCall{
		contextErr: ctx.Err(),
		roomID:     roomID,
		userID:     userID,
	})
	return r.deleteErr
}

func TestGameServiceStartSoloGame(t *testing.T) {
	gameRepository := &fakeGameRepository{}
	scriptRepository := readyGameScriptRepository("  古宅惊魂  ")
	aiClient := &fakeGameInferenceClient{
		response: &ai_client.StartGameResponse{Narrative: "  你站在古宅门前。  "},
	}
	runtimeRepository := &fakeGameRuntimeRepository{}
	service := NewGameService(gameRepository, scriptRepository, aiClient, runtimeRepository)

	result, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

	if err != nil {
		t.Fatalf("StartSoloGame() error = %v", err)
	}
	if scriptRepository.scriptID != 11 || scriptRepository.userID != 7 || scriptRepository.characterQuery != 11 {
		t.Fatalf(
			"script queries = (script=%d, user=%d, characters=%d)",
			scriptRepository.scriptID,
			scriptRepository.userID,
			scriptRepository.characterQuery,
		)
	}
	if result.Room.ID != 41 || result.Room.Name != "古宅惊魂" || result.Room.Status != model.RoomStatusPlaying {
		t.Fatalf("room = %#v", result.Room)
	}
	if result.Player.RoomID != 41 || result.Player.UserID != 7 ||
		result.Player.CharacterID == nil || *result.Player.CharacterID != 13 ||
		!result.Player.IsReady {
		t.Fatalf("player = %#v", result.Player)
	}
	var turnOrder []uint
	if err := json.Unmarshal(result.Room.TurnOrder, &turnOrder); err != nil ||
		len(turnOrder) != 1 || turnOrder[0] != 7 {
		t.Fatalf("turn order = %s, error = %v", result.Room.TurnOrder, err)
	}
	if result.OpeningNarrative != "你站在古宅门前。" {
		t.Fatalf("opening narrative = %q", result.OpeningNarrative)
	}
	if aiClient.request == nil || aiClient.request.RoomID != 41 ||
		aiClient.request.ScriptID != 11 || aiClient.request.CharacterID != 13 ||
		aiClient.request.UserID != 7 {
		t.Fatalf("AI request = %#v", aiClient.request)
	}
	if runtimeRepository.initialized == nil || runtimeRepository.initialized.RoomID != 41 ||
		runtimeRepository.initialized.UserID != 7 ||
		runtimeRepository.initialized.Status != model.RoomStatusPlaying ||
		runtimeRepository.initialized.Opening.Role != "assistant" ||
		runtimeRepository.initialized.Opening.Content != "你站在古宅门前。" ||
		runtimeRepository.initialized.PlayerState["character_id"] != "13" ||
		runtimeRepository.initialized.PlayerState["hp"] != "10" ||
		runtimeRepository.initialized.PlayerState["location"] != "" {
		t.Fatalf("initialized runtime = %#v", runtimeRepository.initialized)
	}
	assertTransition(t, gameRepository.transitions, 0, model.RoomStatusWaiting, model.RoomStatusPlaying)
}

func TestGameServiceStartSoloGameRejectsInvalidRequest(t *testing.T) {
	gameRepository := &fakeGameRepository{}
	scriptRepository := &fakeGameScriptRepository{}
	aiClient := &fakeGameInferenceClient{}
	service := NewGameService(gameRepository, scriptRepository, aiClient, &fakeGameRuntimeRepository{})

	for _, request := range []*StartSoloGameRequest{
		nil,
		{ScriptID: 11, CharacterID: 13},
		{UserID: 7, CharacterID: 13},
		{UserID: 7, ScriptID: 11},
	} {
		if _, err := service.StartSoloGame(context.Background(), request); !errors.Is(err, ErrInvalidGameRequest) {
			t.Fatalf("request %#v error = %v, want ErrInvalidGameRequest", request, err)
		}
	}
	if scriptRepository.scriptID != 0 || gameRepository.createdRoom != nil || aiClient.request != nil {
		t.Fatal("invalid request reached a dependency")
	}
}

func TestGameServiceStartSoloGameValidatesOwnedReadyScript(t *testing.T) {
	tests := []struct {
		name       string
		repository *fakeGameScriptRepository
		want       error
	}{
		{
			name:       "not found or unauthorized",
			repository: &fakeGameScriptRepository{scriptErr: gorm.ErrRecordNotFound},
			want:       ErrScriptNotFound,
		},
		{
			name: "not ready",
			repository: &fakeGameScriptRepository{script: &model.Script{
				ID: 11, UserID: 7, Status: model.ScriptStatusParsing,
			}},
			want: ErrScriptNotReady,
		},
		{
			name: "character belongs to another script",
			repository: &fakeGameScriptRepository{
				script:     &model.Script{ID: 11, UserID: 7, Status: model.ScriptStatusReady},
				characters: []model.ScriptCharacter{{ID: 99, ScriptID: 11}},
			},
			want: ErrCharacterNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gameRepository := &fakeGameRepository{}
			aiClient := &fakeGameInferenceClient{}
			service := NewGameService(gameRepository, test.repository, aiClient, &fakeGameRuntimeRepository{})

			_, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

			if !errors.Is(err, test.want) {
				t.Fatalf("StartSoloGame() error = %v, want %v", err, test.want)
			}
			if gameRepository.createdRoom != nil || aiClient.request != nil {
				t.Fatal("invalid script or character caused side effects")
			}
		})
	}
}

func TestGameServiceStartSoloGameStopsWhenRoomTransactionFails(t *testing.T) {
	gameRepository := &fakeGameRepository{createErr: errors.New("mysql unavailable")}
	aiClient := &fakeGameInferenceClient{}
	service := NewGameService(gameRepository, readyGameScriptRepository("古宅"), aiClient, &fakeGameRuntimeRepository{})

	_, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("StartSoloGame() error = %v, want ErrInternal", err)
	}
	if aiClient.request != nil || len(gameRepository.transitions) != 0 {
		t.Fatal("room transaction failure reached later side effects")
	}
}

func TestGameServiceStartSoloGameRejectsMalformedCharacterStateBeforeSideEffects(t *testing.T) {
	gameRepository := &fakeGameRepository{}
	scriptRepository := readyGameScriptRepository("古宅")
	scriptRepository.characters[0].Attributes = `{"hp":`
	aiClient := &fakeGameInferenceClient{}
	runtimeRepository := &fakeGameRuntimeRepository{}
	service := NewGameService(gameRepository, scriptRepository, aiClient, runtimeRepository)

	_, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("StartSoloGame() error = %v, want ErrInternal", err)
	}
	if gameRepository.createdRoom != nil || aiClient.request != nil || runtimeRepository.initialized != nil {
		t.Fatal("malformed character state caused side effects")
	}
}

func TestGameServiceStartSoloGameEndsWaitingRoomWhenAIFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gameRepository := &fakeGameRepository{}
	aiClient := &fakeGameInferenceClient{
		err:     errors.New("AI unavailable"),
		onStart: cancel,
	}
	service := NewGameService(gameRepository, readyGameScriptRepository("古宅"), aiClient, &fakeGameRuntimeRepository{})

	_, err := service.StartSoloGame(ctx, validStartSoloRequest())

	if !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("StartSoloGame() error = %v, want ErrAIUnavailable", err)
	}
	assertTransition(t, gameRepository.transitions, 0, model.RoomStatusWaiting, model.RoomStatusEnded)
	if gameRepository.transitions[0].contextErr != nil {
		t.Fatalf("cleanup inherited canceled request context: %v", gameRepository.transitions[0].contextErr)
	}
	if gameRepository.createdRoom.Status != model.RoomStatusEnded {
		t.Fatalf("compensated room status = %q", gameRepository.createdRoom.Status)
	}
}

func TestGameServiceStartSoloGameRejectsEmptyNarrativeAndCompensates(t *testing.T) {
	gameRepository := &fakeGameRepository{}
	service := NewGameService(
		gameRepository,
		readyGameScriptRepository("古宅"),
		&fakeGameInferenceClient{response: &ai_client.StartGameResponse{Narrative: "   "}},
		&fakeGameRuntimeRepository{},
	)

	_, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

	if !errors.Is(err, ErrEmptyOpeningNarrative) {
		t.Fatalf("StartSoloGame() error = %v, want ErrEmptyOpeningNarrative", err)
	}
	assertTransition(t, gameRepository.transitions, 0, model.RoomStatusWaiting, model.RoomStatusEnded)
}

func TestGameServiceStartSoloGameCompensatesUncertainRuntimeInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	gameRepository := &fakeGameRepository{}
	aiClient := &fakeGameInferenceClient{
		response: &ai_client.StartGameResponse{Narrative: "开场"},
		onStart:  cancel,
	}
	runtimeRepository := &fakeGameRuntimeRepository{initializeErr: errors.New("Redis response lost")}
	service := NewGameService(
		gameRepository,
		readyGameScriptRepository("古宅"),
		aiClient,
		runtimeRepository,
	)

	_, err := service.StartSoloGame(ctx, validStartSoloRequest())

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("StartSoloGame() error = %v, want ErrInternal", err)
	}
	if runtimeRepository.initializeContextErr == nil {
		t.Fatal("runtime initialization did not observe canceled request context")
	}
	if len(runtimeRepository.deleteCalls) != 1 || runtimeRepository.deleteCalls[0].contextErr != nil {
		t.Fatalf("runtime cleanup calls = %#v", runtimeRepository.deleteCalls)
	}
	assertTransition(t, gameRepository.transitions, 0, model.RoomStatusWaiting, model.RoomStatusEnded)
}

func TestGameServiceStartSoloGameEndsRoomWhenRuntimeCleanupFails(t *testing.T) {
	gameRepository := &fakeGameRepository{transitionResults: []gameTransitionResult{
		{updated: false},
		{updated: true},
	}}
	runtimeRepository := &fakeGameRuntimeRepository{deleteErr: errors.New("Redis unavailable")}
	service := NewGameService(
		gameRepository,
		readyGameScriptRepository("古宅"),
		&fakeGameInferenceClient{response: &ai_client.StartGameResponse{Narrative: "开场"}},
		runtimeRepository,
	)

	_, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("StartSoloGame() error = %v, want ErrInternal", err)
	}
	assertTransitionSources(
		t,
		gameRepository.transitions,
		1,
		[]model.RoomStatus{model.RoomStatusWaiting, model.RoomStatusPlaying},
		model.RoomStatusEnded,
	)
}

func TestGameServiceStartSoloGameCompensatesActivationConflict(t *testing.T) {
	gameRepository := &fakeGameRepository{transitionResults: []gameTransitionResult{
		{updated: false},
		{updated: true},
	}}
	runtimeRepository := &fakeGameRuntimeRepository{}
	service := NewGameService(
		gameRepository,
		readyGameScriptRepository("古宅"),
		&fakeGameInferenceClient{response: &ai_client.StartGameResponse{Narrative: "开场"}},
		runtimeRepository,
	)

	_, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

	if !errors.Is(err, ErrGameStartConflict) {
		t.Fatalf("StartSoloGame() error = %v, want ErrGameStartConflict", err)
	}
	assertTransition(t, gameRepository.transitions, 0, model.RoomStatusWaiting, model.RoomStatusPlaying)
	assertTransitionSources(
		t,
		gameRepository.transitions,
		1,
		[]model.RoomStatus{model.RoomStatusWaiting, model.RoomStatusPlaying},
		model.RoomStatusEnded,
	)
	if len(runtimeRepository.deleteCalls) != 1 ||
		runtimeRepository.deleteCalls[0].roomID != 41 ||
		runtimeRepository.deleteCalls[0].userID != 7 {
		t.Fatalf("runtime delete calls = %#v", runtimeRepository.deleteCalls)
	}
}

func TestGameServiceStartSoloGameCompensatesAmbiguousActivationError(t *testing.T) {
	gameRepository := &fakeGameRepository{transitionResults: []gameTransitionResult{
		{updated: false, err: errors.New("MySQL response lost")},
		{updated: true},
	}}
	runtimeRepository := &fakeGameRuntimeRepository{}
	service := NewGameService(
		gameRepository,
		readyGameScriptRepository("古宅"),
		&fakeGameInferenceClient{response: &ai_client.StartGameResponse{Narrative: "开场"}},
		runtimeRepository,
	)

	_, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("StartSoloGame() error = %v, want ErrInternal", err)
	}
	if len(runtimeRepository.deleteCalls) != 1 {
		t.Fatalf("runtime delete calls = %#v", runtimeRepository.deleteCalls)
	}
	assertTransitionSources(
		t,
		gameRepository.transitions,
		1,
		[]model.RoomStatus{model.RoomStatusWaiting, model.RoomStatusPlaying},
		model.RoomStatusEnded,
	)
}

func TestGameServiceStartSoloGameReportsCompensationFailure(t *testing.T) {
	gameRepository := &fakeGameRepository{transitionResults: []gameTransitionResult{
		{updated: false, err: errors.New("mysql unavailable")},
	}}
	service := NewGameService(
		gameRepository,
		readyGameScriptRepository("古宅"),
		&fakeGameInferenceClient{err: errors.New("AI unavailable")},
		&fakeGameRuntimeRepository{},
	)

	_, err := service.StartSoloGame(context.Background(), validStartSoloRequest())

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("StartSoloGame() error = %v, want ErrInternal", err)
	}
}

func TestSoloRoomNameUsesFallbackAndRuneLimit(t *testing.T) {
	if got := soloRoomName("   "); got != "单人游戏" {
		t.Fatalf("empty room name = %q", got)
	}
	if got := soloRoomName(strings.Repeat("剧", 130)); len([]rune(got)) != 128 {
		t.Fatalf("room name rune count = %d, want 128", len([]rune(got)))
	}
}

func readyGameScriptRepository(title string) *fakeGameScriptRepository {
	return &fakeGameScriptRepository{
		script: &model.Script{
			ID:     11,
			UserID: 7,
			Title:  title,
			Status: model.ScriptStatusReady,
		},
		characters: []model.ScriptCharacter{{
			ID:         13,
			ScriptID:   11,
			Attributes: `{"hp":10,"location":"","character_id":999}`,
		}},
	}
}

func validStartSoloRequest() *StartSoloGameRequest {
	return &StartSoloGameRequest{UserID: 7, ScriptID: 11, CharacterID: 13}
}

func assertTransition(
	t *testing.T,
	transitions []gameTransitionCall,
	index int,
	from model.RoomStatus,
	to model.RoomStatus,
) {
	t.Helper()
	if len(transitions) <= index {
		t.Fatalf("missing transition %d: %#v", index, transitions)
	}
	transition := transitions[index]
	if transition.roomID != 41 || transition.ownerID != 7 ||
		len(transition.from) != 1 || transition.from[0] != from || transition.to != to {
		t.Fatalf("transition %d = %#v", index, transition)
	}
}

func assertTransitionSources(
	t *testing.T,
	transitions []gameTransitionCall,
	index int,
	from []model.RoomStatus,
	to model.RoomStatus,
) {
	t.Helper()
	if len(transitions) <= index {
		t.Fatalf("missing transition %d: %#v", index, transitions)
	}
	transition := transitions[index]
	if transition.roomID != 41 || transition.ownerID != 7 ||
		len(transition.from) != len(from) || transition.to != to {
		t.Fatalf("transition %d = %#v", index, transition)
	}
	for sourceIndex := range from {
		if transition.from[sourceIndex] != from[sourceIndex] {
			t.Fatalf("transition %d = %#v", index, transition)
		}
	}
}
