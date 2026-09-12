package repo

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"trpggame/internal/model"
)

var conversationColumns = []string{
	"id", "type", "direct_low_id", "direct_high_id", "group_id", "last_seq", "last_message_at", "created_at", "updated_at",
}

func TestChatRepoEnsureDirectLocksFriendshipAndCreatesStableMembers(t *testing.T) {
	repository, mock := newMockChatRepo(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM `friendships` WHERE .*user_low_id.*user_high_id.*status.*FOR UPDATE").
		WithArgs(uint(1), uint(2), model.FriendshipStatusAccepted).
		WillReturnRows(sqlmock.NewRows(friendshipColumns).AddRow(5, 1, 2, 1, "accepted", now, now, now, nil))
	mock.ExpectExec("INSERT INTO `conversations` .*ON DUPLICATE KEY UPDATE").
		WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectQuery("SELECT .* FROM `conversations` WHERE .*direct_low_id.*direct_high_id.*FOR UPDATE").
		WithArgs(uint(1), uint(2)).
		WillReturnRows(sqlmock.NewRows(conversationColumns).AddRow(41, "direct", 1, 2, nil, 0, nil, now, now))
	mock.ExpectExec("INSERT INTO `conversation_members` .*ON DUPLICATE KEY UPDATE").
		WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec("INSERT INTO `conversation_members` .*ON DUPLICATE KEY UPDATE").
		WillReturnResult(sqlmock.NewResult(52, 1))
	mock.ExpectCommit()

	conversation, err := repository.EnsureDirectConversation(context.Background(), 1, 2)
	if err != nil || conversation.ID != 41 {
		t.Fatalf("EnsureDirectConversation() = (%#v, %v)", conversation, err)
	}
	assertSQLExpectations(t, mock)
}

func TestChatRepoMarkReadUsesLockedMonotonicUpdate(t *testing.T) {
	repository, mock := newMockChatRepo(t)
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM `conversations` WHERE `conversations`.`id` = \\? .*FOR UPDATE").
		WithArgs(uint(41)).
		WillReturnRows(sqlmock.NewRows(conversationColumns).AddRow(41, "direct", 1, 2, nil, 10, now, now, now))
	mock.ExpectQuery("SELECT .* FROM `conversation_members` WHERE .*conversation_id.*user_id.*status.*FOR UPDATE").
		WithArgs(uint(41), uint(7), model.ConversationMemberStatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"id", "conversation_id", "user_id", "role", "status", "last_read_seq", "joined_at", "left_at"}).
			AddRow(61, 41, 7, "member", "active", 5, now, nil))
	mock.ExpectExec("UPDATE `conversation_members` SET `last_read_seq`=GREATEST\\(last_read_seq, \\?\\) WHERE id = \\?").
		WithArgs(uint64(8), uint(61)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	current, last, err := repository.MarkRead(context.Background(), 7, 41, 8)
	if err != nil || current != 8 || last != 10 {
		t.Fatalf("MarkRead() = (%d,%d,%v)", current, last, err)
	}
	assertSQLExpectations(t, mock)
}

func newMockChatRepo(t *testing.T) (*ChatRepo, sqlmock.Sqlmock) {
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
	return NewChatRepo(db), mock
}
