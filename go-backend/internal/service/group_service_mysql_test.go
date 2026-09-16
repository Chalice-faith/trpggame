//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

func TestGroupServiceMySQL84Lifecycle(t *testing.T) {
	dsn := os.Getenv("TRPG_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TRPG_TEST_MYSQL_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect mysql: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, table := range []string{"groups", "group_members", "conversations", "conversation_members", "messages"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("required table %s is missing", table)
		}
	}

	suffix := time.Now().UnixNano()
	users := []model.User{
		{Username: fmt.Sprintf("go%d", suffix), Email: fmt.Sprintf("go%d@example.test", suffix), PasswordHash: "integration"},
		{Username: fmt.Sprintf("ga%d", suffix), Email: fmt.Sprintf("ga%d@example.test", suffix), PasswordHash: "integration"},
		{Username: fmt.Sprintf("gm%d", suffix), Email: fmt.Sprintf("gm%d@example.test", suffix), PasswordHash: "integration"},
		{Username: fmt.Sprintf("gx%d", suffix), Email: fmt.Sprintf("gx%d@example.test", suffix), PasswordHash: "integration"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	owner, admin, member, extra := users[0].ID, users[1].ID, users[2].ID, users[3].ID
	friendships := []model.Friendship{
		acceptedFriendship(owner, admin), acceptedFriendship(owner, member), acceptedFriendship(admin, extra),
	}
	if err := db.Create(&friendships).Error; err != nil {
		t.Fatal(err)
	}
	var groupID, conversationID uint
	t.Cleanup(func() {
		if conversationID > 0 {
			db.Where("conversation_id = ?", conversationID).Delete(&model.Message{})
			db.Where("conversation_id = ?", conversationID).Delete(&model.ConversationMember{})
			db.Delete(&model.Conversation{}, conversationID)
		}
		if groupID > 0 {
			db.Where("group_id = ?", groupID).Delete(&model.GroupMember{})
			db.Delete(&model.Group{}, groupID)
		}
		db.Where("id IN ?", []uint{friendships[0].ID, friendships[1].ID, friendships[2].ID}).Delete(&model.Friendship{})
		db.Unscoped().Where("id IN ?", []uint{owner, admin, member, extra}).Delete(&model.User{})
	})

	groupService := NewGroupService(repo.NewGroupRepo(db))
	chatRepo := repo.NewChatRepo(db)
	chatService := NewChatService(chatRepo, repo.NewUserRepo(db))
	created, err := groupService.Create(context.Background(), owner, "Integration Group", "")
	if err != nil || created.Version != 1 || created.MemberCount != 1 {
		t.Fatalf("create = (%#v, %v)", created, err)
	}
	groupID, conversationID = created.ID, created.ConversationID

	invited, err := groupService.Invite(context.Background(), owner, groupID, []uint{admin, member}, 1)
	if err != nil || invited.Version != 2 || invited.MemberCount != 3 {
		t.Fatalf("invite = (%#v, %v)", invited, err)
	}
	promoted, err := groupService.SetRole(context.Background(), owner, groupID, admin, model.GroupRoleAdmin, 2)
	if err != nil || promoted.Version != 3 {
		t.Fatalf("promote = (%#v, %v)", promoted, err)
	}
	if _, err := groupService.Invite(context.Background(), admin, groupID, []uint{extra}, 3); err != nil {
		t.Fatalf("admin invite: %v", err)
	}
	groupPage, err := groupService.List(context.Background(), extra, "", 20)
	if err != nil || len(groupPage.Items) != 1 || groupPage.Items[0].ID != groupID || groupPage.Items[0].MemberCount != 4 {
		t.Fatalf("group list = (%#v, %v)", groupPage, err)
	}
	memberPage, err := groupService.ListMembers(context.Background(), extra, groupID, "", 20)
	if err != nil || len(memberPage.Items) != 4 {
		t.Fatalf("member list = (%#v, %v)", memberPage, err)
	}
	if _, err := groupService.Update(context.Background(), member, groupID, 4, stringPointer("forbidden"), nil); !errors.Is(err, ErrGroupPermissionDenied) {
		t.Fatalf("member update = %v", err)
	}
	if _, err := groupService.Update(context.Background(), owner, groupID, 3, stringPointer("stale"), nil); !errors.Is(err, ErrGroupVersionConflict) {
		t.Fatalf("stale update = %v", err)
	}

	if err := db.Model(&model.Friendship{}).Where("id = ?", friendships[2].ID).Update("status", model.FriendshipStatusRemoved).Error; err != nil {
		t.Fatal(err)
	}
	clientMessageID := uuid.NewString()
	message, duplicate, err := chatService.SendText(context.Background(), extra, conversationID, clientMessageID, "group hello")
	if err != nil || duplicate || message.Seq != 6 {
		t.Fatalf("group send = (%#v, %v, %v)", message, duplicate, err)
	}
	repeated, duplicate, err := chatService.SendText(context.Background(), extra, conversationID, clientMessageID, "group hello")
	if err != nil || !duplicate || repeated.ID != message.ID || repeated.Seq != 6 {
		t.Fatalf("duplicate group send = (%#v, %v, %v)", repeated, duplicate, err)
	}
	synced, hasMore, err := chatService.ListMessagesSince(context.Background(), extra, conversationID, 4, 100)
	if err != nil || hasMore || len(synced) != 2 || synced[0].Seq != 5 || synced[1].Seq != 6 {
		t.Fatalf("group sync = (%#v, %v, %v)", synced, hasMore, err)
	}
	views, err := chatRepo.ListActiveConversationViews(context.Background(), conversationID)
	if err != nil || len(views) != 4 {
		t.Fatalf("group conversation views = (%#v, %v)", views, err)
	}
	for _, view := range views {
		if view.Record.Group == nil || view.Record.Group.MemberCount != 4 || view.Record.Conversation.LastSeq != 6 {
			t.Fatalf("invalid group conversation view = %#v", view)
		}
	}
	history, err := chatService.ListMessages(context.Background(), extra, conversationID, 0, 20)
	if err != nil || len(history.Items) != 6 || history.Items[0].Seq != 1 {
		t.Fatalf("full history = (%#v, %v)", history, err)
	}
	conversationPage, err := chatService.ListConversations(context.Background(), extra, "", 20)
	if err != nil || len(conversationPage.Items) != 1 || conversationPage.Items[0].Group == nil ||
		conversationPage.Items[0].Group.ID != groupID || conversationPage.Items[0].Peer != nil || !conversationPage.Items[0].CanSend {
		t.Fatalf("group conversation list = (%#v, %v)", conversationPage, err)
	}

	removed, err := groupService.Remove(context.Background(), admin, groupID, member, 4)
	if err != nil || removed.Version != 5 || removed.MemberCount != 3 {
		t.Fatalf("remove = (%#v, %v)", removed, err)
	}
	if _, err := chatService.ListMessages(context.Background(), member, conversationID, 0, 20); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("removed history = %v", err)
	}
	if _, _, err := chatService.SendText(context.Background(), member, conversationID, uuid.NewString(), "forbidden"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("removed send = %v", err)
	}
	if _, _, err := chatService.ListMessagesSince(context.Background(), member, conversationID, 0, 100); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("removed sync = %v", err)
	}
	views, err = chatRepo.ListActiveConversationViews(context.Background(), conversationID)
	if err != nil || len(views) != 3 {
		t.Fatalf("post-removal group conversation views = (%#v, %v)", views, err)
	}
	for _, view := range views {
		if view.UserID == member {
			t.Fatalf("removed member remained in active views: %#v", views)
		}
	}

	transferred, err := groupService.Transfer(context.Background(), owner, groupID, admin, 5)
	if err != nil || transferred.Version != 6 || transferred.OwnerID != admin {
		t.Fatalf("transfer = (%#v, %v)", transferred, err)
	}
	left, err := groupService.Remove(context.Background(), owner, groupID, owner, 6)
	if err != nil || left.Version != 7 || left.CurrentUserRole != nil {
		t.Fatalf("old owner leave = (%#v, %v)", left, err)
	}
	if _, err := groupService.Get(context.Background(), owner, groupID); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("left owner get = %v", err)
	}

	assertGroupMembershipMirrored(t, db, groupID, conversationID)
}

func acceptedFriendship(left, right uint) model.Friendship {
	low, high := normalizedPair(left, right)
	return model.Friendship{UserLowID: low, UserHighID: high, RequestedBy: left, Status: model.FriendshipStatusAccepted}
}

func stringPointer(value string) *string { return &value }

func assertGroupMembershipMirrored(t *testing.T, db *gorm.DB, groupID, conversationID uint) {
	t.Helper()
	var groupMembers []model.GroupMember
	var conversationMembers []model.ConversationMember
	if err := db.Where("group_id = ?", groupID).Order("user_id").Find(&groupMembers).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("conversation_id = ?", conversationID).Order("user_id").Find(&conversationMembers).Error; err != nil {
		t.Fatal(err)
	}
	if len(groupMembers) != len(conversationMembers) {
		t.Fatalf("membership rows differ: %d != %d", len(groupMembers), len(conversationMembers))
	}
	for index := range groupMembers {
		groupMember, conversationMember := groupMembers[index], conversationMembers[index]
		if groupMember.UserID != conversationMember.UserID || string(groupMember.Role) != string(conversationMember.Role) || string(groupMember.Status) != string(conversationMember.Status) {
			t.Fatalf("membership drift: %#v != %#v", groupMember, conversationMember)
		}
	}
}
