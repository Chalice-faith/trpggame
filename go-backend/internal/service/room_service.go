package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"trpggame/internal/ai_client"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

var (
	ErrInvalidRoomRequest            = errors.New("invalid room request")
	ErrRoomNotFound                  = errors.New("room not found")
	ErrRoomScriptUnavailable         = errors.New("room script unavailable")
	ErrRoomCharactersInsufficient    = errors.New("not enough script characters")
	ErrRoomCapacityReached           = errors.New("room capacity reached")
	ErrRoomNotWaiting                = errors.New("room not waiting")
	ErrRoomRejoinDenied              = errors.New("room rejoin denied")
	ErrRoomPermissionDenied          = errors.New("room permission denied")
	ErrRoomInvalidCharacter          = errors.New("invalid room character")
	ErrRoomCharacterTaken            = errors.New("room character taken")
	ErrRoomStartConditions           = errors.New("room start conditions unmet")
	ErrRoomOwnerLeave                = errors.New("owner must transfer before leaving")
	ErrMultiplayerRuntimeUnavailable = errors.New("multiplayer runtime unavailable")
	ErrMultiplayerAIUnavailable      = errors.New("multiplayer AI unavailable")
	ErrMultiplayerStartInProgress    = errors.New("multiplayer start in progress")
)

type RoomRepository interface {
	Create(context.Context, uint, uint, string, string, int, time.Time) (*repo.RoomRecord, error)
	List(context.Context, uint) ([]model.GameRoom, error)
	Get(context.Context, uint, uint) (*repo.RoomRecord, error)
	Join(context.Context, uint, string, time.Time) (*repo.RoomRecord, error)
	SelectCharacter(context.Context, uint, uint, uint, uint64) (*repo.RoomRecord, error)
	SetReady(context.Context, uint, uint, bool, uint64) (*repo.RoomRecord, error)
	Leave(context.Context, uint, uint, uint64, time.Time) (*repo.RoomRecord, error)
	Remove(context.Context, uint, uint, uint, uint64, time.Time) (*repo.RoomRecord, error)
	Transfer(context.Context, uint, uint, uint, uint64) (*repo.RoomRecord, error)
	Start(context.Context, uint, uint, uint64) (*repo.RoomRecord, error)
}

type RoomCharacter struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type RoomMember struct {
	User        PublicUser `json:"user"`
	CharacterID *uint      `json:"character_id"`
	IsReady     bool       `json:"is_ready"`
	PlayerOrder int        `json:"player_order"`
	JoinedAt    time.Time  `json:"joined_at"`
}

type RoomSummary struct {
	ID         uint             `json:"id"`
	Name       string           `json:"name"`
	ScriptID   uint             `json:"script_id"`
	OwnerID    uint             `json:"owner_id"`
	Status     model.RoomStatus `json:"status"`
	MaxPlayers int              `json:"max_players"`
	RoomCode   string           `json:"room_code"`
	Version    uint64           `json:"version"`
	CreatedAt  time.Time        `json:"created_at"`
}

type RoomSnapshot struct {
	RoomSummary
	Members    []RoomMember    `json:"members"`
	Characters []RoomCharacter `json:"characters"`
}

type RoomMutation struct {
	Type           string
	Snapshot       *RoomSnapshot
	AffectedUserID uint
	RevokeAccess   bool
}

type RoomMutationPublisher interface {
	PublishRoomMutation(RoomMutation)
}

type MultiplayerRuntimeRepository interface {
	AcquireMultiplayerStartLease(context.Context, uint, string, time.Duration) error
	ReleaseMultiplayerStartLease(context.Context, uint, string) error
	InitializeMultiplayerRoom(context.Context, *model.MultiplayerRuntimeState) error
	ActivateMultiplayerRoom(context.Context, uint, string, time.Time) (*model.MultiplayerRuntimeSnapshot, error)
	PauseMultiplayerRoom(context.Context, uint, string) error
	DeleteProvisionalMultiplayerRoom(context.Context, *model.MultiplayerRuntimeState) error
	GetMultiplayerRoom(context.Context, uint) (*model.MultiplayerRuntimeSnapshot, error)
}

type MultiplayerOpeningClient interface {
	StartGame(context.Context, *ai_client.StartGameRequest) (*ai_client.StartGameResponse, error)
}

type RoomSequenceProvider interface {
	CurrentRoomSequence(context.Context, uint) (int64, error)
}

type multiplayerStartCompensator interface {
	PauseStarted(context.Context, uint, uint, uint64) (*repo.RoomRecord, error)
}

type MultiplayerRuntimePublisher interface {
	PublishMultiplayerRuntime(*model.MultiplayerRuntimeSnapshot)
}

type MultiplayerGameState struct {
	Seq int64 `json:"seq"`
	*model.MultiplayerRuntimeSnapshot
}

type RoomService struct {
	rooms     RoomRepository
	publisher RoomMutationPublisher
	runtime   MultiplayerRuntimeRepository
	ai        MultiplayerOpeningClient
	sequences RoomSequenceProvider
	presence  GamePresenceNotifier
	now       func() time.Time
}

func (s *RoomService) SetMutationPublisher(publisher RoomMutationPublisher) {
	if s != nil {
		s.publisher = publisher
	}
}

func (s *RoomService) SetPresenceNotifier(notifier GamePresenceNotifier) {
	if s != nil {
		s.presence = notifier
	}
}

func NewRoomService(rooms RoomRepository) *RoomService {
	return &RoomService{rooms: rooms, now: time.Now}
}

// ConfigureMultiplayer enables the M2.5 V2 start coordinator and read-only state endpoint.
func (s *RoomService) ConfigureMultiplayer(
	runtime MultiplayerRuntimeRepository,
	ai MultiplayerOpeningClient,
	sequences RoomSequenceProvider,
) {
	if s == nil {
		return
	}
	s.runtime, s.ai, s.sequences = runtime, ai, sequences
}

func (s *RoomService) Create(ctx context.Context, userID, scriptID uint, name string, capacity int) (*RoomSnapshot, error) {
	name = strings.TrimSpace(name)
	if userID == 0 || scriptID == 0 || name == "" || utf8.RuneCountInString(name) > 128 || capacity < 4 || capacity > 6 {
		return nil, ErrInvalidRoomRequest
	}
	for attempt := 0; attempt < 3; attempt++ {
		code, err := newRoomCode()
		if err != nil {
			return nil, fmt.Errorf("generate room code: %w", err)
		}
		record, err := s.rooms.Create(ctx, userID, scriptID, name, code, capacity, s.now().UTC())
		if err == nil {
			return roomSnapshot(record), nil
		}
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 && strings.Contains(mysqlErr.Message, "room_code") {
			continue
		}
		return nil, mapRoomError(err)
	}
	return nil, errors.New("room code collision retries exhausted")
}

func (s *RoomService) List(ctx context.Context, userID uint) ([]RoomSummary, error) {
	if userID == 0 {
		return nil, ErrInvalidRoomRequest
	}
	rooms, err := s.rooms.List(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	items := make([]RoomSummary, 0, len(rooms))
	for _, room := range rooms {
		items = append(items, roomSummary(room))
	}
	return items, nil
}

func (s *RoomService) Get(ctx context.Context, userID, roomID uint) (*RoomSnapshot, error) {
	if userID == 0 || roomID == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.Get(ctx, userID, roomID)
	if err != nil {
		return nil, mapRoomError(err)
	}
	return roomSnapshot(record), nil
}

func (s *RoomService) Join(ctx context.Context, userID uint, code string) (*RoomSnapshot, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if userID == 0 || len(code) != 8 {
		return nil, ErrInvalidRoomRequest
	}
	for _, c := range code {
		if !strings.ContainsRune(roomCodeAlphabet, c) {
			return nil, ErrInvalidRoomRequest
		}
	}
	record, err := s.rooms.Join(ctx, userID, code, s.now().UTC())
	if err != nil {
		return nil, mapRoomError(err)
	}
	return s.roomMutationResult(record), nil
}

func (s *RoomService) SelectCharacter(ctx context.Context, actorID, roomID, characterID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || characterID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.SelectCharacter(ctx, actorID, roomID, characterID, version)
	if err != nil {
		return nil, mapRoomError(err)
	}
	return s.roomMutationResult(record), nil
}

func (s *RoomService) SetReady(ctx context.Context, actorID, roomID uint, ready bool, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.SetReady(ctx, actorID, roomID, ready, version)
	if err != nil {
		return nil, mapRoomError(err)
	}
	return s.roomMutationResult(record), nil
}

func (s *RoomService) Leave(ctx context.Context, actorID, roomID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.Leave(ctx, actorID, roomID, version, s.now().UTC())
	if err != nil {
		return nil, mapRoomError(err)
	}
	return s.roomMutationResult(record), nil
}

func (s *RoomService) Remove(ctx context.Context, actorID, roomID, targetID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || targetID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.Remove(ctx, actorID, roomID, targetID, version, s.now().UTC())
	if err != nil {
		return nil, mapRoomError(err)
	}
	return s.roomMutationResult(record), nil
}

func (s *RoomService) Transfer(ctx context.Context, actorID, roomID, targetID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || targetID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.Transfer(ctx, actorID, roomID, targetID, version)
	if err != nil {
		return nil, mapRoomError(err)
	}
	return s.roomMutationResult(record), nil
}

func (s *RoomService) Start(ctx context.Context, actorID, roomID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	if s.runtime == nil && s.ai == nil {
		record, err := s.rooms.Start(ctx, actorID, roomID, version)
		if err != nil {
			return nil, mapRoomError(err)
		}
		return s.roomMutationResult(record), nil
	}
	if s.runtime == nil {
		return nil, ErrMultiplayerRuntimeUnavailable
	}
	if s.ai == nil {
		return nil, ErrMultiplayerAIUnavailable
	}
	return s.startMultiplayer(ctx, actorID, roomID, version)
}

func (s *RoomService) startMultiplayer(ctx context.Context, actorID, roomID uint, version uint64) (*RoomSnapshot, error) {
	leaseToken := uuid.NewString()
	if err := s.runtime.AcquireMultiplayerStartLease(ctx, roomID, leaseToken, repo.DefaultMultiplayerStartLeaseTTL); err != nil {
		if errors.Is(err, repo.ErrMultiplayerStartInProgress) {
			return nil, ErrMultiplayerStartInProgress
		}
		return nil, fmt.Errorf("%w: acquire start lease: %v", ErrMultiplayerRuntimeUnavailable, err)
	}
	defer s.releaseStartLease(roomID, leaseToken)

	candidate, err := s.rooms.Get(ctx, actorID, roomID)
	if err != nil {
		return nil, mapRoomError(err)
	}
	state, participants, err := multiplayerStartState(candidate, actorID, version)
	if err != nil {
		return nil, err
	}
	opening, err := s.ai.StartGame(ctx, &ai_client.StartGameRequest{
		RoomID: roomID, ScriptID: candidate.Room.ScriptID,
		UserID: state.TurnOrder[0], CharacterID: state.Players[0].CharacterID,
		Participants: participants,
	})
	if err != nil || opening == nil || strings.TrimSpace(opening.Narrative) == "" {
		return nil, fmt.Errorf("%w: generate opening narrative", ErrMultiplayerAIUnavailable)
	}
	state.Opening = model.RuntimeMessage{Role: "assistant", Content: strings.TrimSpace(opening.Narrative)}
	if err := s.runtime.InitializeMultiplayerRoom(ctx, state); err != nil {
		cleanupErr := s.deleteProvisional(state)
		if cleanupErr != nil {
			return nil, fmt.Errorf("%w: initialize provisional runtime: %v; cleanup provisional runtime: %v", ErrMultiplayerRuntimeUnavailable, err, cleanupErr)
		}
		return nil, fmt.Errorf("%w: initialize provisional runtime: %v", ErrMultiplayerRuntimeUnavailable, err)
	}

	record, err := s.rooms.Start(ctx, actorID, roomID, version)
	if err != nil {
		if cleanupErr := s.deleteProvisional(state); cleanupErr != nil {
			return nil, fmt.Errorf("%w: start room: %v; cleanup provisional runtime: %v", ErrMultiplayerRuntimeUnavailable, err, cleanupErr)
		}
		return nil, mapRoomError(err)
	}
	deadline := s.now().UTC().Add(state.TurnTimeout)
	runtimeSnapshot, activateErr := s.runtime.ActivateMultiplayerRoom(ctx, roomID, state.Generation, deadline)
	if activateErr != nil {
		compensationErr := s.safePauseStartedRoom(actorID, state, record.Room.Version)
		if compensationErr != nil {
			return nil, fmt.Errorf("%w: activate runtime: %v; safe pause: %v", ErrMultiplayerRuntimeUnavailable, activateErr, compensationErr)
		}
		return nil, fmt.Errorf("%w: activate runtime: %v", ErrMultiplayerRuntimeUnavailable, activateErr)
	}
	snapshot := s.roomMutationResult(record)
	if publisher, ok := s.publisher.(MultiplayerRuntimePublisher); ok {
		publisher.PublishMultiplayerRuntime(runtimeSnapshot)
	}
	if s.presence != nil {
		s.presence.NotifyGameStatusChanged(ctx, append([]uint(nil), state.TurnOrder...))
	}
	return snapshot, nil
}

func (s *RoomService) GetMultiplayerState(ctx context.Context, userID, roomID uint) (*MultiplayerGameState, error) {
	if userID == 0 || roomID == 0 {
		return nil, ErrInvalidRoomRequest
	}
	if s.runtime == nil {
		return nil, ErrMultiplayerRuntimeUnavailable
	}
	record, err := s.rooms.Get(ctx, userID, roomID)
	if err != nil {
		return nil, mapRoomError(err)
	}
	if record.Room.IsSolo || (record.Room.Status != model.RoomStatusPlaying && record.Room.Status != model.RoomStatusPaused) {
		return nil, ErrRoomNotFound
	}
	snapshot, err := s.runtime.GetMultiplayerRoom(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMultiplayerRuntimeUnavailable, err)
	}
	if err := validateRuntimeAgainstRoom(snapshot, record); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMultiplayerRuntimeUnavailable, err)
	}
	seq := int64(0)
	if s.sequences != nil {
		seq, err = s.sequences.CurrentRoomSequence(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("%w: read room sequence: %v", ErrMultiplayerRuntimeUnavailable, err)
		}
	}
	return &MultiplayerGameState{Seq: seq, MultiplayerRuntimeSnapshot: snapshot}, nil
}

func multiplayerStartState(
	record *repo.RoomRecord,
	actorID uint,
	expectedVersion uint64,
) (*model.MultiplayerRuntimeState, []ai_client.GameParticipant, error) {
	if record == nil || record.Room.ID == 0 || record.Room.OwnerID != actorID {
		return nil, nil, ErrRoomPermissionDenied
	}
	if record.Room.Status != model.RoomStatusWaiting {
		return nil, nil, ErrRoomNotWaiting
	}
	if record.Room.Version != expectedVersion {
		return nil, nil, &repo.RoomVersionError{Current: record.Room.Version}
	}
	if len(record.Members) < 2 || record.Room.TurnTimeoutSeconds <= 0 {
		return nil, nil, ErrRoomStartConditions
	}
	characters := make(map[uint]model.ScriptCharacter, len(record.Characters))
	for _, character := range record.Characters {
		characters[character.ID] = character
	}
	state := &model.MultiplayerRuntimeState{
		RoomID: record.Room.ID, Generation: uuid.NewString(),
		TurnOrder: make([]uint, 0, len(record.Members)), Players: make([]model.MultiplayerRuntimePlayer, 0, len(record.Members)),
		Opening:     model.RuntimeMessage{Role: "assistant", Content: "pending"},
		TurnTimeout: time.Duration(record.Room.TurnTimeoutSeconds) * time.Second,
	}
	participants := make([]ai_client.GameParticipant, 0, len(record.Members))
	seenCharacters := make(map[uint]struct{}, len(record.Members))
	for _, member := range record.Members {
		if !member.Player.IsReady || member.Player.CharacterID == nil {
			return nil, nil, ErrRoomStartConditions
		}
		characterID := *member.Player.CharacterID
		character, exists := characters[characterID]
		if !exists {
			return nil, nil, ErrRoomStartConditions
		}
		if _, duplicate := seenCharacters[characterID]; duplicate {
			return nil, nil, ErrRoomStartConditions
		}
		seenCharacters[characterID] = struct{}{}
		playerState, err := characterRuntimeState(&character)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid multiplayer character state: %w", err)
		}
		state.TurnOrder = append(state.TurnOrder, member.Player.UserID)
		state.Players = append(state.Players, model.MultiplayerRuntimePlayer{
			UserID: member.Player.UserID, CharacterID: characterID, PlayerState: playerState,
			Items: []model.RuntimeItem{}, Buffs: []model.RuntimeBuff{},
		})
		participants = append(participants, ai_client.GameParticipant{UserID: member.Player.UserID, CharacterID: characterID})
	}
	return state, participants, nil
}

func validateRuntimeAgainstRoom(snapshot *model.MultiplayerRuntimeSnapshot, record *repo.RoomRecord) error {
	if snapshot == nil || snapshot.RoomID != record.Room.ID || snapshot.Status != record.Room.Status || len(snapshot.Players) != len(record.Members) {
		return errors.New("runtime room mismatch")
	}
	var persistedOrder []uint
	if json.Unmarshal(record.Room.TurnOrder, &persistedOrder) != nil || len(persistedOrder) != len(snapshot.TurnOrder) {
		return errors.New("invalid persisted turn order")
	}
	for index, userID := range persistedOrder {
		if snapshot.TurnOrder[index] != userID {
			return errors.New("runtime turn order mismatch")
		}
	}
	players := make(map[uint]uint, len(snapshot.Players))
	for _, player := range snapshot.Players {
		players[player.UserID] = player.CharacterID
	}
	for _, member := range record.Members {
		if member.Player.CharacterID == nil || players[member.Player.UserID] != *member.Player.CharacterID {
			return errors.New("runtime roster mismatch")
		}
	}
	return nil
}

func (s *RoomService) releaseStartLease(roomID uint, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.runtime.ReleaseMultiplayerStartLease(ctx, roomID, token)
}

func (s *RoomService) deleteProvisional(state *model.MultiplayerRuntimeState) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.runtime.DeleteProvisionalMultiplayerRoom(ctx, state)
}

func (s *RoomService) safePauseStartedRoom(ownerID uint, state *model.MultiplayerRuntimeState, expectedVersion uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make([]error, 0, 2)
	if err := s.runtime.PauseMultiplayerRoom(ctx, state.RoomID, state.Generation); err != nil {
		errs = append(errs, err)
	}
	compensator, ok := s.rooms.(multiplayerStartCompensator)
	if !ok {
		errs = append(errs, errors.New("room repository cannot safe pause"))
	} else if _, err := compensator.PauseStarted(ctx, ownerID, state.RoomID, expectedVersion); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *RoomService) roomMutationResult(record *repo.RoomRecord) *RoomSnapshot {
	snapshot := roomSnapshot(record)
	if s.publisher != nil && record != nil && record.Mutation != "" {
		s.publisher.PublishRoomMutation(RoomMutation{
			Type: record.Mutation, Snapshot: snapshot, AffectedUserID: record.AffectedUserID,
			RevokeAccess: record.Mutation == "room_member_left",
		})
	}
	return snapshot
}

const roomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newRoomCode() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	var code [8]byte
	for i, value := range raw {
		code[i] = roomCodeAlphabet[int(value)%len(roomCodeAlphabet)]
	}
	return string(code[:]), nil
}

func roomSummary(room model.GameRoom) RoomSummary {
	code := ""
	if room.RoomCode != nil {
		code = *room.RoomCode
	}
	return RoomSummary{ID: room.ID, Name: room.Name, ScriptID: room.ScriptID, OwnerID: room.OwnerID,
		Status: room.Status, MaxPlayers: room.MaxPlayers, RoomCode: code, Version: room.Version, CreatedAt: room.CreatedAt}
}

func roomSnapshot(record *repo.RoomRecord) *RoomSnapshot {
	if record == nil {
		return nil
	}
	result := &RoomSnapshot{RoomSummary: roomSummary(record.Room), Members: make([]RoomMember, 0, len(record.Members)), Characters: make([]RoomCharacter, 0, len(record.Characters))}
	for _, member := range record.Members {
		result.Members = append(result.Members, RoomMember{User: PublicUser{ID: member.User.ID, Username: member.User.Username, Nickname: member.User.Nickname, AvatarURL: member.User.AvatarURL},
			CharacterID: member.Player.CharacterID, IsReady: member.Player.IsReady, PlayerOrder: member.Player.PlayerOrder, JoinedAt: member.Player.JoinedAt})
	}
	for _, character := range record.Characters {
		result.Characters = append(result.Characters, RoomCharacter{ID: character.ID, Name: character.Name, Description: character.Description})
	}
	return result
}

func mapRoomError(err error) error {
	switch {
	case errors.Is(err, repo.ErrRoomMissing):
		return ErrRoomNotFound
	case errors.Is(err, repo.ErrRoomScriptUnavailable):
		return ErrRoomScriptUnavailable
	case errors.Is(err, repo.ErrRoomCharactersInsufficient):
		return ErrRoomCharactersInsufficient
	case errors.Is(err, repo.ErrRoomClosed):
		return ErrRoomNotWaiting
	case errors.Is(err, repo.ErrRoomFull):
		return ErrRoomCapacityReached
	case errors.Is(err, repo.ErrRoomRemoved):
		return ErrRoomRejoinDenied
	case errors.Is(err, repo.ErrRoomPermission):
		return ErrRoomPermissionDenied
	case errors.Is(err, repo.ErrRoomInvalidCharacter):
		return ErrRoomInvalidCharacter
	case errors.Is(err, repo.ErrRoomCharacterTaken):
		return ErrRoomCharacterTaken
	case errors.Is(err, repo.ErrRoomStartConditions):
		return ErrRoomStartConditions
	case errors.Is(err, repo.ErrRoomOwnerLeave):
		return ErrRoomOwnerLeave
	default:
		return err
	}
}
