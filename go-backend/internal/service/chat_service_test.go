package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type chatRepoStub struct {
	conversation model.Conversation
	record       repo.ConversationRecord
	rows         []repo.ConversationRecord
	messages     []model.Message
	markCurrent  uint64
	markLast     uint64
	sendResult   repo.SendMessageResult
	err          error
	low          uint
	high         uint
	before       time.Time
	beforeID     uint
	limit        int
	requested    uint64
	sentContent  string
}

func (s *chatRepoStub) EnsureDirectConversation(_ context.Context, low, high uint) (*model.Conversation, error) {
	s.low, s.high = low, high
	return &s.conversation, s.err
}
func (s *chatRepoStub) GetConversation(context.Context, uint, uint) (*repo.ConversationRecord, error) {
	return &s.record, s.err
}
func (s *chatRepoStub) ListConversations(_ context.Context, _ uint, before time.Time, beforeID uint, limit int) ([]repo.ConversationRecord, error) {
	s.before, s.beforeID, s.limit = before, beforeID, limit
	return append([]repo.ConversationRecord(nil), s.rows...), s.err
}
func (s *chatRepoStub) ListMessages(_ context.Context, _, _ uint, _ uint64, limit int) ([]model.Message, error) {
	s.limit = limit
	return append([]model.Message(nil), s.messages...), s.err
}
func (s *chatRepoStub) MarkRead(_ context.Context, _, _ uint, requested uint64) (uint64, uint64, error) {
	s.requested = requested
	return s.markCurrent, s.markLast, s.err
}
func (s *chatRepoStub) SendMessage(_ context.Context, _, _ uint, _, content string, _ time.Time) (*repo.SendMessageResult, error) {
	s.sentContent = content
	return &s.sendResult, s.err
}

type chatUsersStub struct {
	users map[uint]model.User
	err   error
}

func (s chatUsersStub) FindActiveByID(_ context.Context, id uint) (*model.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	user, ok := s.users[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}
func (s chatUsersStub) FindActiveByIDs(_ context.Context, ids []uint) ([]model.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	result := make([]model.User, 0, len(ids))
	for _, id := range ids {
		if user, ok := s.users[id]; ok {
			result = append(result, user)
		}
	}
	return result, nil
}

func TestChatServiceCreateDirectNormalizesPairAndReturnsSummary(t *testing.T) {
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	low, high := uint(2), uint(9)
	repository := &chatRepoStub{
		conversation: model.Conversation{ID: 41},
		record: repo.ConversationRecord{
			Conversation: model.Conversation{ID: 41, Type: model.ConversationTypeDirect, DirectLowID: &low, DirectHighID: &high, CreatedAt: now, UpdatedAt: now},
			ActivityAt:   now, Peer: model.User{ID: 2, Username: "alice"}, FriendshipStatus: model.FriendshipStatusAccepted,
			LastMessage: &model.Message{ID: 3, ConversationID: 41, Seq: 1, SenderID: 2, ClientMessageID: "550e8400-e29b-41d4-a716-446655440000", MessageType: model.MessageTypeText, Content: "hello", Metadata: []byte(`{}`), CreatedAt: now},
		},
	}
	svc := NewChatService(repository, chatUsersStub{users: map[uint]model.User{2: {ID: 2, Username: "alice"}}})
	result, err := svc.CreateDirect(context.Background(), 9, 2)
	if err != nil || result.ID != 41 || !result.CanSend || result.Peer.ID != 2 || result.LastMessage == nil || result.LastMessage.Sender == nil {
		t.Fatalf("CreateDirect() = (%#v, %v)", result, err)
	}
	if repository.low != 2 || repository.high != 9 {
		t.Fatalf("normalized pair = (%d,%d)", repository.low, repository.high)
	}
}

func TestChatServiceCreateDirectHidesMissingPeerAsFriendshipRequired(t *testing.T) {
	svc := NewChatService(&chatRepoStub{}, chatUsersStub{users: map[uint]model.User{}})
	_, err := svc.CreateDirect(context.Background(), 1, 2)
	if !errors.Is(err, ErrFriendshipRequired) {
		t.Fatalf("error = %v", err)
	}
}

func TestChatServiceConversationCursorAndUnreadSemantics(t *testing.T) {
	firstTime := time.Date(2026, 9, 12, 3, 2, 1, 123000000, time.UTC)
	secondTime := firstTime.Add(-time.Minute)
	peer := model.User{ID: 2, Username: "bob", Nickname: "Bob"}
	repository := &chatRepoStub{rows: []repo.ConversationRecord{
		{Conversation: model.Conversation{ID: 9, Type: model.ConversationTypeDirect, LastSeq: 8, CreatedAt: firstTime, UpdatedAt: firstTime}, LastReadSeq: 5, ActivityAt: firstTime, Peer: peer, FriendshipStatus: model.FriendshipStatusAccepted},
		{Conversation: model.Conversation{ID: 8, Type: model.ConversationTypeDirect, LastSeq: 3, CreatedAt: secondTime, UpdatedAt: secondTime}, LastReadSeq: 9, ActivityAt: secondTime, Peer: peer, FriendshipStatus: model.FriendshipStatusRemoved},
	}}
	svc := NewChatService(repository, chatUsersStub{users: map[uint]model.User{2: peer}})
	page, err := svc.ListConversations(context.Background(), 1, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].UnreadCount != 3 || !page.Items[0].CanSend || page.NextCursor == "" {
		t.Fatalf("page = %#v, err=%v", page, err)
	}
	_, err = svc.ListConversations(context.Background(), 1, page.NextCursor, 1)
	if err != nil || !repository.before.Equal(firstTime) || repository.beforeID != 9 {
		t.Fatalf("decoded cursor = (%s,%d), err=%v", repository.before, repository.beforeID, err)
	}
	if _, err := svc.ListConversations(context.Background(), 1, "not-a-cursor", 20); !errors.Is(err, ErrInvalidConversationRequest) {
		t.Fatalf("invalid cursor error = %v", err)
	}
}

func TestChatServiceHistoryReturnsAscendingPageAndNextBoundary(t *testing.T) {
	now := time.Now().UTC()
	repository := &chatRepoStub{messages: []model.Message{
		{ID: 5, ConversationID: 41, Seq: 5, SenderID: 1, Metadata: []byte(`{}`), CreatedAt: now},
		{ID: 4, ConversationID: 41, Seq: 4, SenderID: 2, Metadata: []byte(`{}`), CreatedAt: now},
		{ID: 3, ConversationID: 41, Seq: 3, SenderID: 1, Metadata: []byte(`{}`), CreatedAt: now},
	}}
	users := chatUsersStub{users: map[uint]model.User{1: {ID: 1}, 2: {ID: 2}}}
	page, err := NewChatService(repository, users).ListMessages(context.Background(), 1, 41, 0, 2)
	if err != nil || !page.HasMore || page.NextBeforeSeq != 4 || len(page.Items) != 2 || page.Items[0].Seq != 4 || page.Items[1].Seq != 5 {
		t.Fatalf("page = %#v, err=%v", page, err)
	}
	if repository.limit != 3 {
		t.Fatalf("repository limit = %d", repository.limit)
	}
}

func TestChatServiceMarkReadIsMappedAndComputesUnread(t *testing.T) {
	repository := &chatRepoStub{markCurrent: 7, markLast: 10}
	result, err := NewChatService(repository, chatUsersStub{}).MarkRead(context.Background(), 1, 41, 7)
	if err != nil || result.LastReadSeq != 7 || result.UnreadCount != 3 || repository.requested != 7 {
		t.Fatalf("MarkRead() = (%#v, %v)", result, err)
	}
	repository.err = repo.ErrChatReadConflict
	if _, err := NewChatService(repository, chatUsersStub{}).MarkRead(context.Background(), 1, 41, 11); !errors.Is(err, ErrReadSequenceConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestChatServiceSendTextValidatesAndPreservesContent(t *testing.T) {
	now := time.Now().UTC()
	repository := &chatRepoStub{sendResult: repo.SendMessageResult{Message: model.Message{
		ID: 1, ConversationID: 41, Seq: 1, SenderID: 1, ClientMessageID: "550e8400-e29b-41d4-a716-446655440000",
		MessageType: model.MessageTypeText, Content: "  hello\n", Metadata: []byte(`{}`), CreatedAt: now,
	}, Duplicate: true}}
	svc := NewChatService(repository, chatUsersStub{users: map[uint]model.User{1: {ID: 1, Username: "alice"}}})
	item, duplicate, err := svc.SendText(context.Background(), 1, 41, "550e8400-e29b-41d4-a716-446655440000", "  hello\n")
	if err != nil || !duplicate || item.Seq != 1 || repository.sentContent != "  hello\n" || item.Sender == nil {
		t.Fatalf("SendText() = (%#v, %v, %v)", item, duplicate, err)
	}
	encoded, err := json.Marshal(item)
	if err != nil || !json.Valid(encoded) || !strings.Contains(string(encoded), `"metadata":{}`) {
		t.Fatalf("message JSON = %s, err=%v", encoded, err)
	}
	for _, content := range []string{"", " \n\t", "bad\x00value", string(make([]rune, 4001))} {
		if _, _, err := svc.SendText(context.Background(), 1, 41, "550e8400-e29b-41d4-a716-446655440000", content); !errors.Is(err, ErrInvalidMessageContent) {
			t.Fatalf("content %q error = %v", content, err)
		}
	}
	if _, _, err := svc.SendText(context.Background(), 1, 41, "NOT-A-UUID", "hello"); !errors.Is(err, ErrInvalidChatMessage) {
		t.Fatalf("UUID error = %v", err)
	}
	repository.err = repo.ErrChatIdempotencyConflict
	if _, _, err := svc.SendText(context.Background(), 1, 41, "550e8400-e29b-41d4-a716-446655440000", "different"); !errors.Is(err, ErrInvalidChatMessage) {
		t.Fatalf("idempotency conflict = %v", err)
	}
}
