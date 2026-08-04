package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"trpggame/internal/ai_client"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

// SubmitGameActionRequest 是玩家行动的服务层请求。
type SubmitGameActionRequest struct {
	UserID       uint
	RoomID       uint
	RequestID    string
	ExpectedTurn int
	Action       string
}

// ActionDiceRoll 是对客户端稳定的骰子响应契约。
type ActionDiceRoll struct {
	Type         string `json:"type"`
	Result       int    `json:"result"`
	Target       int    `json:"target"`
	Success      bool   `json:"success"`
	CriticalHit  bool   `json:"critical_hit"`
	CriticalMiss bool   `json:"critical_miss"`
	Description  string `json:"description"`
	Reason       string `json:"reason"`
}

// SubmitGameActionResult 是可缓存并幂等重放的玩家行动结果。
type SubmitGameActionResult struct {
	Narrative   string          `json:"narrative"`
	DiceRoll    *ActionDiceRoll `json:"dice_roll,omitempty"`
	Effects     *ActionEffects  `json:"effects"`
	CurrentTurn int             `json:"current_turn"`
	Duplicate   bool            `json:"-"`
}

// SubmitAction 校验持久权限，调用 AI，并通过 Redis CAS 原子提交运行态。
func (s *GameService) SubmitAction(
	ctx context.Context,
	req *SubmitGameActionRequest,
) (*SubmitGameActionResult, error) {
	action, requestID, fingerprint, err := validateGameActionRequest(req)
	if err != nil {
		return nil, err
	}

	room, err := s.gameRepo.FindRoomByIDAndOwnerID(ctx, req.RoomID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGameRoomNotFound
		}
		return nil, fmt.Errorf("%w: find game room: %v", ErrInternal, err)
	}
	player, err := s.gameRepo.FindPlayer(ctx, room.ID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGamePlayerNotFound
		}
		return nil, fmt.Errorf("%w: find game player: %v", ErrInternal, err)
	}
	if player.CharacterID == nil || *player.CharacterID == 0 {
		return nil, ErrGamePlayerNotFound
	}

	cached, found, err := s.runtimeRepo.FindActionResult(
		ctx,
		room.ID,
		requestID,
		fingerprint,
	)
	if err != nil {
		return nil, mapActionRuntimeError(err)
	}
	if found {
		return decodeSubmitActionResult(cached.ResponseJSON, true)
	}
	if room.Status != model.RoomStatusPlaying {
		return nil, ErrGameRoomNotPlaying
	}

	aiResult, err := s.aiClient.SubmitAction(ctx, &ai_client.GameActionRequest{
		RoomID:      room.ID,
		UserID:      req.UserID,
		Action:      action,
		ScriptID:    room.ScriptID,
		CharacterID: *player.CharacterID,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: infer game action: %v", ErrAIUnavailable, err)
	}
	if aiResult == nil || strings.TrimSpace(aiResult.Narrative) == "" {
		return nil, ErrEmptyActionNarrative
	}
	effects, err := InterpretActionEffects(req.UserID, aiResult.StatusChanges)
	if err != nil {
		return nil, err
	}
	diceRoll, err := normalizeActionDiceRoll(aiResult.DiceRoll)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidActionEffects, err)
	}

	result := &SubmitGameActionResult{
		Narrative:   strings.TrimSpace(aiResult.Narrative),
		DiceRoll:    diceRoll,
		Effects:     effects,
		CurrentTurn: req.ExpectedTurn + 1,
	}
	responseJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("%w: encode action result: %v", ErrInternal, err)
	}
	commitResult, err := s.runtimeRepo.CommitAction(ctx, &model.ActionRuntimeMutation{
		RoomID:             room.ID,
		UserID:             req.UserID,
		ExpectedTurn:       req.ExpectedTurn,
		RequestID:          requestID,
		RequestFingerprint: fingerprint,
		PlayerStateChanges: effects.PlayerStateChanges,
		ItemMutations:      runtimeItemMutations(effects.Items),
		BuffMutations:      runtimeBuffMutations(effects.Buffs),
		Messages: []model.RuntimeMessage{
			{Role: "user", Content: action},
			{Role: "assistant", Content: result.Narrative},
		},
		ResponseJSON: responseJSON,
	})
	if err != nil {
		return nil, mapActionRuntimeError(err)
	}
	if commitResult == nil || (!commitResult.Duplicate && commitResult.CurrentTurn != result.CurrentTurn) {
		return nil, fmt.Errorf("%w: invalid action commit result", ErrInternal)
	}
	return decodeSubmitActionResult(commitResult.ResponseJSON, commitResult.Duplicate)
}

func validateGameActionRequest(
	req *SubmitGameActionRequest,
) (action string, requestID string, fingerprint string, err error) {
	if req == nil || req.UserID == 0 || req.RoomID == 0 || req.ExpectedTurn < 0 {
		return "", "", "", ErrInvalidGameAction
	}
	action = strings.TrimSpace(req.Action)
	if action == "" || utf8.RuneCountInString(action) > 2000 {
		return "", "", "", ErrInvalidGameAction
	}
	parsedRequestID, parseErr := uuid.Parse(strings.TrimSpace(req.RequestID))
	if parseErr != nil {
		return "", "", "", ErrInvalidGameAction
	}
	requestID = parsedRequestID.String()
	payload, marshalErr := json.Marshal(struct {
		UserID       uint   `json:"user_id"`
		RoomID       uint   `json:"room_id"`
		ExpectedTurn int    `json:"expected_turn"`
		Action       string `json:"action"`
	}{req.UserID, req.RoomID, req.ExpectedTurn, action})
	if marshalErr != nil {
		return "", "", "", ErrInvalidGameAction
	}
	fingerprintBytes := sha256.Sum256(payload)
	return action, requestID, fmt.Sprintf("%x", fingerprintBytes), nil
}

func normalizeActionDiceRoll(value *ai_client.DiceRollData) (*ActionDiceRoll, error) {
	if value == nil {
		return nil, nil
	}
	maximum := 0
	switch value.Type {
	case "D20":
		maximum = 20
	case "D100":
		maximum = 100
	default:
		return nil, errors.New("unsupported dice type")
	}
	if value.Result < 1 || value.Result > maximum || value.Target < 1 || value.Target > maximum ||
		value.CriticalHit && value.CriticalMiss {
		return nil, errors.New("invalid dice range")
	}
	criticalHit := value.Result == 20
	criticalMiss := value.Result == 1
	if value.Type == "D100" {
		criticalHit = value.Result >= 96
		criticalMiss = value.Result <= 5
	}
	success := criticalHit || (!criticalMiss && value.Result >= value.Target)
	if value.CriticalHit != criticalHit || value.CriticalMiss != criticalMiss || value.Success != success {
		return nil, errors.New("inconsistent dice result")
	}
	description := strings.TrimSpace(value.Description)
	reason := strings.TrimSpace(value.Reason)
	if description == "" || reason == "" || utf8.RuneCountInString(description) > 1000 ||
		utf8.RuneCountInString(reason) > 500 {
		return nil, errors.New("invalid dice text")
	}
	return &ActionDiceRoll{
		Type: value.Type, Result: value.Result, Target: value.Target, Success: value.Success,
		CriticalHit: value.CriticalHit, CriticalMiss: value.CriticalMiss,
		Description: description, Reason: reason,
	}, nil
}

func runtimeItemMutations(items []ItemMutation) []model.RuntimeItemMutation {
	result := make([]model.RuntimeItemMutation, len(items))
	for index, item := range items {
		result[index] = model.RuntimeItemMutation{
			Name: item.Name, QuantityDelta: item.QuantityDelta, Description: item.Description,
		}
	}
	return result
}

func runtimeBuffMutations(buffs []BuffMutation) []model.RuntimeBuffMutation {
	result := make([]model.RuntimeBuffMutation, len(buffs))
	for index, buff := range buffs {
		result[index] = model.RuntimeBuffMutation{Name: buff.Name, Duration: buff.Duration}
	}
	return result
}

func decodeSubmitActionResult(encoded json.RawMessage, duplicate bool) (*SubmitGameActionResult, error) {
	var result SubmitGameActionResult
	if len(encoded) == 0 || decodeStrictJSON(encoded, &result) != nil ||
		strings.TrimSpace(result.Narrative) == "" ||
		utf8.RuneCountInString(strings.TrimSpace(result.Narrative)) > 64<<10 ||
		result.Effects == nil || result.Effects.PlayerStateChanges == nil ||
		result.Effects.Items == nil || result.Effects.Buffs == nil || result.Effects.Events == nil ||
		result.CurrentTurn <= 0 {
		return nil, fmt.Errorf("%w: malformed cached action result", ErrInternal)
	}
	if result.DiceRoll != nil {
		normalized, err := normalizeActionDiceRoll(&ai_client.DiceRollData{
			Type: result.DiceRoll.Type, Result: result.DiceRoll.Result,
			Target: result.DiceRoll.Target, Success: result.DiceRoll.Success,
			CriticalHit: result.DiceRoll.CriticalHit, CriticalMiss: result.DiceRoll.CriticalMiss,
			Description: result.DiceRoll.Description, Reason: result.DiceRoll.Reason,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: malformed cached dice result", ErrInternal)
		}
		result.DiceRoll = normalized
	}
	result.Narrative = strings.TrimSpace(result.Narrative)
	result.Duplicate = duplicate
	return &result, nil
}

func mapActionRuntimeError(err error) error {
	switch {
	case errors.Is(err, repo.ErrGameRuntimeConflict):
		return ErrGameActionConflict
	case errors.Is(err, repo.ErrGameRuntimeNotPlaying):
		return ErrGameRoomNotPlaying
	case errors.Is(err, repo.ErrActionIdempotencyConflict):
		return ErrActionRequestConflict
	case errors.Is(err, repo.ErrInsufficientItemQuantity):
		return ErrInsufficientItems
	case errors.Is(err, repo.ErrGameRuntimeUnavailable):
		return ErrGameRuntimeUnavailable
	default:
		return fmt.Errorf("%w: action runtime: %v", ErrInternal, err)
	}
}
