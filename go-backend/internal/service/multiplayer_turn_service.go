package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"trpggame/internal/ai_client"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type MultiplayerGameRepository interface {
	FindRoomByID(context.Context, uint) (*model.GameRoom, error)
	FindPlayersByRoom(context.Context, uint) ([]model.RoomPlayer, error)
	AdvanceMultiplayerRoomProgress(context.Context, uint, int, int) (bool, error)
}

type MultiplayerTurnRepository interface {
	FindActionResult(context.Context, uint, string, string) (*model.ActionCommitResult, bool, error)
	GetMultiplayerRoom(context.Context, uint) (*model.MultiplayerRuntimeSnapshot, error)
	AcquireMultiplayerAction(context.Context, uint, uint, string, int, string, string, time.Time) (*model.MultiplayerActionAcquireResult, error)
	ReleaseMultiplayerAction(context.Context, *model.MultiplayerActionMutation, time.Time) error
	CommitMultiplayerAction(context.Context, *model.MultiplayerActionMutation) (*model.MultiplayerActionCommitResult, error)
	SkipMultiplayerTurn(context.Context, *model.MultiplayerSkipRequest) (*model.MultiplayerSkipResult, error)
	ListDueMultiplayerDeadlines(context.Context, time.Time, int) ([]model.MultiplayerDeadlineTask, error)
	DiscardMultiplayerDeadline(context.Context, model.MultiplayerDeadlineTask) error
}

type MultiplayerActionPublisher interface {
	PublishMultiplayerActionEvent(roomID uint, requestID string, event GameActionStreamEvent)
}

type SkipMultiplayerTurnRequest struct {
	UserID       uint
	RoomID       uint
	RequestID    string
	ExpectedTurn int
}

func (s *GameService) submitMultiplayerAction(
	ctx context.Context,
	req *SubmitGameActionRequest,
	room *model.GameRoom,
	action, requestID, fingerprint string,
	_ ActionStreamObserver,
) (*SubmitGameActionResult, error) {
	gameRepo, runtime, err := s.multiplayerDependencies()
	if err != nil {
		return nil, err
	}
	if room == nil || room.IsSolo {
		return nil, ErrGameRoomNotFound
	}
	player, err := s.gameRepo.FindPlayer(ctx, room.ID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find multiplayer player: %v", ErrInternal, err)
	}
	if player == nil || player.Status != model.RoomPlayerStatusActive || player.CharacterID == nil {
		return nil, ErrGameRoomNotFound
	}
	cached, found, err := runtime.FindActionResult(ctx, room.ID, requestID, fingerprint)
	if err != nil {
		return nil, mapMultiplayerRuntimeError(err)
	}
	if found {
		result, decodeErr := decodeMultiplayerActionResult(cached.ResponseJSON, true)
		if decodeErr != nil {
			return nil, decodeErr
		}
		current, readErr := runtime.GetMultiplayerRoom(ctx, room.ID)
		if readErr != nil {
			return nil, mapMultiplayerRuntimeError(readErr)
		}
		if result.Generation != current.Generation {
			return nil, ErrMultiplayerTurnConflict
		}
		if err := advanceMultiplayerProgress(ctx, gameRepo, room.ID, result.CurrentTurn, result.RoundNumber); err != nil {
			return nil, err
		}
		s.flushAutoSaveBestEffort(ctx, room.ID)
		return result, nil
	}
	if room.Status != model.RoomStatusPlaying {
		return nil, ErrGameRoomNotPlaying
	}
	snapshot, err := runtime.GetMultiplayerRoom(ctx, room.ID)
	if err != nil {
		return nil, mapMultiplayerRuntimeError(err)
	}
	players, participants, err := multiplayerParticipants(ctx, gameRepo, room, snapshot)
	if err != nil {
		return nil, err
	}
	if snapshot.CurrentTurn != req.ExpectedTurn {
		return nil, ErrMultiplayerTurnConflict
	}
	acquired, err := runtime.AcquireMultiplayerAction(
		ctx, room.ID, req.UserID, snapshot.Generation, req.ExpectedTurn, requestID, fingerprint, s.now().UTC(),
	)
	if err != nil {
		return nil, mapMultiplayerRuntimeError(err)
	}
	if acquired.Duplicate {
		result, decodeErr := decodeMultiplayerActionResult(acquired.ResponseJSON, true)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if err := advanceMultiplayerProgress(ctx, gameRepo, room.ID, result.CurrentTurn, result.RoundNumber); err != nil {
			return nil, err
		}
		s.flushAutoSaveBestEffort(ctx, room.ID)
		return result, nil
	}
	generation := acquired.Generation
	s.publishMultiplayer(room.ID, requestID, GameActionStreamEvent{
		Type: "action_started", Multiplayer: true, Generation: generation,
		CurrentTurn: req.ExpectedTurn, PlayerID: req.UserID,
	})

	actorCharacterID := *player.CharacterID
	aiRequest := &ai_client.GameActionRequest{
		RoomID: room.ID, UserID: req.UserID, Action: action, ScriptID: room.ScriptID,
		CharacterID: actorCharacterID, Participants: participants,
	}
	var aiResult *ai_client.GameActionResponse
	streamed := false
	if streamClient, ok := s.aiClient.(GameInferenceStreamClient); ok {
		aiResult, err = streamClient.SubmitActionStream(ctx, aiRequest, func(event ai_client.ActionStreamEvent) error {
			current, readErr := runtime.GetMultiplayerRoom(ctx, room.ID)
			if readErr != nil {
				return readErr
			}
			if current.Status != model.RoomStatusPlaying || current.Generation != generation ||
				current.CurrentTurn != req.ExpectedTurn || current.ActionLease == nil ||
				current.ActionLease.RequestID != requestID {
				return repo.ErrGameRuntimeGenerationConflict
			}
			if event.Type == "narrative_chunk" && event.Content != "" {
				streamed = true
				s.publishMultiplayer(room.ID, requestID, GameActionStreamEvent{
					Type: "narrative_chunk", Content: event.Content, Multiplayer: true,
					Generation: generation, CurrentTurn: req.ExpectedTurn, PlayerID: req.UserID,
				})
			}
			return nil
		})
	} else {
		aiResult, err = s.aiClient.SubmitAction(ctx, aiRequest)
	}
	if err != nil {
		if errors.Is(err, repo.ErrGameRuntimeGenerationConflict) {
			s.cancelMultiplayerAction(room, req, generation, requestID, fingerprint, "generation_invalidated")
			return nil, ErrMultiplayerTurnConflict
		}
		s.cancelMultiplayerAction(room, req, generation, requestID, fingerprint, "ai_unavailable")
		if errors.Is(err, repo.ErrGameRuntimeUnavailable) {
			return nil, ErrMultiplayerRuntimeUnavailable
		}
		return nil, fmt.Errorf("%w: infer multiplayer action: %v", ErrMultiplayerAIUnavailable, err)
	}
	if aiResult == nil || strings.TrimSpace(aiResult.Narrative) == "" {
		s.cancelMultiplayerAction(room, req, generation, requestID, fingerprint, "empty_narrative")
		return nil, ErrMultiplayerAIUnavailable
	}
	if !streamed {
		s.publishMultiplayer(room.ID, requestID, GameActionStreamEvent{
			Type: "narrative_chunk", Content: strings.TrimSpace(aiResult.Narrative), Multiplayer: true,
			Generation: generation, CurrentTurn: req.ExpectedTurn, PlayerID: req.UserID,
		})
	}
	roster := make([]uint, len(players))
	for index := range players {
		roster[index] = players[index].UserID
	}
	effects, err := InterpretMultiplayerActionEffects(roster, aiResult.StatusChanges)
	if err != nil {
		s.cancelMultiplayerAction(room, req, generation, requestID, fingerprint, "invalid_effects")
		return nil, err
	}
	diceRoll, err := normalizeActionDiceRoll(aiResult.DiceRoll)
	if err != nil {
		s.cancelMultiplayerAction(room, req, generation, requestID, fingerprint, "invalid_dice")
		return nil, fmt.Errorf("%w: %v", ErrInvalidActionEffects, err)
	}
	nextTurn := req.ExpectedTurn + 1
	nextDeadline := s.now().UTC().Add(time.Duration(room.TurnTimeoutSeconds) * time.Second)
	result := &SubmitGameActionResult{
		Narrative: strings.TrimSpace(aiResult.Narrative), DiceRoll: diceRoll,
		MultiplayerEffects: effects, Generation: generation, CurrentTurn: nextTurn,
		RoundNumber: nextTurn / len(snapshot.TurnOrder), CurrentActorID: snapshot.TurnOrder[nextTurn%len(snapshot.TurnOrder)],
		DeadlineAt: &nextDeadline,
	}
	responseJSON, err := json.Marshal(result)
	if err != nil {
		s.cancelMultiplayerAction(room, req, generation, requestID, fingerprint, "encode_failed")
		return nil, fmt.Errorf("%w: encode multiplayer action result: %v", ErrInternal, err)
	}
	mutation := &model.MultiplayerActionMutation{
		RoomID: room.ID, UserID: req.UserID, Generation: generation, ExpectedTurn: req.ExpectedTurn,
		RequestID: requestID, RequestFingerprint: fingerprint, ResponseJSON: responseJSON, NextDeadline: nextDeadline,
		Messages:        []model.RuntimeMessage{{Role: "user", Content: action}, {Role: "assistant", Content: result.Narrative}},
		PlayerMutations: multiplayerRuntimeMutations(effects),
	}
	commit, err := runtime.CommitMultiplayerAction(ctx, mutation)
	if err != nil {
		if errors.Is(err, repo.ErrGameRuntimeUnavailable) {
			cached, found, lookupErr := runtime.FindActionResult(ctx, room.ID, requestID, fingerprint)
			if lookupErr != nil {
				return nil, mapMultiplayerRuntimeError(lookupErr)
			}
			if found {
				committed, decodeErr := decodeMultiplayerActionResult(cached.ResponseJSON, false)
				if decodeErr != nil {
					return nil, decodeErr
				}
				if err := advanceMultiplayerProgress(ctx, gameRepo, room.ID, committed.CurrentTurn, committed.RoundNumber); err != nil {
					return nil, err
				}
				s.flushAutoSaveBestEffort(ctx, room.ID)
				s.publishCommittedMultiplayerAction(room.ID, requestID, committed)
				return committed, nil
			}
		}
		s.cancelMultiplayerAction(room, req, generation, requestID, fingerprint, "commit_failed")
		return nil, mapMultiplayerRuntimeError(err)
	}
	committed, err := decodeMultiplayerActionResult(commit.ResponseJSON, commit.Duplicate)
	if err != nil {
		return nil, err
	}
	if err := advanceMultiplayerProgress(ctx, gameRepo, room.ID, committed.CurrentTurn, committed.RoundNumber); err != nil {
		return nil, err
	}
	s.flushAutoSaveBestEffort(ctx, room.ID)
	if !commit.Duplicate {
		s.publishCommittedMultiplayerAction(room.ID, requestID, committed)
	}
	return committed, nil
}

func (s *GameService) SkipMultiplayerTurn(ctx context.Context, req *SkipMultiplayerTurnRequest) (*model.MultiplayerSkipResult, error) {
	if req == nil || req.UserID == 0 || req.RoomID == 0 || req.ExpectedTurn < 0 || uuid.Validate(strings.TrimSpace(req.RequestID)) != nil {
		return nil, ErrInvalidGameAction
	}
	gameRepo, runtime, err := s.multiplayerDependencies()
	if err != nil {
		return nil, err
	}
	room, err := gameRepo.FindRoomByID(ctx, req.RoomID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find multiplayer room: %v", ErrInternal, err)
	}
	if room == nil || room.IsSolo {
		return nil, ErrGameRoomNotFound
	}
	player, err := s.gameRepo.FindPlayer(ctx, room.ID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find multiplayer player: %v", ErrInternal, err)
	}
	if player == nil || player.Status != model.RoomPlayerStatusActive {
		return nil, ErrGameRoomNotFound
	}
	snapshot, err := runtime.GetMultiplayerRoom(ctx, room.ID)
	if err != nil {
		return nil, mapMultiplayerRuntimeError(err)
	}
	fingerprint := multiplayerSkipFingerprint(room.ID, req.UserID, snapshot.Generation, req.ExpectedTurn, "manual")
	if cached, found, lookupErr := runtime.FindActionResult(ctx, room.ID, req.RequestID, fingerprint); lookupErr != nil {
		return nil, mapMultiplayerRuntimeError(lookupErr)
	} else if found {
		var result model.MultiplayerSkipResult
		if cached == nil || json.Unmarshal(cached.ResponseJSON, &result) != nil || result.Generation != snapshot.Generation ||
			result.CurrentTurn != req.ExpectedTurn+1 || result.Reason != "manual" {
			return nil, ErrMultiplayerRuntimeUnavailable
		}
		result.Duplicate = true
		if err := advanceMultiplayerProgress(ctx, gameRepo, room.ID, result.CurrentTurn, result.RoundNumber); err != nil {
			return nil, err
		}
		s.flushAutoSaveBestEffort(ctx, room.ID)
		return &result, nil
	}
	if snapshot.CurrentTurn != req.ExpectedTurn {
		return nil, ErrMultiplayerTurnConflict
	}
	return s.skipMultiplayer(ctx, gameRepo, runtime, room, snapshot, req.UserID, req.RequestID, "manual", s.now().UTC(), false)
}

func (s *GameService) ProcessMultiplayerDeadline(ctx context.Context, task model.MultiplayerDeadlineTask, now time.Time) error {
	gameRepo, runtime, err := s.multiplayerDependencies()
	if err != nil {
		return err
	}
	room, err := gameRepo.FindRoomByID(ctx, task.RoomID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return runtime.DiscardMultiplayerDeadline(ctx, task)
	}
	if err != nil {
		return fmt.Errorf("%w: find multiplayer deadline room: %v", ErrInternal, err)
	}
	if room == nil || room.IsSolo {
		return runtime.DiscardMultiplayerDeadline(ctx, task)
	}
	snapshot, err := runtime.GetMultiplayerRoom(ctx, task.RoomID)
	if err != nil {
		if errors.Is(err, repo.ErrGameRuntimeUnavailable) || errors.Is(err, repo.ErrGameRuntimeStatusConflict) {
			return runtime.DiscardMultiplayerDeadline(ctx, task)
		}
		return mapMultiplayerRuntimeError(err)
	}
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(task.Member)).String()
	if snapshot.Generation != task.Generation || snapshot.CurrentTurn != task.Turn || snapshot.Status != model.RoomStatusPlaying {
		return runtime.DiscardMultiplayerDeadline(ctx, task)
	}
	if lease := snapshot.ActionLease; lease != nil {
		if now.Before(lease.ClaimedAt.Add(repo.MultiplayerActionRecoveryTimeout)) {
			return nil
		}
		releaseErr := runtime.ReleaseMultiplayerAction(ctx, &model.MultiplayerActionMutation{
			RoomID: task.RoomID, UserID: lease.UserID, Generation: lease.Generation, ExpectedTurn: lease.Turn,
			RequestID: lease.RequestID, RequestFingerprint: lease.Fingerprint,
		}, now.UTC().Add(time.Duration(room.TurnTimeoutSeconds)*time.Second))
		if errors.Is(releaseErr, repo.ErrGameRuntimeGenerationConflict) {
			return runtime.DiscardMultiplayerDeadline(ctx, task)
		}
		if releaseErr != nil {
			return mapMultiplayerRuntimeError(releaseErr)
		}
		s.publishMultiplayer(task.RoomID, lease.RequestID, GameActionStreamEvent{
			Type: "action_cancelled", Multiplayer: true, Generation: lease.Generation,
			CurrentTurn: lease.Turn, PlayerID: lease.UserID, Reason: "recovery_timeout",
		})
		return nil
	}
	if snapshot.DeadlineAt != nil && now.Before(*snapshot.DeadlineAt) {
		return nil
	}
	_, err = s.skipMultiplayer(ctx, gameRepo, runtime, room, snapshot, 0, requestID, "timeout", now.UTC(), true)
	return err
}

func (s *GameService) skipMultiplayer(ctx context.Context, gameRepo MultiplayerGameRepository, runtime MultiplayerTurnRepository, room *model.GameRoom, snapshot *model.MultiplayerRuntimeSnapshot, userID uint, requestID, reason string, now time.Time, timeout bool) (*model.MultiplayerSkipResult, error) {
	expectedTurn := snapshot.CurrentTurn
	if timeout && snapshot.Generation == "" {
		return nil, ErrGameRuntimeUnavailable
	}
	nextTurn := expectedTurn + 1
	nextDeadline := now.Add(time.Duration(room.TurnTimeoutSeconds) * time.Second)
	result := &model.MultiplayerSkipResult{
		Generation: snapshot.Generation, SkippedUserID: snapshot.CurrentActorID,
		CurrentTurn: nextTurn, RoundNumber: nextTurn / len(snapshot.TurnOrder),
		CurrentActorID: snapshot.TurnOrder[nextTurn%len(snapshot.TurnOrder)], DeadlineAt: nextDeadline, Reason: reason,
	}
	response, _ := json.Marshal(result)
	fingerprint := multiplayerSkipFingerprint(room.ID, userID, snapshot.Generation, expectedTurn, reason)
	committed, err := runtime.SkipMultiplayerTurn(ctx, &model.MultiplayerSkipRequest{
		RoomID: room.ID, UserID: userID, Generation: snapshot.Generation, ExpectedTurn: expectedTurn,
		RequestID: requestID, RequestFingerprint: fingerprint, ResponseJSON: response,
		Reason: reason, Now: now, NextDeadline: nextDeadline, Timeout: timeout,
	})
	if err != nil {
		return nil, mapMultiplayerRuntimeError(err)
	}
	if err := advanceMultiplayerProgress(ctx, gameRepo, room.ID, committed.CurrentTurn, committed.RoundNumber); err != nil {
		return nil, err
	}
	s.flushAutoSaveBestEffort(ctx, room.ID)
	if !committed.Duplicate {
		s.publishMultiplayer(room.ID, requestID, GameActionStreamEvent{
			Type: "turn_skip", Multiplayer: true, Generation: committed.Generation,
			CurrentTurn: committed.CurrentTurn, PlayerID: committed.SkippedUserID, Reason: committed.Reason,
			Result: skipAsActionResult(committed),
		})
		s.publishMultiplayer(room.ID, requestID, GameActionStreamEvent{
			Type: "turn_start", Multiplayer: true, Generation: committed.Generation,
			CurrentTurn: committed.CurrentTurn, PlayerID: committed.CurrentActorID,
			Result: skipAsActionResult(committed),
		})
	}
	return committed, nil
}

func (s *GameService) flushAutoSaveBestEffort(ctx context.Context, roomID uint) {
	if err := s.FlushPendingMultiplayerAutoSaves(ctx, roomID); err != nil {
		log.Printf("flush multiplayer auto-save room=%d: %v", roomID, err)
	}
}

func (s *GameService) multiplayerDependencies() (MultiplayerGameRepository, MultiplayerTurnRepository, error) {
	gameRepo, gameOK := s.gameRepo.(MultiplayerGameRepository)
	runtime, runtimeOK := s.runtimeRepo.(MultiplayerTurnRepository)
	if !gameOK || !runtimeOK || s.multiplayerPublisher == nil {
		return nil, nil, ErrGameRuntimeUnavailable
	}
	return gameRepo, runtime, nil
}

func multiplayerParticipants(ctx context.Context, gameRepo MultiplayerGameRepository, room *model.GameRoom, snapshot *model.MultiplayerRuntimeSnapshot) ([]model.RoomPlayer, []ai_client.GameParticipant, error) {
	players, err := gameRepo.FindPlayersByRoom(ctx, room.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: list multiplayer players: %v", ErrInternal, err)
	}
	active := make([]model.RoomPlayer, 0, len(players))
	for _, player := range players {
		if player.Status == model.RoomPlayerStatusActive {
			active = append(active, player)
		}
	}
	if len(active) != len(snapshot.TurnOrder) || len(active) < 2 {
		return nil, nil, ErrGameRuntimeUnavailable
	}
	participants := make([]ai_client.GameParticipant, len(active))
	runtimeCharacters := make(map[uint]uint, len(snapshot.Players))
	for _, player := range snapshot.Players {
		runtimeCharacters[player.UserID] = player.CharacterID
	}
	for index, player := range active {
		if player.CharacterID == nil || player.UserID != snapshot.TurnOrder[index] ||
			runtimeCharacters[player.UserID] != *player.CharacterID {
			return nil, nil, ErrGameRuntimeUnavailable
		}
		participants[index] = ai_client.GameParticipant{UserID: player.UserID, CharacterID: *player.CharacterID}
	}
	return active, participants, nil
}

func multiplayerRuntimeMutations(effects *MultiplayerActionEffects) []model.MultiplayerPlayerMutation {
	result := make([]model.MultiplayerPlayerMutation, len(effects.Players))
	for index, player := range effects.Players {
		result[index] = model.MultiplayerPlayerMutation{
			UserID: player.UserID, PlayerStateChanges: player.PlayerStateChanges,
			ItemMutations: runtimeItemMutations(player.Items), BuffMutations: runtimeBuffMutations(player.Buffs),
		}
	}
	return result
}

func (s *GameService) cancelMultiplayerAction(room *model.GameRoom, req *SubmitGameActionRequest, generation, requestID, fingerprint, reason string) {
	if room == nil {
		return
	}
	runtime, ok := s.runtimeRepo.(MultiplayerTurnRepository)
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := runtime.ReleaseMultiplayerAction(ctx, &model.MultiplayerActionMutation{
			RoomID: room.ID, UserID: req.UserID, Generation: generation, ExpectedTurn: req.ExpectedTurn,
			RequestID: requestID, RequestFingerprint: fingerprint,
		}, s.now().UTC().Add(time.Duration(room.TurnTimeoutSeconds)*time.Second)); err != nil &&
			!errors.Is(err, repo.ErrGameRuntimeGenerationConflict) {
			log.Printf("multiplayer action cleanup room=%d turn=%d: %v", room.ID, req.ExpectedTurn, err)
		}
		cancel()
	}
	s.publishMultiplayer(room.ID, requestID, GameActionStreamEvent{
		Type: "action_cancelled", Multiplayer: true, Generation: generation,
		CurrentTurn: req.ExpectedTurn, PlayerID: req.UserID, Reason: reason,
	})
}

func (s *GameService) publishCommittedMultiplayerAction(roomID uint, requestID string, result *SubmitGameActionResult) {
	if result.DiceRoll != nil {
		s.publishMultiplayer(roomID, requestID, GameActionStreamEvent{Type: "dice_roll", Multiplayer: true, Generation: result.Generation, CurrentTurn: result.CurrentTurn, Result: result})
	}
	for _, player := range result.MultiplayerEffects.Players {
		s.publishMultiplayer(roomID, requestID, GameActionStreamEvent{Type: "status_update", Multiplayer: true, Generation: result.Generation, CurrentTurn: result.CurrentTurn, PlayerID: player.UserID, Result: result})
	}
	s.publishMultiplayer(roomID, requestID, GameActionStreamEvent{Type: "narrative_complete", Content: result.Narrative, Multiplayer: true, Generation: result.Generation, CurrentTurn: result.CurrentTurn, Result: result})
	s.publishMultiplayer(roomID, requestID, GameActionStreamEvent{Type: "turn_start", Multiplayer: true, Generation: result.Generation, CurrentTurn: result.CurrentTurn, PlayerID: result.CurrentActorID, Result: result})
}

func (s *GameService) publishMultiplayer(roomID uint, requestID string, event GameActionStreamEvent) {
	if s.multiplayerPublisher != nil {
		s.multiplayerPublisher.PublishMultiplayerActionEvent(roomID, requestID, event)
	}
}

func decodeMultiplayerActionResult(encoded json.RawMessage, duplicate bool) (*SubmitGameActionResult, error) {
	var result SubmitGameActionResult
	if len(encoded) == 0 || decodeStrictJSON(encoded, &result) != nil || strings.TrimSpace(result.Narrative) == "" ||
		result.MultiplayerEffects == nil || result.MultiplayerEffects.Players == nil || result.MultiplayerEffects.Events == nil ||
		uuid.Validate(result.Generation) != nil || result.CurrentTurn <= 0 || result.CurrentActorID == 0 || result.DeadlineAt == nil || result.DeadlineAt.IsZero() {
		return nil, fmt.Errorf("%w: malformed cached multiplayer action", ErrInternal)
	}
	if result.DiceRoll != nil {
		normalized, err := normalizeActionDiceRoll(&ai_client.DiceRollData{
			Type: result.DiceRoll.Type, Result: result.DiceRoll.Result, Target: result.DiceRoll.Target,
			Success: result.DiceRoll.Success, CriticalHit: result.DiceRoll.CriticalHit, CriticalMiss: result.DiceRoll.CriticalMiss,
			Description: result.DiceRoll.Description, Reason: result.DiceRoll.Reason,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: malformed cached multiplayer dice", ErrInternal)
		}
		result.DiceRoll = normalized
	}
	result.Narrative = strings.TrimSpace(result.Narrative)
	result.Duplicate = duplicate
	return &result, nil
}

func advanceMultiplayerProgress(ctx context.Context, repository MultiplayerGameRepository, roomID uint, turn, round int) error {
	updated, err := repository.AdvanceMultiplayerRoomProgress(ctx, roomID, turn, round)
	if err != nil {
		return fmt.Errorf("%w: persist multiplayer progress: %v", ErrInternal, err)
	}
	if !updated {
		return ErrGameRoomNotPlaying
	}
	return nil
}

func multiplayerSkipFingerprint(roomID, userID uint, generation string, turn int, reason string) string {
	payload := fmt.Sprintf("%d:%d:%s:%d:%s", roomID, userID, generation, turn, reason)
	value := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", value[:])
}

func skipAsActionResult(result *model.MultiplayerSkipResult) *SubmitGameActionResult {
	deadline := result.DeadlineAt
	return &SubmitGameActionResult{
		Generation: result.Generation, CurrentTurn: result.CurrentTurn, RoundNumber: result.RoundNumber,
		CurrentActorID: result.CurrentActorID, DeadlineAt: &deadline,
	}
}

func mapMultiplayerRuntimeError(err error) error {
	switch {
	case errors.Is(err, repo.ErrGameRuntimeNotPlaying):
		return ErrGameRoomNotPlaying
	case errors.Is(err, repo.ErrMultiplayerNotCurrentActor):
		return ErrMultiplayerNotActor
	case errors.Is(err, repo.ErrMultiplayerActionInProgress):
		return ErrMultiplayerActionInProgress
	case errors.Is(err, repo.ErrActionIdempotencyConflict):
		return ErrMultiplayerRequestConflict
	case errors.Is(err, repo.ErrInsufficientItemQuantity):
		return ErrInsufficientItems
	case errors.Is(err, repo.ErrGameRuntimeConflict), errors.Is(err, repo.ErrGameRuntimeGenerationConflict), errors.Is(err, repo.ErrMultiplayerDeadlineNotReached):
		return ErrMultiplayerTurnConflict
	case errors.Is(err, repo.ErrGameRuntimeUnavailable), errors.Is(err, repo.ErrGameRuntimeStatusConflict):
		return ErrMultiplayerRuntimeUnavailable
	default:
		return fmt.Errorf("%w: multiplayer runtime: %v", ErrInternal, err)
	}
}
