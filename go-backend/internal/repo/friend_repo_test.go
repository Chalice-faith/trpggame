package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"trpggame/internal/model"
)

var friendshipColumns = []string{
	"id", "user_low_id", "user_high_id", "requested_by", "status", "created_at", "updated_at", "responded_at", "removed_at",
}

func TestFriendRepoMutatePairLocksUpdatesAndCommits(t *testing.T) {
	repository, mock := newMockFriendRepo(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM `friendships` WHERE .*user_low_id.*user_high_id.*FOR UPDATE").
		WithArgs(uint(1), uint(2)).
		WillReturnRows(sqlmock.NewRows(friendshipColumns).AddRow(9, 1, 2, 1, "pending", now, now, nil, nil))
	mock.ExpectExec("UPDATE `friendships` SET .* WHERE `id` = \\?").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	row, changed, err := repository.MutatePair(context.Background(), 1, 2, nil, func(friendship *model.Friendship) (bool, error) {
		friendship.Status = model.FriendshipStatusAccepted
		friendship.RespondedAt = &now
		return true, nil
	})
	if err != nil || !changed || row.Status != model.FriendshipStatusAccepted {
		t.Fatalf("MutatePair() = (%#v, %v, %v)", row, changed, err)
	}
	assertSQLExpectations(t, mock)
}

func TestFriendRepoMutateByIDRollsBackCallbackError(t *testing.T) {
	repository, mock := newMockFriendRepo(t)
	now := time.Now().UTC()
	wantErr := errors.New("state conflict")
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM `friendships` WHERE `friendships`.`id` = \\? .*FOR UPDATE").
		WithArgs(uint(9)).
		WillReturnRows(sqlmock.NewRows(friendshipColumns).AddRow(9, 1, 2, 1, "pending", now, now, nil, nil))
	mock.ExpectRollback()

	_, changed, err := repository.MutateByID(context.Background(), 9, func(*model.Friendship) (bool, error) {
		return false, wantErr
	})
	if changed || !errors.Is(err, wantErr) {
		t.Fatalf("MutateByID() = (changed=%v, err=%v)", changed, err)
	}
	assertSQLExpectations(t, mock)
}

func TestFriendRepoMutatePairUsesIdempotentInsert(t *testing.T) {
	repository, mock := newMockFriendRepo(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO `friendships` .*ON DUPLICATE KEY UPDATE").
		WillReturnResult(sqlmock.NewResult(12, 1))
	mock.ExpectQuery("SELECT .* FROM `friendships` WHERE .*user_low_id.*user_high_id.*FOR UPDATE").
		WithArgs(uint(1), uint(2)).
		WillReturnRows(sqlmock.NewRows(friendshipColumns).AddRow(12, 1, 2, 1, "pending", now, now, nil, nil))
	mock.ExpectCommit()

	row, changed, err := repository.MutatePair(context.Background(), 1, 2, &model.Friendship{
		UserLowID: 1, UserHighID: 2, RequestedBy: 1, Status: model.FriendshipStatusPending,
	}, nil)
	if err != nil || !changed || row.ID != 12 {
		t.Fatalf("MutatePair() = (%#v, %v, %v)", row, changed, err)
	}
	assertSQLExpectations(t, mock)
}

func TestFriendRepoListRequestsScopesDirectionStatusAndCursor(t *testing.T) {
	repository, mock := newMockFriendRepo(t)
	mock.ExpectQuery("SELECT .* FROM `friendships` WHERE status = \\? AND \\(requested_by <> \\? AND \\(user_low_id = \\? OR user_high_id = \\?\\)\\) AND id < \\? ORDER BY id DESC LIMIT 21").
		WithArgs(model.FriendshipStatusPending, uint(7), uint(7), uint(7), uint(50)).
		WillReturnRows(sqlmock.NewRows(friendshipColumns))
	rows, err := repository.ListRequests(context.Background(), 7, true, model.FriendshipStatusPending, 50, 21)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ListRequests() = (%#v, %v)", rows, err)
	}
	assertSQLExpectations(t, mock)
}

func TestFriendRepoListFriendsOnlyReturnsAcceptedParticipantRows(t *testing.T) {
	repository, mock := newMockFriendRepo(t)
	mock.ExpectQuery("SELECT .* FROM `friendships` WHERE status = \\? AND \\(user_low_id = \\? OR user_high_id = \\?\\) ORDER BY id DESC LIMIT 51").
		WithArgs(model.FriendshipStatusAccepted, uint(7), uint(7)).
		WillReturnRows(sqlmock.NewRows(friendshipColumns))
	rows, err := repository.ListFriends(context.Background(), 7, 0, 51)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ListFriends() = (%#v, %v)", rows, err)
	}
	assertSQLExpectations(t, mock)
}

func TestFriendRepoListsAcceptedPeerIDsForPresenceAuthorization(t *testing.T) {
	repository, mock := newMockFriendRepo(t)
	mock.ExpectQuery("SELECT CASE WHEN user_low_id = \\? THEN user_high_id ELSE user_low_id END FROM `friendships` WHERE status = \\? AND \\(user_low_id = \\? OR user_high_id = \\?\\) ORDER BY id").
		WithArgs(uint(7), model.FriendshipStatusAccepted, uint(7), uint(7)).
		WillReturnRows(sqlmock.NewRows([]string{"peer_id"}).AddRow(8).AddRow(9))
	peerIDs, err := repository.ListAcceptedPeerIDs(context.Background(), 7)
	if err != nil || len(peerIDs) != 2 || peerIDs[0] != 8 || peerIDs[1] != 9 {
		t.Fatalf("ListAcceptedPeerIDs() = (%#v, %v)", peerIDs, err)
	}
	assertSQLExpectations(t, mock)
}

func newMockFriendRepo(t *testing.T) (*FriendRepo, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewFriendRepo(db), mock
}
