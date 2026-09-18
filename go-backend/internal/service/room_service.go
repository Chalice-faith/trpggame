package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

var (
	ErrInvalidRoomRequest         = errors.New("invalid room request")
	ErrRoomNotFound               = errors.New("room not found")
	ErrRoomScriptUnavailable      = errors.New("room script unavailable")
	ErrRoomCharactersInsufficient = errors.New("not enough script characters")
	ErrRoomCapacityReached        = errors.New("room capacity reached")
	ErrRoomNotWaiting             = errors.New("room not waiting")
	ErrRoomRejoinDenied           = errors.New("room rejoin denied")
	ErrRoomPermissionDenied       = errors.New("room permission denied")
	ErrRoomInvalidCharacter       = errors.New("invalid room character")
	ErrRoomCharacterTaken         = errors.New("room character taken")
	ErrRoomStartConditions        = errors.New("room start conditions unmet")
	ErrRoomOwnerLeave             = errors.New("owner must transfer before leaving")
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

type RoomService struct {
	rooms RoomRepository
	now   func() time.Time
}

func NewRoomService(rooms RoomRepository) *RoomService {
	return &RoomService{rooms: rooms, now: time.Now}
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
	return roomSnapshot(record), nil
}

func (s *RoomService) SelectCharacter(ctx context.Context, actorID, roomID, characterID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || characterID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.SelectCharacter(ctx, actorID, roomID, characterID, version)
	if err != nil {
		return nil, mapRoomError(err)
	}
	return roomSnapshot(record), nil
}

func (s *RoomService) SetReady(ctx context.Context, actorID, roomID uint, ready bool, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.SetReady(ctx, actorID, roomID, ready, version)
	if err != nil {
		return nil, mapRoomError(err)
	}
	return roomSnapshot(record), nil
}

func (s *RoomService) Leave(ctx context.Context, actorID, roomID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.Leave(ctx, actorID, roomID, version, s.now().UTC())
	if err != nil {
		return nil, mapRoomError(err)
	}
	return roomSnapshot(record), nil
}

func (s *RoomService) Remove(ctx context.Context, actorID, roomID, targetID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || targetID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.Remove(ctx, actorID, roomID, targetID, version, s.now().UTC())
	if err != nil {
		return nil, mapRoomError(err)
	}
	return roomSnapshot(record), nil
}

func (s *RoomService) Transfer(ctx context.Context, actorID, roomID, targetID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || targetID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.Transfer(ctx, actorID, roomID, targetID, version)
	if err != nil {
		return nil, mapRoomError(err)
	}
	return roomSnapshot(record), nil
}

func (s *RoomService) Start(ctx context.Context, actorID, roomID uint, version uint64) (*RoomSnapshot, error) {
	if actorID == 0 || roomID == 0 || version == 0 {
		return nil, ErrInvalidRoomRequest
	}
	record, err := s.rooms.Start(ctx, actorID, roomID, version)
	if err != nil {
		return nil, mapRoomError(err)
	}
	return roomSnapshot(record), nil
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
