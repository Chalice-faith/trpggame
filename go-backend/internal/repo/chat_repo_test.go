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

func TestChatRepoListMessagesAfterUsesMembershipAndAscendingSequence(t *testing.T) {
	repository, mock := newMockChatRepo(t)
	now := time.Now().UTC()
	messageColumns := []string{
		"id", "conversation_id", "seq", "sender_id", "client_message_id", "message_type", "content", "metadata", "created_at",
	}
	mock.ExpectQuery("SELECT .* FROM `messages` JOIN conversation_members cm ON cm.conversation_id = messages.conversation_id AND cm.user_id = \\? AND cm.status = \\? WHERE messages.conversation_id = \\? AND messages.seq > \\? ORDER BY messages.seq ASC LIMIT 3").
		WithArgs(uint(7), model.ConversationMemberStatusActive, uint(41), uint64(8)).
		WillReturnRows(sqlmock.NewRows(messageColumns).
			AddRow(91, 41, 9, 8, "550e8400-e29b-41d4-a716-446655440000", "text", "nine", []byte(`{}`), now).
			AddRow(92, 41, 10, 8, "8c21c14d-cf36-4fd2-845d-1496d9c154b2", "text", "ten", []byte(`{}`), now))

	messages, err := repository.ListMessagesAfter(context.Background(), 7, 41, 8, 3)
	if err != nil || len(messages) != 2 || messages[0].Seq != 9 || messages[1].Seq != 10 {
		t.Fatalf("ListMessagesAfter() = (%#v, %v)", messages, err)
	}
	assertSQLExpectations(t, mock)
}

func TestChatRepoListMessagesAfterHidesConversationFromNonMember(t *testing.T) {
	repository, mock := newMockChatRepo(t)
	mock.ExpectQuery("SELECT .* FROM `messages` JOIN conversation_members cm .* ORDER BY messages.seq ASC LIMIT 3").
		WithArgs(uint(7), model.ConversationMemberStatusActive, uint(41), uint64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "conversation_id", "seq", "sender_id", "client_message_id", "message_type", "content", "metadata", "created_at"}))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `conversation_members` WHERE conversation_id = \\? AND user_id = \\? AND status = \\?").
		WithArgs(uint(41), uint(7), model.ConversationMemberStatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	_, err := repository.ListMessagesAfter(context.Background(), 7, 41, 8, 3)
	if !errors.Is(err, ErrChatConversationMissing) {
		t.Fatalf("ListMessagesAfter() error = %v", err)
	}
	assertSQLExpectations(t, mock)
}

func TestChatRepoListActiveConversationViewsLoadsGroupMembersInOneQuery(t *testing.T) {
	repository, mock := newMockChatRepo(t)
	now := time.Now().UTC()
	columns := []string{
		"viewer_id", "id", "type", "direct_low_id", "direct_high_id", "group_id", "last_seq",
		"last_message_at", "created_at", "updated_at", "last_read_seq", "activity_at",
		"group_name", "group_avatar_url", "group_owner_id", "group_version", "group_created_at", "group_updated_at",
		"group_role", "group_member_count", "last_message_id", "last_message_seq", "last_sender_id",
		"last_client_id", "last_message_type", "last_content", "last_metadata", "last_created_at",
	}
	mock.ExpectQuery("SELECT .*me.user_id AS viewer_id.*FROM conversations c.*ORDER BY me.user_id ASC").
		WithArgs(uint(41)).
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow(7, 41, "group", nil, nil, 9, 5, now, now, now, 5, now, "调查局", "", 7, 3, now, now, "owner", 2, 91, 5, 7, "550e8400-e29b-41d4-a716-446655440000", "text", "hello", []byte(`{}`), now).
			AddRow(8, 41, "group", nil, nil, 9, 5, now, now, now, 2, now, "调查局", "", 7, 3, now, now, "member", 2, 91, 5, 7, "550e8400-e29b-41d4-a716-446655440000", "text", "hello", []byte(`{}`), now))

	views, err := repository.ListActiveConversationViews(context.Background(), 41)
	if err != nil || len(views) != 2 {
		t.Fatalf("ListActiveConversationViews() = (%#v, %v)", views, err)
	}
	if views[0].UserID != 7 || views[0].Record.Group == nil || views[0].Record.Group.CurrentUserRole != model.GroupRoleOwner || views[0].Record.LastReadSeq != 5 {
		t.Fatalf("owner view = %#v", views[0])
	}
	if views[1].UserID != 8 || views[1].Record.Group == nil || views[1].Record.Group.CurrentUserRole != model.GroupRoleMember || views[1].Record.LastReadSeq != 2 {
		t.Fatalf("member view = %#v", views[1])
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
