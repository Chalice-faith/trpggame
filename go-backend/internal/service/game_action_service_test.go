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
	"trpggame/internal/repo"
)

func TestGameServiceSubmitActionCommitsAuthoritativeEffects(t *testing.T) {
	service, gameRepository, aiClient, runtimeRepository := actionServiceFixture()
	aiClient.actionResponse = &ai_client.GameActionResponse{
		Narrative: "  你找到钥匙，但吸入了毒雾。  ",
		DiceRoll: &ai_client.DiceRollData{
			Type: "D20", Result: 17, Target: 12, Success: true,
			Description: "D20 = 17 (目标 12) — 成功", Reason: "侦查书房",
		},
		StatusChanges: effectChanges(
			effectCall("update_player_status", map[string]any{
				"player_id": 7, "field": "hp", "value": 8, "reason": "毒雾",
			}),
			effectCall("add_item", map[string]any{
				"player_id": 7, "item_name": "钥匙", "quantity": 1, "description": "黄铜",
			}),
			effectCall("add_buff", map[string]any{
				"player_id": 7, "buff_name": "中毒", "duration": 3,
			}),
			effectCall("trigger_event", map[string]any{
				"event_name": "找到钥匙", "description": "玩家在书房找到黄铜钥匙。",
			}),
		),
	}

	result, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())

	if err != nil {
		t.Fatalf("SubmitAction() error = %v", err)
	}
	if result.Narrative != "你找到钥匙，但吸入了毒雾。" || result.CurrentTurn != 1 || result.Duplicate {
		t.Fatalf("result = %#v", result)
	}
	if result.DiceRoll == nil || result.DiceRoll.Result != 17 || !result.DiceRoll.Success {
		t.Fatalf("dice roll = %#v", result.DiceRoll)
	}
	if aiClient.actionRequest == nil || aiClient.actionRequest.RoomID != 41 ||
		aiClient.actionRequest.UserID != 7 || aiClient.actionRequest.ScriptID != 11 ||
		aiClient.actionRequest.CharacterID != 13 || aiClient.actionRequest.Action != "我检查书房" {
		t.Fatalf("AI request = %#v", aiClient.actionRequest)
	}
	if runtimeRepository.findCalls != 1 || runtimeRepository.beginCalls != 1 || runtimeRepository.committed == nil {
		t.Fatalf("runtime calls = find:%d begin:%d commit:%#v", runtimeRepository.findCalls, runtimeRepository.beginCalls, runtimeRepository.committed)
	}
	mutation := runtimeRepository.committed
	if mutation.RoomID != 41 || mutation.UserID != 7 ||
		mutation.Generation != "11111111-1111-4111-8111-111111111111" || mutation.ExpectedTurn != 0 ||
		mutation.PlayerStateChanges["hp"] != "8" || len(mutation.ItemMutations) != 1 ||
		mutation.ItemMutations[0].Name != "钥匙" || len(mutation.BuffMutations) != 1 ||
		len(mutation.RequestFingerprint) != 64 || len(mutation.Messages) != 2 {
		t.Fatalf("runtime mutation = %#v", mutation)
	}
	if len(result.Effects.Events) != 1 || result.Effects.Events[0].Name != "找到钥匙" {
		t.Fatalf("normalized events = %#v", result.Effects.Events)
	}
	if gameRepository.room.ID != 41 {
		t.Fatalf("room = %#v", gameRepository.room)
	}
	if len(gameRepository.progressCalls) != 2 || gameRepository.progressCalls[0].turn != 0 ||
		gameRepository.progressCalls[1].turn != 1 {
		t.Fatalf("progress calls = %#v, want turns [0 1]", gameRepository.progressCalls)
	}
}

func TestGameServiceSubmitActionStreamEmitsChunksAndCommittedMetadata(t *testing.T) {
	service, _, aiClient, _ := actionServiceFixture()
	aiClient.streamEvents = []ai_client.ActionStreamEvent{
		{Type: "narrative_chunk", Content: "你发现"},
		{Type: "narrative_chunk", Content: "一把钥匙。"},
		{Type: "complete", Narrative: "你发现一把钥匙。"},
	}
	aiClient.streamResponse = &ai_client.GameActionResponse{
		Narrative: "你发现一把钥匙。",
		StatusChanges: effectChanges(effectCall("update_player_status", map[string]any{
			"player_id": 7, "field": "san", "value": 48, "reason": "紧张",
		})),
	}

	var events []GameActionStreamEvent
	result, err := service.SubmitActionStream(
		context.Background(),
		validSubmitGameActionRequest(),
		func(event GameActionStreamEvent) { events = append(events, event) },
	)

	if err != nil {
		t.Fatalf("SubmitActionStream() error = %v", err)
	}
	if result == nil || result.CurrentTurn != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(events) != 4 || events[0].Type != "narrative_chunk" ||
		events[1].Type != "narrative_chunk" || events[2].Type != "status_update" ||
		events[3].Type != "narrative_complete" {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Content != "你发现" || events[3].Result == nil ||
		events[3].Result.Narrative != "你发现一把钥匙。" {
		t.Fatalf("event payloads = %#v", events)
	}
}

func TestGameServiceSubmitActionReplaysCachedResultBeforeStatusCheckAndAI(t *testing.T) {
	service, gameRepository, aiClient, runtimeRepository := actionServiceFixture()
	gameRepository.room.Status = model.RoomStatusPaused
	cached := &SubmitGameActionResult{
		Narrative: "原始响应",
		Effects: &ActionEffects{
			PlayerStateChanges: map[string]string{},
			Items:              []ItemMutation{}, Buffs: []BuffMutation{}, Events: []KeyEventMutation{},
		},
		CurrentTurn: 1,
	}
	encoded, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal cached response: %v", err)
	}
	runtimeRepository.findFound = true
	runtimeRepository.findResult = &model.ActionCommitResult{
		Duplicate: true, CurrentTurn: 3, ResponseJSON: encoded,
	}

	result, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())

	if err != nil {
		t.Fatalf("SubmitAction() error = %v", err)
	}
	if !result.Duplicate || result.Narrative != "原始响应" || result.CurrentTurn != 1 {
		t.Fatalf("cached result = %#v", result)
	}
	if aiClient.actionRequest != nil || runtimeRepository.committed != nil {
		t.Fatal("cached request reached AI or commit")
	}
	if len(gameRepository.progressCalls) != 1 || gameRepository.progressCalls[0].turn != 1 {
		t.Fatalf("cached progress calls = %#v, want turn 1", gameRepository.progressCalls)
	}
}

func TestGameServiceSubmitActionCachedRetryRepairsFailedMySQLProgressWithoutAI(t *testing.T) {
	service, gameRepository, aiClient, runtimeRepository := actionServiceFixture()
	aiClient.actionResponse = &ai_client.GameActionResponse{Narrative: "first inference"}
	cached := &SubmitGameActionResult{
		Narrative: "first inference",
		Effects: &ActionEffects{
			PlayerStateChanges: map[string]string{},
			Items:              []ItemMutation{}, Buffs: []BuffMutation{}, Events: []KeyEventMutation{},
		},
		CurrentTurn: 1,
	}
	encoded, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal cached action: %v", err)
	}
	runtimeRepository.commitResult = &model.ActionCommitResult{CurrentTurn: 1, ResponseJSON: encoded}
	gameRepository.progressResults = []gameTransitionResult{
		{updated: true},
		{err: errors.New("mysql unavailable")},
	}

	request := validSubmitGameActionRequest()
	if _, err := service.SubmitAction(context.Background(), request); !errors.Is(err, ErrInternal) {
		t.Fatalf("first SubmitAction() error = %v, want internal retryable error", err)
	}
	if runtimeRepository.committed == nil {
		t.Fatal("action was not committed before MySQL progress failure")
	}

	runtimeRepository.findFound = true
	runtimeRepository.findResult = &model.ActionCommitResult{
		Duplicate: true, CurrentTurn: 1, ResponseJSON: encoded,
	}
	aiClient.actionRequest = nil
	runtimeRepository.committed = nil
	result, err := service.SubmitAction(context.Background(), request)
	if err != nil || result == nil || !result.Duplicate || result.CurrentTurn != 1 {
		t.Fatalf("retry SubmitAction() = (%#v, %v)", result, err)
	}
	if aiClient.actionRequest != nil || runtimeRepository.committed != nil {
		t.Fatal("cached progress repair repeated AI or Redis commit")
	}
	if len(gameRepository.progressCalls) != 3 || gameRepository.progressCalls[2].turn != 1 {
		t.Fatalf("progress calls = %#v, want preflight, failed commit sync, retry sync", gameRepository.progressCalls)
	}
}

func TestGameServiceSubmitActionValidatesRequestBeforeDependencies(t *testing.T) {
	service, _, aiClient, runtimeRepository := actionServiceFixture()
	tests := []*SubmitGameActionRequest{
		nil,
		{RoomID: 41, RequestID: validActionRequestID(), Action: "行动"},
		{UserID: 7, RequestID: validActionRequestID(), Action: "行动"},
		{UserID: 7, RoomID: 41, RequestID: "not-uuid", Action: "行动"},
		{UserID: 7, RoomID: 41, RequestID: validActionRequestID(), ExpectedTurn: -1, Action: "行动"},
		{UserID: 7, RoomID: 41, RequestID: validActionRequestID(), Action: "  "},
		{UserID: 7, RoomID: 41, RequestID: validActionRequestID(), Action: strings.Repeat("行", 2001)},
	}
	for index, request := range tests {
		if _, err := service.SubmitAction(context.Background(), request); !errors.Is(err, ErrInvalidGameAction) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
	if aiClient.actionRequest != nil || runtimeRepository.findCalls != 0 {
		t.Fatal("invalid request reached downstream dependency")
	}
}

func TestGameServiceSubmitActionEnforcesPersistentRoomAndPlayerAccess(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeGameRepository)
		want      error
	}{
		{"room not found", func(repository *fakeGameRepository) {
			repository.room = nil
			repository.roomErr = gorm.ErrRecordNotFound
		}, ErrGameRoomNotFound},
		{"room query fails", func(repository *fakeGameRepository) {
			repository.roomErr = errors.New("MySQL unavailable")
		}, ErrInternal},
		{"player not found", func(repository *fakeGameRepository) {
			repository.player = nil
			repository.playerErr = gorm.ErrRecordNotFound
		}, ErrGamePlayerNotFound},
		{"character missing", func(repository *fakeGameRepository) {
			repository.player.CharacterID = nil
		}, ErrGamePlayerNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, gameRepository, aiClient, runtimeRepository := actionServiceFixture()
			test.configure(gameRepository)
			_, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("SubmitAction() error = %v, want %v", err, test.want)
			}
			if aiClient.actionRequest != nil || runtimeRepository.committed != nil {
				t.Fatal("unauthorized or invalid player reached mutation")
			}
		})
	}
}

func TestGameServiceSubmitActionRejectsNewActionWhenRoomIsNotPlaying(t *testing.T) {
	service, gameRepository, aiClient, _ := actionServiceFixture()
	gameRepository.room.Status = model.RoomStatusPaused
	_, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())
	if !errors.Is(err, ErrGameRoomNotPlaying) {
		t.Fatalf("SubmitAction() error = %v", err)
	}
	if aiClient.actionRequest != nil {
		t.Fatal("paused room reached AI")
	}
}

func TestGameServiceSubmitActionRejectsUnsafeAIResultBeforeCommit(t *testing.T) {
	tests := []struct {
		name     string
		response *ai_client.GameActionResponse
		aiErr    error
		want     error
	}{
		{"AI failure", nil, errors.New("upstream unavailable"), ErrAIUnavailable},
		{"nil response", nil, nil, ErrEmptyActionNarrative},
		{"empty narrative", &ai_client.GameActionResponse{Narrative: "  "}, nil, ErrEmptyActionNarrative},
		{"unsafe effects", &ai_client.GameActionResponse{
			Narrative:     "响应",
			StatusChanges: effectChanges(effectCall("delete_room", map[string]any{})),
		}, nil, ErrInvalidActionEffects},
		{"inconsistent dice", &ai_client.GameActionResponse{
			Narrative: "响应",
			DiceRoll: &ai_client.DiceRollData{
				Type: "D20", Result: 17, Target: 12, Success: false,
				Description: "描述", Reason: "侦查",
			},
		}, nil, ErrInvalidActionEffects},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, aiClient, runtimeRepository := actionServiceFixture()
			aiClient.actionResponse = test.response
			aiClient.actionErr = test.aiErr
			_, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("SubmitAction() error = %v, want %v", err, test.want)
			}
			if runtimeRepository.committed != nil {
				t.Fatal("unsafe AI result reached Redis commit")
			}
		})
	}
}

func TestGameServiceSubmitActionMapsRuntimeFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"turn conflict", repo.ErrGameRuntimeConflict, ErrGameActionConflict},
		{"generation invalidated", repo.ErrGameRuntimeGenerationConflict, ErrGameActionConflict},
		{"runtime paused", repo.ErrGameRuntimeNotPlaying, ErrGameRoomNotPlaying},
		{"request conflict", repo.ErrActionIdempotencyConflict, ErrActionRequestConflict},
		{"insufficient items", repo.ErrInsufficientItemQuantity, ErrInsufficientItems},
		{"runtime unavailable", repo.ErrGameRuntimeUnavailable, ErrGameRuntimeUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, aiClient, runtimeRepository := actionServiceFixture()
			aiClient.actionResponse = &ai_client.GameActionResponse{Narrative: "响应"}
			runtimeRepository.commitErr = test.err
			_, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("SubmitAction() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestGameServiceSubmitActionRejectsPreflightGenerationBoundary(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"turn conflict", repo.ErrGameRuntimeConflict, ErrGameActionConflict},
		{"runtime paused", repo.ErrGameRuntimeNotPlaying, ErrGameRoomNotPlaying},
		{"runtime unavailable", repo.ErrGameRuntimeUnavailable, ErrGameRuntimeUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, aiClient, runtimeRepository := actionServiceFixture()
			runtimeRepository.beginErr = test.err
			_, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("SubmitAction() error = %v, want %v", err, test.want)
			}
			if aiClient.actionRequest != nil || runtimeRepository.committed != nil {
				t.Fatal("failed action preflight reached AI or Redis commit")
			}
		})
	}
}

func TestGameServiceSubmitActionMapsPreflightRequestConflict(t *testing.T) {
	service, _, aiClient, runtimeRepository := actionServiceFixture()
	runtimeRepository.findErr = repo.ErrActionIdempotencyConflict
	_, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())
	if !errors.Is(err, ErrActionRequestConflict) {
		t.Fatalf("SubmitAction() error = %v", err)
	}
	if aiClient.actionRequest != nil {
		t.Fatal("request ID conflict reached AI")
	}
}

func TestNormalizeActionDiceRollValidatesServerResult(t *testing.T) {
	result, err := normalizeActionDiceRoll(&ai_client.DiceRollData{
		Type: "D100", Result: 5, Target: 40, Success: false,
		CriticalMiss: true, Description: "大失败", Reason: "理智检定",
	})
	if err != nil || result == nil || !result.CriticalMiss || result.Success {
		t.Fatalf("normalize dice = (%#v, %v)", result, err)
	}
	if _, err := normalizeActionDiceRoll(&ai_client.DiceRollData{
		Type: "D20", Result: 20, Target: 12, Success: true,
		CriticalHit: false, Description: "错误标记", Reason: "侦查",
	}); err == nil {
		t.Fatal("inconsistent critical result was accepted")
	}
}

func TestDecodeSubmitActionResultRejectsMalformedCache(t *testing.T) {
	for _, encoded := range []json.RawMessage{
		nil,
		json.RawMessage(`{"narrative":"","effects":{},"current_turn":1}`),
		json.RawMessage(`{"narrative":"响应","current_turn":1}`),
		json.RawMessage(`{"narrative":"响应","effects":{},"current_turn":1}`),
		json.RawMessage(`{"narrative":"响应","effects":{},"current_turn":0}`),
		json.RawMessage(`{"narrative":"响应","effects":{},"current_turn":1,"extra":true}`),
	} {
		if _, err := decodeSubmitActionResult(encoded, true); !errors.Is(err, ErrInternal) {
			t.Fatalf("cache %s error = %v", encoded, err)
		}
	}
}

func TestGameServiceSubmitActionUsesWinnerResponseOnConcurrentDuplicate(t *testing.T) {
	service, _, aiClient, runtimeRepository := actionServiceFixture()
	aiClient.actionResponse = &ai_client.GameActionResponse{Narrative: "本次生成但未胜出"}
	winner := &SubmitGameActionResult{
		Narrative: "并发请求的原始结果",
		Effects: &ActionEffects{
			PlayerStateChanges: map[string]string{},
			Items:              []ItemMutation{}, Buffs: []BuffMutation{}, Events: []KeyEventMutation{},
		},
		CurrentTurn: 1,
	}
	encoded, err := json.Marshal(winner)
	if err != nil {
		t.Fatalf("marshal winner: %v", err)
	}
	runtimeRepository.commitResult = &model.ActionCommitResult{
		Duplicate: true, CurrentTurn: 1, ResponseJSON: encoded,
	}

	result, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())

	if err != nil {
		t.Fatalf("SubmitAction() error = %v", err)
	}
	if !result.Duplicate || result.Narrative != "并发请求的原始结果" {
		t.Fatalf("result = %#v", result)
	}
}

func TestGameServiceSubmitActionPersistsAutomaticSaveSnapshot(t *testing.T) {
	service, gameRepository, aiClient, runtimeRepository := actionServiceFixture()
	gameRepository.autoSaveCreated = true
	aiClient.actionResponse = &ai_client.GameActionResponse{Narrative: "第十回合结果"}
	winner := &SubmitGameActionResult{
		Narrative: "第十回合结果",
		Effects: &ActionEffects{
			PlayerStateChanges: map[string]string{},
			Items:              []ItemMutation{}, Buffs: []BuffMutation{}, Events: []KeyEventMutation{},
		},
		CurrentTurn: 10,
	}
	encoded, err := json.Marshal(winner)
	if err != nil {
		t.Fatalf("marshal action result: %v", err)
	}
	runtimeRepository.commitResult = &model.ActionCommitResult{
		CurrentTurn: 10, ResponseJSON: encoded, AutoSaveSnapshot: automaticSaveSnapshot(),
	}
	request := validSubmitGameActionRequest()
	request.ExpectedTurn = 9

	result, err := service.SubmitAction(context.Background(), request)

	if err != nil || result == nil || result.CurrentTurn != 10 {
		t.Fatalf("SubmitAction() = (%#v, %v)", result, err)
	}
	if gameRepository.createdAutoSave == nil || gameRepository.createdAutoSave.RoundNumber != 10 {
		t.Fatalf("automatic save = %#v", gameRepository.createdAutoSave)
	}
}

func TestGameServiceSubmitActionCachedReplayRetriesPendingAutomaticSave(t *testing.T) {
	service, gameRepository, aiClient, runtimeRepository := actionServiceFixture()
	gameRepository.autoSaveCreated = true
	cached := &SubmitGameActionResult{
		Narrative: "第十回合结果",
		Effects: &ActionEffects{
			PlayerStateChanges: map[string]string{},
			Items:              []ItemMutation{}, Buffs: []BuffMutation{}, Events: []KeyEventMutation{},
		},
		CurrentTurn: 10,
	}
	encoded, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal cached action: %v", err)
	}
	runtimeRepository.findFound = true
	runtimeRepository.findResult = &model.ActionCommitResult{
		Duplicate: true, CurrentTurn: 10, ResponseJSON: encoded,
	}
	runtimeRepository.pendingAutoSaves = []model.PendingAutoSave{{
		Generation: "11111111-1111-4111-8111-111111111111",
		Snapshot:   automaticSaveSnapshot(),
	}}

	result, err := service.SubmitAction(context.Background(), validSubmitGameActionRequest())

	if err != nil || result == nil || !result.Duplicate || result.CurrentTurn != 10 {
		t.Fatalf("SubmitAction() = (%#v, %v)", result, err)
	}
	if gameRepository.createdAutoSave == nil || len(runtimeRepository.pendingAutoSaves) != 0 ||
		len(runtimeRepository.acknowledgedAutoSaves) != 1 {
		t.Fatalf("automatic save retry state = repo:%#v runtime:%#v", gameRepository, runtimeRepository)
	}
	if aiClient.actionRequest != nil || runtimeRepository.committed != nil {
		t.Fatal("cached replay reached AI or action commit")
	}
}

func TestGameServiceSubmitActionKeepsCommittedResultRetryableUntilAutoSaveSucceeds(t *testing.T) {
	service, gameRepository, aiClient, runtimeRepository := actionServiceFixture()
	gameRepository.createAutoSaveErr = errors.New("mysql unavailable")
	aiClient.actionResponse = &ai_client.GameActionResponse{Narrative: "第十回合结果"}
	winner := &SubmitGameActionResult{
		Narrative: "第十回合结果",
		Effects: &ActionEffects{
			PlayerStateChanges: map[string]string{},
			Items:              []ItemMutation{}, Buffs: []BuffMutation{}, Events: []KeyEventMutation{},
		},
		CurrentTurn: 10,
	}
	encoded, err := json.Marshal(winner)
	if err != nil {
		t.Fatalf("marshal action result: %v", err)
	}
	runtimeRepository.commitResult = &model.ActionCommitResult{
		CurrentTurn: 10, ResponseJSON: encoded, AutoSaveSnapshot: automaticSaveSnapshot(),
	}
	request := validSubmitGameActionRequest()
	request.ExpectedTurn = 9

	if _, err := service.SubmitAction(context.Background(), request); !errors.Is(err, ErrInternal) {
		t.Fatalf("first SubmitAction() error = %v", err)
	}
	if runtimeRepository.committed == nil || len(runtimeRepository.pendingAutoSaves) != 1 ||
		len(runtimeRepository.acknowledgedAutoSaves) != 0 {
		t.Fatalf("failed automatic save state = %#v", runtimeRepository)
	}

	gameRepository.createAutoSaveErr = nil
	gameRepository.autoSaveCreated = true
	runtimeRepository.findFound = true
	runtimeRepository.findResult = &model.ActionCommitResult{
		Duplicate: true, CurrentTurn: 10, ResponseJSON: encoded,
	}
	aiClient.actionRequest = nil
	runtimeRepository.committed = nil
	result, err := service.SubmitAction(context.Background(), request)
	if err != nil || result == nil || !result.Duplicate || result.CurrentTurn != 10 {
		t.Fatalf("retry SubmitAction() = (%#v, %v)", result, err)
	}
	if len(runtimeRepository.pendingAutoSaves) != 0 || len(runtimeRepository.acknowledgedAutoSaves) != 1 ||
		aiClient.actionRequest != nil || runtimeRepository.committed != nil {
		t.Fatalf("retry state = runtime:%#v AI:%#v", runtimeRepository, aiClient.actionRequest)
	}
}

func actionServiceFixture() (
	*GameService,
	*fakeGameRepository,
	*fakeGameInferenceClient,
	*fakeGameRuntimeRepository,
) {
	characterID := uint(13)
	gameRepository := &fakeGameRepository{
		room: &model.GameRoom{
			ID: 41, OwnerID: 7, ScriptID: 11, Status: model.RoomStatusPlaying,
		},
		player: &model.RoomPlayer{
			ID: 51, RoomID: 41, UserID: 7, CharacterID: &characterID,
		},
	}
	aiClient := &fakeGameInferenceClient{}
	runtimeRepository := &fakeGameRuntimeRepository{}
	service := NewGameService(
		gameRepository,
		readyGameScriptRepository("古宅"),
		aiClient,
		runtimeRepository,
	)
	return service, gameRepository, aiClient, runtimeRepository
}

func validSubmitGameActionRequest() *SubmitGameActionRequest {
	return &SubmitGameActionRequest{
		UserID: 7, RoomID: 41, RequestID: validActionRequestID(),
		ExpectedTurn: 0, Action: "  我检查书房  ",
	}
}

func validActionRequestID() string {
	return "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
}
