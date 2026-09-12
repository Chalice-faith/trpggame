//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

func TestChatServiceMySQL84Concurrency(t *testing.T) {
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
	sqlDB.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = sqlDB.Close() })
	assertM22Schema(t, db)

	suffix := time.Now().UnixNano()
	users := []model.User{
		{Username: fmt.Sprintf("ca%d", suffix), Email: fmt.Sprintf("ca%d@example.test", suffix), PasswordHash: "integration", Nickname: "Chat A"},
		{Username: fmt.Sprintf("cb%d", suffix), Email: fmt.Sprintf("cb%d@example.test", suffix), PasswordHash: "integration", Nickname: "Chat B"},
		{Username: fmt.Sprintf("cc%d", suffix), Email: fmt.Sprintf("cc%d@example.test", suffix), PasswordHash: "integration", Nickname: "Chat C"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	a, b, c := users[0].ID, users[1].ID, users[2].ID
	low, high := normalizedPair(a, b)
	t.Cleanup(func() {
		var conversationIDs []uint
		db.Model(&model.Conversation{}).Where("direct_low_id = ? AND direct_high_id = ?", low, high).Pluck("id", &conversationIDs)
		if len(conversationIDs) > 0 {
			db.Where("conversation_id IN ?", conversationIDs).Delete(&model.Message{})
			db.Where("conversation_id IN ?", conversationIDs).Delete(&model.ConversationMember{})
			db.Where("id IN ?", conversationIDs).Delete(&model.Conversation{})
		}
		db.Where("user_low_id = ? AND user_high_id = ?", low, high).Delete(&model.Friendship{})
		db.Unscoped().Where("id IN ?", []uint{a, b, c}).Delete(&model.User{})
	})

	userRepo := repo.NewUserRepo(db)
	friendService := NewFriendService(repo.NewFriendRepo(db), userRepo, OfflinePresenceProvider{}, nil)
	request, err := friendService.SendRequest(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := friendService.RespondRequest(context.Background(), b, request.ID, true); err != nil {
		t.Fatal(err)
	}
	chatService := NewChatService(repo.NewChatRepo(db), userRepo)

	t.Run("concurrent direct creation reuses one conversation", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan uint, 16)
		errs := make(chan error, 16)
		var wg sync.WaitGroup
		for index := range 16 {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				current, peer := a, b
				if index%2 == 1 {
					current, peer = b, a
				}
				conversation, err := chatService.CreateDirect(context.Background(), current, peer)
				if err != nil {
					errs <- err
					return
				}
				results <- conversation.ID
			}(index)
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent create: %v", err)
		}
		conversationID := uint(0)
		for id := range results {
			if conversationID == 0 {
				conversationID = id
			}
			if id != conversationID {
				t.Fatalf("conversation IDs differ: %d and %d", conversationID, id)
			}
		}
		var count int64
		db.Model(&model.Conversation{}).Where("direct_low_id = ? AND direct_high_id = ?", low, high).Count(&count)
		if count != 1 {
			t.Fatalf("conversation count = %d", count)
		}
		db.Model(&model.ConversationMember{}).Where("conversation_id = ?", conversationID).Count(&count)
		if count != 2 {
			t.Fatalf("member count = %d", count)
		}
	})

	conversation, err := chatService.CreateDirect(context.Background(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := conversation.ID
	if _, err := chatService.CreateDirect(context.Background(), a, c); !errors.Is(err, ErrFriendshipRequired) {
		t.Fatalf("non-friend direct conversation = %v", err)
	}

	t.Run("concurrent sends allocate continuous sequence", func(t *testing.T) {
		start := make(chan struct{})
		seqs := make(chan uint64, 24)
		errs := make(chan error, 24)
		var wg sync.WaitGroup
		for index := range 24 {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				sender := a
				if index%2 == 1 {
					sender = b
				}
				message, duplicate, err := chatService.SendText(context.Background(), sender, conversationID, uuid.NewString(), fmt.Sprintf("message-%d", index))
				if err != nil {
					errs <- err
					return
				}
				if duplicate {
					errs <- errors.New("new UUID reported duplicate")
					return
				}
				seqs <- message.Seq
			}(index)
		}
		close(start)
		wg.Wait()
		close(seqs)
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent send: %v", err)
		}
		actual := make([]int, 0, 24)
		for seq := range seqs {
			actual = append(actual, int(seq))
		}
		sort.Ints(actual)
		for index, seq := range actual {
			if seq != index+1 {
				t.Fatalf("sequences = %v", actual)
			}
		}
		var stored model.Conversation
		if err := db.First(&stored, conversationID).Error; err != nil || stored.LastSeq != 24 {
			t.Fatalf("stored conversation = %#v, err=%v", stored, err)
		}
	})

	t.Run("same client ID is persisted once", func(t *testing.T) {
		clientID := uuid.NewString()
		start := make(chan struct{})
		results := make(chan repo.SendMessageResult, 12)
		errs := make(chan error, 12)
		var wg sync.WaitGroup
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				message, duplicate, err := chatService.SendText(context.Background(), a, conversationID, clientID, "idempotent")
				if err != nil {
					errs <- err
					return
				}
				results <- repo.SendMessageResult{Message: model.Message{ID: message.ID, Seq: message.Seq}, Duplicate: duplicate}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			t.Fatalf("duplicate send: %v", err)
		}
		firstID, firstSeq := uint(0), uint64(0)
		newCount := 0
		for result := range results {
			if firstID == 0 {
				firstID, firstSeq = result.Message.ID, result.Message.Seq
			}
			if result.Message.ID != firstID || result.Message.Seq != firstSeq {
				t.Fatalf("duplicate result differs: %#v", result)
			}
			if !result.Duplicate {
				newCount++
			}
		}
		if newCount != 1 || firstSeq != 25 {
			t.Fatalf("newCount=%d seq=%d", newCount, firstSeq)
		}
		var count int64
		db.Model(&model.Message{}).Where("conversation_id = ? AND sender_id = ? AND client_message_id = ?", conversationID, a, clientID).Count(&count)
		if count != 1 {
			t.Fatalf("idempotent message count = %d", count)
		}
		if _, _, err := chatService.SendText(context.Background(), a, conversationID, clientID, "different"); !errors.Is(err, ErrInvalidChatMessage) {
			t.Fatalf("reused client ID with different content = %v", err)
		}
	})

	t.Run("history and read watermarks remain available after unfriend", func(t *testing.T) {
		if _, err := chatService.ListMessages(context.Background(), c, conversationID, 0, 100); !errors.Is(err, ErrConversationNotFound) {
			t.Fatalf("non-member history = %v", err)
		}
		if _, err := chatService.MarkRead(context.Background(), c, conversationID, 1); !errors.Is(err, ErrConversationNotFound) {
			t.Fatalf("non-member read = %v", err)
		}
		page, err := chatService.ListMessages(context.Background(), b, conversationID, 0, 100)
		if err != nil || len(page.Items) != 25 || page.Items[0].Seq != 1 || page.Items[24].Seq != 25 {
			t.Fatalf("history = %#v, err=%v", page, err)
		}
		read, err := chatService.MarkRead(context.Background(), b, conversationID, 25)
		if err != nil || read.LastReadSeq != 25 || read.UnreadCount != 0 {
			t.Fatalf("read = %#v, err=%v", read, err)
		}
		read, err = chatService.MarkRead(context.Background(), b, conversationID, 10)
		if err != nil || read.LastReadSeq != 25 {
			t.Fatalf("monotonic read = %#v, err=%v", read, err)
		}
		if _, err := chatService.MarkRead(context.Background(), b, conversationID, 26); !errors.Is(err, ErrReadSequenceConflict) {
			t.Fatalf("read conflict = %v", err)
		}
		if err := friendService.DeleteFriend(context.Background(), a, b); err != nil {
			t.Fatal(err)
		}
		conversations, err := chatService.ListConversations(context.Background(), a, "", 20)
		if err != nil || len(conversations.Items) != 1 || conversations.Items[0].CanSend {
			t.Fatalf("unfriended conversation = %#v, err=%v", conversations, err)
		}
		if _, err := chatService.ListMessages(context.Background(), a, conversationID, 0, 100); err != nil {
			t.Fatalf("history after unfriend: %v", err)
		}
		if _, _, err := chatService.SendText(context.Background(), a, conversationID, uuid.NewString(), "blocked"); !errors.Is(err, ErrFriendshipRequired) {
			t.Fatalf("send after unfriend = %v", err)
		}
		reapply, err := friendService.SendRequest(context.Background(), a, b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := friendService.RespondRequest(context.Background(), b, reapply.ID, true); err != nil {
			t.Fatal(err)
		}
		reused, err := chatService.CreateDirect(context.Background(), b, a)
		if err != nil || reused.ID != conversationID || !reused.CanSend {
			t.Fatalf("reused conversation = %#v, err=%v", reused, err)
		}
	})
}

func assertM22Schema(t *testing.T, db *gorm.DB) {
	t.Helper()
	type indexDefinition struct {
		TableName   string
		IndexName   string
		ColumnNames string
		NonUnique   int
	}
	var indexes []indexDefinition
	err := db.Raw(`SELECT TABLE_NAME AS table_name, INDEX_NAME AS index_name,
 GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',') AS column_names,
 NON_UNIQUE AS non_unique
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('conversations', 'conversation_members', 'messages')
GROUP BY TABLE_NAME, INDEX_NAME, NON_UNIQUE`).Scan(&indexes).Error
	if err != nil {
		t.Fatalf("query M2.2 indexes: %v", err)
	}
	actual := make(map[string]indexDefinition, len(indexes))
	for _, index := range indexes {
		actual[index.TableName+"."+index.IndexName] = index
	}
	expected := map[string]struct {
		columns   string
		nonUnique int
	}{
		"conversations.uk_conversations_direct_pair":                             {"direct_low_id,direct_high_id", 0},
		"conversations.idx_conversations_activity":                               {"last_message_at,id", 1},
		"conversation_members.uk_conversation_members_conversation_user":         {"conversation_id,user_id", 0},
		"conversation_members.idx_conversation_members_user_status_conversation": {"user_id,status,conversation_id", 1},
		"messages.uk_messages_conversation_seq":                                  {"conversation_id,seq", 0},
		"messages.uk_messages_client_id":                                         {"conversation_id,sender_id,client_message_id", 0},
	}
	for key, want := range expected {
		index, ok := actual[key]
		if !ok || index.ColumnNames != want.columns || index.NonUnique != want.nonUnique {
			t.Fatalf("index %s = %#v, want columns=%q non_unique=%d", key, index, want.columns, want.nonUnique)
		}
	}

	type columnDefinition struct {
		ColumnName       string
		ColumnType       string
		DataType         string
		CharacterSetName *string
		CollationName    *string
	}
	var columns []columnDefinition
	if err := db.Raw(`SELECT COLUMN_NAME AS column_name, COLUMN_TYPE AS column_type,
 DATA_TYPE AS data_type, CHARACTER_SET_NAME AS character_set_name, COLLATION_NAME AS collation_name
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'messages'
 AND COLUMN_NAME IN ('seq', 'client_message_id', 'metadata')`).Scan(&columns).Error; err != nil {
		t.Fatalf("query M2.2 columns: %v", err)
	}
	byName := make(map[string]columnDefinition, len(columns))
	for _, column := range columns {
		byName[column.ColumnName] = column
	}
	clientID := byName["client_message_id"]
	if clientID.ColumnType != "char(36)" || clientID.CharacterSetName == nil || *clientID.CharacterSetName != "ascii" || clientID.CollationName == nil || *clientID.CollationName != "ascii_bin" {
		t.Fatalf("client_message_id definition = %#v", clientID)
	}
	if seq := byName["seq"]; seq.ColumnType != "bigint unsigned" {
		t.Fatalf("seq definition = %#v", seq)
	}
	if metadata := byName["metadata"]; metadata.DataType != "json" {
		t.Fatalf("metadata definition = %#v", metadata)
	}
}
