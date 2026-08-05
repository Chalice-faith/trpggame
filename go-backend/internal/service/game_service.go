package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"trpggame/internal/ai_client"
	"trpggame/internal/model"
)

const (
	maxGameRoomNameLength = 128
	startCleanupTimeout   = 5 * time.Second
)

// GameRepository 描述单人游戏启动所需的数据访问能力。
type GameRepository interface {
	CreateRoomWithPlayer(ctx context.Context, room *model.GameRoom, player *model.RoomPlayer) error
	CreateSave(ctx context.Context, save *model.GameSave) error
	FindRoomByIDAndOwnerID(ctx context.Context, id, ownerID uint) (*model.GameRoom, error)
	FindPlayer(ctx context.Context, roomID, userID uint) (*model.RoomPlayer, error)
	TransitionRoomStatus(
		ctx context.Context,
		roomID uint,
		ownerID uint,
		from []model.RoomStatus,
		to model.RoomStatus,
	) (bool, error)
}

// GameScriptRepository 描述单人游戏启动所需的剧本查询能力。
type GameScriptRepository interface {
	FindByIDAndUserID(id, userID uint) (*model.Script, error)
	FindCharactersByScriptID(scriptID uint) ([]model.ScriptCharacter, error)
}

// GameInferenceClient 描述开场叙事调用能力。
type GameInferenceClient interface {
	StartGame(
		ctx context.Context,
		req *ai_client.StartGameRequest,
	) (*ai_client.StartGameResponse, error)
	SubmitAction(
		ctx context.Context,
		req *ai_client.GameActionRequest,
	) (*ai_client.GameActionResponse, error)
}

// GameRuntimeRepository 描述单人游戏运行态初始化与失败清理能力。
type GameRuntimeRepository interface {
	InitializeSoloRoom(ctx context.Context, state *model.SoloRuntimeState) error
	DeleteSoloRoom(ctx context.Context, roomID, userID uint) error
	CaptureSoloRoom(ctx context.Context, roomID, userID uint) (*model.SoloRuntimeSnapshot, error)
	FindActionResult(
		ctx context.Context,
		roomID uint,
		requestID string,
		fingerprint string,
	) (*model.ActionCommitResult, bool, error)
	CommitAction(
		ctx context.Context,
		mutation *model.ActionRuntimeMutation,
	) (*model.ActionCommitResult, error)
}

// GameService 单人游戏业务逻辑。
type GameService struct {
	gameRepo    GameRepository
	scriptRepo  GameScriptRepository
	aiClient    GameInferenceClient
	runtimeRepo GameRuntimeRepository
}

// StartSoloGameRequest 是单人快速开始的服务层请求。
type StartSoloGameRequest struct {
	UserID      uint
	ScriptID    uint
	CharacterID uint
}

// StartSoloGameResult 是单人快速开始的服务层结果。
type StartSoloGameResult struct {
	Room             *model.GameRoom
	Player           *model.RoomPlayer
	OpeningNarrative string
}

// NewGameService 创建 GameService。
func NewGameService(
	gameRepository GameRepository,
	scriptRepository GameScriptRepository,
	aiClient GameInferenceClient,
	runtimeRepository GameRuntimeRepository,
) *GameService {
	return &GameService{
		gameRepo:    gameRepository,
		scriptRepo:  scriptRepository,
		aiClient:    aiClient,
		runtimeRepo: runtimeRepository,
	}
}

// StartSoloGame 校验剧本与角色，创建房间，并在开场生成成功后启动游戏。
func (s *GameService) StartSoloGame(
	ctx context.Context,
	req *StartSoloGameRequest,
) (*StartSoloGameResult, error) {
	if req == nil || req.UserID == 0 || req.ScriptID == 0 || req.CharacterID == 0 {
		return nil, ErrInvalidGameRequest
	}

	script, err := s.scriptRepo.FindByIDAndUserID(req.ScriptID, req.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrScriptNotFound
		}
		return nil, fmt.Errorf("%w: find game script: %v", ErrInternal, err)
	}
	if script.Status != model.ScriptStatusReady {
		return nil, ErrScriptNotReady
	}

	characters, err := s.scriptRepo.FindCharactersByScriptID(script.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: list script characters: %v", ErrInternal, err)
	}
	character := findCharacter(characters, req.CharacterID)
	if character == nil {
		return nil, ErrCharacterNotFound
	}
	playerState, err := characterRuntimeState(character)
	if err != nil {
		return nil, fmt.Errorf("%w: prepare character runtime state: %v", ErrInternal, err)
	}

	turnOrder, err := json.Marshal([]uint{req.UserID})
	if err != nil {
		return nil, fmt.Errorf("%w: encode turn order: %v", ErrInternal, err)
	}
	room := &model.GameRoom{
		Name:        soloRoomName(script.Title),
		ScriptID:    script.ID,
		OwnerID:     req.UserID,
		Status:      model.RoomStatusWaiting,
		MaxPlayers:  1,
		CurrentTurn: 0,
		RoundNumber: 0,
		TurnOrder:   turnOrder,
		IsSolo:      true,
	}
	characterID := req.CharacterID
	player := &model.RoomPlayer{
		UserID:      req.UserID,
		CharacterID: &characterID,
		PlayerOrder: 0,
		IsReady:     true,
	}
	if err := s.gameRepo.CreateRoomWithPlayer(ctx, room, player); err != nil {
		return nil, fmt.Errorf("%w: create solo room: %v", ErrInternal, err)
	}

	opening, err := s.aiClient.StartGame(ctx, &ai_client.StartGameRequest{
		RoomID:      room.ID,
		ScriptID:    script.ID,
		CharacterID: req.CharacterID,
		UserID:      req.UserID,
	})
	if err != nil {
		return nil, s.abortStart(
			ctx,
			room,
			fmt.Errorf("%w: generate opening narrative: %v", ErrAIUnavailable, err),
			false,
			[]model.RoomStatus{model.RoomStatusWaiting},
		)
	}
	if opening == nil || strings.TrimSpace(opening.Narrative) == "" {
		return nil, s.abortStart(
			ctx,
			room,
			ErrEmptyOpeningNarrative,
			false,
			[]model.RoomStatus{model.RoomStatusWaiting},
		)
	}

	openingNarrative := strings.TrimSpace(opening.Narrative)
	if err := s.runtimeRepo.InitializeSoloRoom(ctx, &model.SoloRuntimeState{
		RoomID:      room.ID,
		UserID:      req.UserID,
		Status:      model.RoomStatusPlaying,
		Turn:        room.RoundNumber,
		PlayerState: playerState,
		Opening: model.RuntimeMessage{
			Role:    "assistant",
			Content: openingNarrative,
		},
	}); err != nil {
		return nil, s.abortStart(
			ctx,
			room,
			fmt.Errorf("%w: initialize game runtime: %v", ErrInternal, err),
			true,
			[]model.RoomStatus{model.RoomStatusWaiting},
		)
	}

	started, err := s.gameRepo.TransitionRoomStatus(
		ctx,
		room.ID,
		req.UserID,
		[]model.RoomStatus{model.RoomStatusWaiting},
		model.RoomStatusPlaying,
	)
	if err != nil {
		return nil, s.abortStart(
			ctx,
			room,
			fmt.Errorf("%w: activate solo room: %v", ErrInternal, err),
			true,
			[]model.RoomStatus{model.RoomStatusWaiting, model.RoomStatusPlaying},
		)
	}
	if !started {
		return nil, s.abortStart(
			ctx,
			room,
			ErrGameStartConflict,
			true,
			[]model.RoomStatus{model.RoomStatusWaiting, model.RoomStatusPlaying},
		)
	}

	room.Status = model.RoomStatusPlaying
	return &StartSoloGameResult{
		Room:             room,
		Player:           player,
		OpeningNarrative: openingNarrative,
	}, nil
}

func (s *GameService) abortStart(
	ctx context.Context,
	room *model.GameRoom,
	cause error,
	cleanupRuntime bool,
	from []model.RoomStatus,
) error {
	cleanupContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		startCleanupTimeout,
	)
	defer cancel()

	compensationErrors := make([]error, 0, 2)
	if cleanupRuntime {
		if err := s.runtimeRepo.DeleteSoloRoom(cleanupContext, room.ID, room.OwnerID); err != nil {
			compensationErrors = append(compensationErrors, fmt.Errorf("delete runtime: %w", err))
		}
	}

	ended, cleanupErr := s.gameRepo.TransitionRoomStatus(
		cleanupContext,
		room.ID,
		room.OwnerID,
		from,
		model.RoomStatusEnded,
	)
	if cleanupErr != nil {
		compensationErrors = append(compensationErrors, fmt.Errorf("end room: %w", cleanupErr))
	}
	if cleanupErr == nil && !ended {
		compensationErrors = append(compensationErrors, errors.New("end room changed no row"))
	}
	if ended {
		room.Status = model.RoomStatusEnded
	}
	if len(compensationErrors) > 0 {
		return fmt.Errorf(
			"%w: %v; compensate failed start: %v",
			ErrInternal,
			cause,
			errors.Join(compensationErrors...),
		)
	}
	return cause
}

func findCharacter(characters []model.ScriptCharacter, characterID uint) *model.ScriptCharacter {
	for index := range characters {
		if characters[index].ID == characterID {
			return &characters[index]
		}
	}
	return nil
}

func characterRuntimeState(character *model.ScriptCharacter) (map[string]string, error) {
	state := map[string]string{"character_id": fmt.Sprint(character.ID)}
	attributes := strings.TrimSpace(character.Attributes)
	if attributes == "" {
		return state, nil
	}

	var rawAttributes map[string]json.RawMessage
	if err := json.Unmarshal([]byte(attributes), &rawAttributes); err != nil {
		return nil, err
	}
	if rawAttributes == nil {
		return nil, errors.New("character attributes must be a JSON object")
	}
	for field, rawValue := range rawAttributes {
		decoder := json.NewDecoder(strings.NewReader(string(rawValue)))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		switch typed := value.(type) {
		case string:
			state[field] = typed
		case json.Number:
			state[field] = typed.String()
		case bool:
			state[field] = fmt.Sprint(typed)
		case nil:
			state[field] = ""
		default:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return nil, err
			}
			state[field] = string(encoded)
		}
	}
	// 剧本属性不能覆盖服务端确认过的角色身份。
	state["character_id"] = fmt.Sprint(character.ID)
	return state, nil
}

func soloRoomName(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "单人游戏"
	}
	runes := []rune(title)
	if len(runes) > maxGameRoomNameLength {
		runes = runes[:maxGameRoomNameLength]
	}
	return string(runes)
}
