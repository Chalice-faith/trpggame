package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"trpggame/internal/imws"
	"trpggame/internal/model"
	"trpggame/internal/realtime"
	"trpggame/internal/repo"
)

type publishedEvent struct {
	userID    uint
	eventType string
	data      any
}

type eventRecorder struct {
	mu     sync.Mutex
	events []publishedEvent
}

func (r *eventRecorder) PublishUserEvent(userID uint, eventType string, data any) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, publishedEvent{userID: userID, eventType: eventType, data: data})
	return true
}

func (r *eventRecorder) all() []publishedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]publishedEvent(nil), r.events...)
}

// realtimeConvStub 在 chatRepoStub 之上按接收者返回不同的会话视角，
// 以验证 conversation_updated 的 per-recipient 语义。
type realtimeConvStub struct {
	chatRepoStub
	records map[uint]repo.ConversationRecord
}

func (s *realtimeConvStub) GetConversation(_ context.Context, userID, _ uint) (*repo.ConversationRecord, error) {
	record, ok := s.records[userID]
	if !ok {
		return nil, repo.ErrChatConversationMissing
	}
	return &record, nil
}

func realtimeConversationFixture(low, high uint, lastSeq uint64, senderRead uint64, peerRead uint64) map[uint]repo.ConversationRecord {
	lowID, highID := low, high
	activity := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	build := func(viewer, peer uint, lastRead uint64) repo.ConversationRecord {
		return repo.ConversationRecord{
			Conversation: model.Conversation{
				ID: 41, Type: model.ConversationTypeDirect, DirectLowID: &lowID, DirectHighID: &highID,
				LastSeq: lastSeq, LastMessageAt: &activity, CreatedAt: activity, UpdatedAt: activity,
			},
			LastReadSeq: lastRead, ActivityAt: activity,
			Peer:             model.User{ID: peer, Username: "peer", Nickname: "Peer"},
			FriendshipStatus: model.FriendshipStatusAccepted,
		}
	}
	return map[uint]repo.ConversationRecord{
		low:  build(low, high, senderRead),
		high: build(high, low, peerRead),
	}
}

func TestChatRealtimeChatMessageAckAndPeerDelivery(t *testing.T) {
	requestID := "550e8400-e29b-41d4-a716-446655440000"
	now := time.Now().UTC()
	repository := &realtimeConvStub{
		chatRepoStub: chatRepoStub{
			sendResult: repo.SendMessageResult{Message: model.Message{
				ID: 1, ConversationID: 41, Seq: 5, SenderID: 7, ClientMessageID: requestID,
				MessageType: model.MessageTypeText, Content: "今晚开团吗？", Metadata: []byte(`{}`), CreatedAt: now,
			}},
		},
		records: realtimeConversationFixture(7, 8, 5, 5, 4),
	}
	events := &eventRecorder{}
	realtime := NewChatRealtime(NewChatService(repository, chatUsersStub{users: map[uint]model.User{
		7: {ID: 7, Username: "sender"}, 8: {ID: 8, Username: "peer"},
	}}), events)

	reply, err := realtime.HandleIM(context.Background(), 7, imws.ClientMessage{
		Type: imws.MsgChatMessage, RequestID: requestID,
		Data: json.RawMessage(`{"conversation_id":41,"message_type":"text","content":"今晚开团吗？"}`),
	})
	if err != nil {
		t.Fatalf("HandleIM() error = %v", err)
	}
	if reply.Type != imws.MsgChatAck || reply.RequestID != requestID {
		t.Fatalf("reply = %#v", reply)
	}
	var ack ChatAckData
	if err := json.Unmarshal(reply.Data, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Duplicate || ack.Message == nil || ack.Message.Seq != 5 || ack.Message.Sender == nil {
		t.Fatalf("ack data = %#v", ack)
	}

	events2 := events.all()
	if len(events2) != 3 {
		t.Fatalf("published events = %#v", events2)
	}
	if events2[0].userID != 8 || events2[0].eventType != "chat_message" {
		t.Fatalf("first event = %#v", events2[0])
	}
	push, ok := events2[0].data.(ChatMessageEventData)
	if !ok || push.Message == nil || push.Message.Content != "今晚开团吗？" || push.Message.Sender.ID != 7 {
		t.Fatalf("peer push = %#v", events2[0].data)
	}
	if events2[1].userID != 7 || events2[2].userID != 8 {
		t.Fatalf("conversation_updated recipients = %d, %d", events2[1].userID, events2[2].userID)
	}
	for index, wantUnread := range []uint64{0, 1} {
		data, ok := events2[index+1].data.(ConversationUpdatedEventData)
		if !ok {
			t.Fatalf("event %d data = %#v", index+1, events2[index+1].data)
		}
		if data.Conversation.LastSeq != 5 || data.Conversation.UnreadCount != wantUnread {
			t.Fatalf("event %d conversation = %#v", index+1, data.Conversation)
		}
	}
}

func TestChatRealtimeDuplicateMessageOnlyAcks(t *testing.T) {
	requestID := "550e8400-e29b-41d4-a716-446655440000"
	repository := &realtimeConvStub{
		chatRepoStub: chatRepoStub{sendResult: repo.SendMessageResult{
			Message: model.Message{ID: 1, ConversationID: 41, Seq: 5, SenderID: 7, ClientMessageID: requestID}, Duplicate: true,
		}},
		records: realtimeConversationFixture(7, 8, 5, 5, 4),
	}
	events := &eventRecorder{}
	realtime := NewChatRealtime(NewChatService(repository, chatUsersStub{users: map[uint]model.User{7: {ID: 7}}}), events)

	reply, err := realtime.HandleIM(context.Background(), 7, imws.ClientMessage{
		Type: imws.MsgChatMessage, RequestID: requestID,
		Data: json.RawMessage(`{"conversation_id":41,"message_type":"text","content":"hello"}`),
	})
	if err != nil {
		t.Fatalf("HandleIM() error = %v", err)
	}
	var ack ChatAckData
	if err := json.Unmarshal(reply.Data, &ack); err != nil {
		t.Fatal(err)
	}
	if !ack.Duplicate || ack.Message == nil {
		t.Fatalf("ack data = %#v", ack)
	}
	if remaining := events.all(); len(remaining) != 0 {
		t.Fatalf("duplicate broadcast events = %#v", remaining)
	}
}

func TestChatRealtimeSendErrorsMapToBusinessErrors(t *testing.T) {
	requestID := "550e8400-e29b-41d4-a716-446655440000"
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "conversation missing", err: ErrConversationNotFound, wantCode: imws.ErrorCodeConversationNotFound},
		{name: "friendship required", err: ErrFriendshipRequired, wantCode: imws.ErrorCodeFriendshipRequired},
		{name: "invalid message", err: ErrInvalidChatMessage, wantCode: imws.ErrorCodeInvalidChatMessage},
		{name: "invalid content", err: ErrInvalidMessageContent, wantCode: imws.ErrorCodeInvalidMessageContent},
		{name: "internal", err: errors.New("database down"), wantCode: imws.ErrorCodeChatUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository := &realtimeConvStub{chatRepoStub: chatRepoStub{err: tt.err}}
			events := &eventRecorder{}
			realtime := NewChatRealtime(NewChatService(repository, chatUsersStub{}), events)
			_, err := realtime.HandleIM(context.Background(), 7, imws.ClientMessage{
				Type: imws.MsgChatMessage, RequestID: requestID,
				Data: json.RawMessage(`{"conversation_id":41,"message_type":"text","content":"hello"}`),
			})
			var business *imws.BusinessError
			if !errors.As(err, &business) || business.Code != tt.wantCode {
				t.Fatalf("error = %v, want code %d", err, tt.wantCode)
			}
			if remaining := events.all(); len(remaining) != 0 {
				t.Fatalf("failed send published events = %#v", remaining)
			}
		})
	}
}

func TestChatRealtimeChatMessageRejectsInvalidPayloads(t *testing.T) {
	requestID := "550e8400-e29b-41d4-a716-446655440000"
	realtime := NewChatRealtime(NewChatService(&realtimeConvStub{}, chatUsersStub{}), &eventRecorder{})
	tests := []struct {
		name     string
		data     string
		wantCode int
	}{
		{name: "missing data", data: ``, wantCode: imws.ErrorCodeInvalidChatMessage},
		{name: "unknown field", data: `{"conversation_id":41,"message_type":"text","content":"hi","extra":1}`, wantCode: imws.ErrorCodeInvalidChatMessage},
		{name: "non text type", data: `{"conversation_id":41,"message_type":"system","content":"hi"}`, wantCode: imws.ErrorCodeInvalidMessageContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := realtime.HandleIM(context.Background(), 7, imws.ClientMessage{
				Type: imws.MsgChatMessage, RequestID: requestID, Data: json.RawMessage(tt.data),
			})
			var business *imws.BusinessError
			if !errors.As(err, &business) || business.Code != tt.wantCode {
				t.Fatalf("error = %v, want code %d", err, tt.wantCode)
			}
		})
	}
}

func TestChatRealtimeImSyncReturnsAscendingBatch(t *testing.T) {
	now := time.Now().UTC()
	repository := &realtimeConvStub{chatRepoStub: chatRepoStub{afterMessages: []model.Message{
		{ID: 9, ConversationID: 41, Seq: 9, SenderID: 8, ClientMessageID: "9", Metadata: []byte(`{}`), CreatedAt: now},
		{ID: 10, ConversationID: 41, Seq: 10, SenderID: 7, ClientMessageID: "10", Metadata: []byte(`{}`), CreatedAt: now},
		{ID: 11, ConversationID: 41, Seq: 11, SenderID: 8, ClientMessageID: "11", Metadata: []byte(`{}`), CreatedAt: now},
	}}}
	events := &eventRecorder{}
	realtime := NewChatRealtime(NewChatService(repository, chatUsersStub{users: map[uint]model.User{
		7: {ID: 7}, 8: {ID: 8},
	}}), events)

	reply, err := realtime.HandleIM(context.Background(), 7, imws.ClientMessage{
		Type: imws.MsgImSync, RequestID: "8c21c14d-cf36-4fd2-845d-1496d9c154b2",
		Data: json.RawMessage(`{"conversation_id":41,"since_seq":8,"limit":2}`),
	})
	if err != nil {
		t.Fatalf("HandleIM() error = %v", err)
	}
	if reply.Type != imws.MsgImSyncBatch {
		t.Fatalf("reply type = %q", reply.Type)
	}
	var batch ImSyncBatchData
	if err := json.Unmarshal(reply.Data, &batch); err != nil {
		t.Fatal(err)
	}
	if batch.ConversationID != 41 || !batch.HasMore || batch.NextSeq != 10 || len(batch.Messages) != 2 {
		t.Fatalf("batch = %#v", batch)
	}
	if batch.Messages[0].Seq != 9 || batch.Messages[1].Seq != 10 {
		t.Fatalf("batch order = %#v", batch.Messages)
	}
	if repository.sinceSeq != 8 || repository.limit != 3 {
		t.Fatalf("repo query = (since=%d, limit=%d)", repository.sinceSeq, repository.limit)
	}
}

func TestChatRealtimeImSyncEmptyBatchEchoesSinceSeq(t *testing.T) {
	repository := &realtimeConvStub{chatRepoStub: chatRepoStub{afterMessages: []model.Message{}}}
	realtime := NewChatRealtime(NewChatService(repository, chatUsersStub{}), &eventRecorder{})
	reply, err := realtime.HandleIM(context.Background(), 7, imws.ClientMessage{
		Type: imws.MsgImSync, RequestID: "8c21c14d-cf36-4fd2-845d-1496d9c154b2",
		Data: json.RawMessage(`{"conversation_id":41,"since_seq":12}`),
	})
	if err != nil {
		t.Fatalf("HandleIM() error = %v", err)
	}
	var batch ImSyncBatchData
	if err := json.Unmarshal(reply.Data, &batch); err != nil {
		t.Fatal(err)
	}
	if batch.HasMore || batch.NextSeq != 12 || len(batch.Messages) != 0 {
		t.Fatalf("batch = %#v", batch)
	}
}

func TestChatRealtimeImSyncRejectsInvalidPayloads(t *testing.T) {
	requestID := "8c21c14d-cf36-4fd2-845d-1496d9c154b2"
	realtime := NewChatRealtime(NewChatService(&realtimeConvStub{}, chatUsersStub{}), &eventRecorder{})
	for _, data := range []string{
		``,
		`{"conversation_id":0,"since_seq":0}`,
		`{"conversation_id":41,"limit":101}`,
		`{"conversation_id":41,"extra":true}`,
	} {
		_, err := realtime.HandleIM(context.Background(), 7, imws.ClientMessage{
			Type: imws.MsgImSync, RequestID: requestID, Data: json.RawMessage(data),
		})
		var business *imws.BusinessError
		if !errors.As(err, &business) || business.Code != imws.ErrorCodeInvalidSyncRequest {
			t.Fatalf("data %q error = %v, want code %d", data, err, imws.ErrorCodeInvalidSyncRequest)
		}
	}
}

// TestChatRealtimeChatMessageOverRealWebSocket 用两个真实 WebSocket 连接验证
// 发送端 ack、接收端 chat_message 与双方 conversation_updated 的端到端投递。
func TestChatRealtimeChatMessageOverRealWebSocket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	origins, err := realtime.ParseAllowedOrigins(imwsTestOrigin)
	if err != nil {
		t.Fatal(err)
	}
	hub := imws.NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)

	requestID := "550e8400-e29b-41d4-a716-446655440000"
	now := time.Now().UTC().Truncate(time.Millisecond)
	repository := &realtimeConvStub{
		chatRepoStub: chatRepoStub{
			sendResult: repo.SendMessageResult{Message: model.Message{
				ID: 1, ConversationID: 41, Seq: 1, SenderID: 7, ClientMessageID: requestID,
				MessageType: model.MessageTypeText, Content: "realtime hello", Metadata: []byte(`{}`), CreatedAt: now,
			}},
		},
		records: realtimeConversationFixture(7, 8, 1, 1, 0),
	}
	chatService := NewChatService(repository, chatUsersStub{users: map[uint]model.User{
		7: {ID: 7, Username: "alice", Nickname: "Alice"},
		8: {ID: 8, Username: "bob", Nickname: "Bob"},
	}})
	hub.SetInboundHandler(NewChatRealtime(chatService, hub))

	engine := gin.New()
	engine.GET("/ws/im", imws.HandleWebSocket(hub, imwsTestJWTSecret, origins))
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	sender := dialIMTest(t, wsURL, generateIMTestToken(t, 7))
	defer sender.Close()
	_ = readIMServerMessage(t, sender)
	peer := dialIMTest(t, wsURL, generateIMTestToken(t, 8))
	defer peer.Close()
	_ = readIMServerMessage(t, peer)

	frame := `{"type":"chat_message","request_id":"` + requestID + `","data":{"conversation_id":41,"message_type":"text","content":"realtime hello"}}`
	if err := sender.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write chat_message: %v", err)
	}

	// Hub 按事件循环顺序投递：发送者先收 conversation_updated 再收 chat_ack，
	// 对端先收 chat_message 再收 conversation_updated；单连接内顺序固定。
	senderFrames := make([]imws.ServerMessage, 2)
	for index := range senderFrames {
		senderFrames[index] = readIMServerMessage(t, sender)
	}
	if senderFrames[0].Type != imws.MsgConversationUpdated || senderFrames[1].Type != imws.MsgChatAck {
		t.Fatalf("sender frames = %q, %q", senderFrames[0].Type, senderFrames[1].Type)
	}
	var updatedData ConversationUpdatedEventData
	if err := json.Unmarshal(senderFrames[0].Data, &updatedData); err != nil {
		t.Fatal(err)
	}
	if updatedData.Conversation.ID != 41 || updatedData.Conversation.LastSeq != 1 ||
		updatedData.Conversation.UnreadCount != 0 || updatedData.Conversation.Peer.ID != 8 {
		t.Fatalf("sender conversation view = %#v", updatedData.Conversation)
	}
	var ackData ChatAckData
	if err := json.Unmarshal(senderFrames[1].Data, &ackData); err != nil {
		t.Fatal(err)
	}
	if senderFrames[1].RequestID != requestID || ackData.Duplicate || ackData.Message == nil || ackData.Message.Seq != 1 {
		t.Fatalf("ack = %#v data = %#v", senderFrames[1], ackData)
	}

	peerFrames := make([]imws.ServerMessage, 2)
	for index := range peerFrames {
		peerFrames[index] = readIMServerMessage(t, peer)
	}
	if peerFrames[0].Type != imws.MsgChatMessage || peerFrames[1].Type != imws.MsgConversationUpdated {
		t.Fatalf("peer frames = %q, %q", peerFrames[0].Type, peerFrames[1].Type)
	}
	var pushData ChatMessageEventData
	if err := json.Unmarshal(peerFrames[0].Data, &pushData); err != nil {
		t.Fatal(err)
	}
	if pushData.Message == nil || pushData.Message.Content != "realtime hello" || pushData.Message.Sender.ID != 7 {
		t.Fatalf("peer push = %#v", pushData)
	}
	var peerUpdated ConversationUpdatedEventData
	if err := json.Unmarshal(peerFrames[1].Data, &peerUpdated); err != nil {
		t.Fatal(err)
	}
	if peerUpdated.Conversation.ID != 41 || peerUpdated.Conversation.LastSeq != 1 ||
		peerUpdated.Conversation.UnreadCount != 1 || peerUpdated.Conversation.Peer.ID != 7 {
		t.Fatalf("peer conversation view = %#v", peerUpdated.Conversation)
	}
}
