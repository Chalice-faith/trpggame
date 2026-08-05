package repo

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"trpggame/internal/model"
)

func TestGameRepoCreateRoomWithPlayerCommitsBothRecords(t *testing.T) {
	repository, mock := newMockGameRepo(t)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `game_rooms`").
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectExec("INSERT INTO `room_players`").
		WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectCommit()

	room := validGameRoom()
	player := &model.RoomPlayer{UserID: 7, PlayerOrder: 0, IsReady: true}
	err := repository.CreateRoomWithPlayer(context.Background(), room, player)

	if err != nil {
		t.Fatalf("CreateRoomWithPlayer() error = %v", err)
	}
	if room.ID != 41 || player.ID != 51 || player.RoomID != 41 {
		t.Fatalf("created room/player IDs = (%d, %d, room=%d)", room.ID, player.ID, player.RoomID)
	}
	assertSQLExpectations(t, mock)
}

func TestGameRepoCreateRoomWithPlayerRollsBackAndRestoresIDs(t *testing.T) {
	repository, mock := newMockGameRepo(t)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `game_rooms`").
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectExec("INSERT INTO `room_players`").
		WillReturnError(errors.New("duplicate player"))
	mock.ExpectRollback()

	room := validGameRoom()
	player := &model.RoomPlayer{UserID: 7}
	err := repository.CreateRoomWithPlayer(context.Background(), room, player)

	if err == nil {
		t.Fatal("CreateRoomWithPlayer() error = nil, want transaction failure")
	}
	if room.ID != 0 || player.ID != 0 || player.RoomID != 0 {
		t.Fatalf("rolled-back IDs were not restored: room=%d player=%d room_id=%d", room.ID, player.ID, player.RoomID)
	}
	assertSQLExpectations(t, mock)
}

func TestGameRepoTransitionRoomStatusUsesOwnerAndSourceStatusCAS(t *testing.T) {
	repository, mock := newMockGameRepo(t)
	mock.ExpectBegin()
	mock.ExpectExec(
		"UPDATE `game_rooms` SET .* WHERE id = \\? AND owner_id = \\? AND status IN \\(\\?,\\?\\)",
	).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	updated, err := repository.TransitionRoomStatus(
		context.Background(),
		41,
		7,
		[]model.RoomStatus{model.RoomStatusPlaying, model.RoomStatusPaused},
		model.RoomStatusEnded,
	)

	if err != nil || !updated {
		t.Fatalf("TransitionRoomStatus() = (%v, %v), want (true, nil)", updated, err)
	}
	assertSQLExpectations(t, mock)
}

func TestGameRepoTransitionRoomStatusRejectsInvalidContractBeforeSQL(t *testing.T) {
	repository, mock := newMockGameRepo(t)

	if updated, err := repository.TransitionRoomStatus(
		context.Background(), 1, 2, nil, model.RoomStatusPaused,
	); updated || !errors.Is(err, ErrEmptySourceStatuses) {
		t.Fatalf("empty source transition = (%v, %v)", updated, err)
	}
	if updated, err := repository.TransitionRoomStatus(
		context.Background(),
		1,
		2,
		[]model.RoomStatus{model.RoomStatusWaiting},
		model.RoomStatus("finished"),
	); updated || !errors.Is(err, ErrInvalidRoomStatus) {
		t.Fatalf("invalid target transition = (%v, %v)", updated, err)
	}
	assertSQLExpectations(t, mock)
}

func TestGameRepoFindRoomByIDAndOwnerIDScopesAccess(t *testing.T) {
	repository, mock := newMockGameRepo(t)
	rows := sqlmock.NewRows([]string{
		"id", "name", "script_id", "owner_id", "status", "max_players",
		"current_turn", "round_number", "turn_order", "is_solo", "created_at", "ended_at",
	}).AddRow(
		41, "古宅独奏", 11, 7, "playing", 1,
		0, 2, []byte(`[7]`), true, time.Now(), nil,
	)
	mock.ExpectQuery("SELECT \\* FROM `game_rooms` WHERE id = \\? AND owner_id = \\?").
		WithArgs(41, 7).
		WillReturnRows(rows)

	room, err := repository.FindRoomByIDAndOwnerID(context.Background(), 41, 7)

	if err != nil {
		t.Fatalf("FindRoomByIDAndOwnerID() error = %v", err)
	}
	if room.ID != 41 || room.OwnerID != 7 || room.RoundNumber != 2 {
		t.Fatalf("room = %#v", room)
	}
	assertSQLExpectations(t, mock)
}

func TestGameRepoFindPlayersByRoomUsesStableTurnOrder(t *testing.T) {
	repository, mock := newMockGameRepo(t)
	rows := sqlmock.NewRows([]string{
		"id", "room_id", "user_id", "character_id", "player_order", "is_ready", "joined_at",
	}).AddRow(1, 41, 7, nil, 0, true, time.Now())
	mock.ExpectQuery(
		"SELECT \\* FROM `room_players` WHERE room_id = \\? ORDER BY player_order ASC, id ASC",
	).WithArgs(41).WillReturnRows(rows)

	players, err := repository.FindPlayersByRoom(context.Background(), 41)

	if err != nil || len(players) != 1 || players[0].UserID != 7 {
		t.Fatalf("FindPlayersByRoom() = (%#v, %v)", players, err)
	}
	assertSQLExpectations(t, mock)
}

func TestGameRepoSaveQueriesRemainRoomScoped(t *testing.T) {
	repository, mock := newMockGameRepo(t)
	createdAt := time.Now()
	columns := []string{
		"id", "room_id", "save_name", "round_number", "summary_memory",
		"redis_snapshot", "recent_messages", "is_auto", "created_at",
	}
	mock.ExpectQuery("SELECT \\* FROM `game_saves` WHERE id = \\? AND room_id = \\?").
		WithArgs(9, 41).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(
			9, 41, "手动存档", 3, "进入书房",
			[]byte(`{"status":"playing"}`), []byte(`[]`), false, createdAt,
		))
	mock.ExpectQuery(
		"SELECT \\* FROM `game_saves` WHERE room_id = \\? ORDER BY created_at DESC, id DESC",
	).WithArgs(41).WillReturnRows(sqlmock.NewRows(columns))
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM `game_saves` WHERE id = \\? AND room_id = \\?").
		WithArgs(9, 41).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	save, err := repository.FindSaveByID(context.Background(), 41, 9)
	if err != nil || save.ID != 9 || save.RoomID != 41 {
		t.Fatalf("FindSaveByID() = (%#v, %v)", save, err)
	}
	saves, err := repository.ListSaves(context.Background(), 41)
	if err != nil || saves == nil || len(saves) != 0 {
		t.Fatalf("ListSaves() = (%#v, %v), want non-nil empty slice", saves, err)
	}
	deleted, err := repository.DeleteSave(context.Background(), 41, 9)
	if err != nil || !deleted {
		t.Fatalf("DeleteSave() = (%v, %v), want (true, nil)", deleted, err)
	}
	assertSQLExpectations(t, mock)
}

func newMockGameRepo(t *testing.T) (*GameRepo, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(
		mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open GORM sqlmock connection: %v", err)
	}
	return NewGameRepo(db), mock
}

func validGameRoom() *model.GameRoom {
	return &model.GameRoom{
		Name:       "古宅独奏",
		ScriptID:   11,
		OwnerID:    7,
		Status:     model.RoomStatusWaiting,
		MaxPlayers: 1,
		TurnOrder:  json.RawMessage(`[7]`),
		IsSolo:     true,
	}
}

func assertSQLExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGameRepoDeleteSaveReturnsFalseWhenNoRowMatches(t *testing.T) {
	repository, mock := newMockGameRepo(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `game_saves` WHERE id = ? AND room_id = ?")).
		WithArgs(9, 41).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	deleted, err := repository.DeleteSave(context.Background(), 41, 9)

	if err != nil || deleted {
		t.Fatalf("DeleteSave() = (%v, %v), want (false, nil)", deleted, err)
	}
	assertSQLExpectations(t, mock)
}
