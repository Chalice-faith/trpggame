package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"trpggame/internal/service"
)

type chatHandlerServiceStub struct {
	cursor         string
	limit          int
	conversationID uint
	beforeSeq      uint64
	readSeq        uint64
	err            error
}

func (s *chatHandlerServiceStub) CreateDirect(context.Context, uint, uint) (*service.ConversationSummary, error) {
	return &service.ConversationSummary{ID: 41}, s.err
}
func (s *chatHandlerServiceStub) ListConversations(_ context.Context, _ uint, cursor string, limit int) (*service.ConversationPage, error) {
	s.cursor, s.limit = cursor, limit
	return &service.ConversationPage{Items: []service.ConversationSummary{}}, s.err
}
func (s *chatHandlerServiceStub) ListMessages(_ context.Context, _, conversationID uint, beforeSeq uint64, limit int) (*service.MessagePage, error) {
	s.conversationID, s.beforeSeq, s.limit = conversationID, beforeSeq, limit
	return &service.MessagePage{Items: []service.MessageItem{}}, s.err
}
func (s *chatHandlerServiceStub) MarkRead(_ context.Context, _, conversationID uint, readSeq uint64) (*service.ReadResult, error) {
	s.conversationID, s.readSeq = conversationID, readSeq
	return &service.ReadResult{ConversationID: conversationID, LastReadSeq: readSeq}, s.err
}

func chatTestRouter(stub *chatHandlerServiceStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user_id", uint(7)); c.Next() })
	h := NewChatHandler(stub)
	router.POST("/conversations/direct", h.CreateDirect)
	router.GET("/conversations", h.ListConversations)
	router.GET("/conversations/:conversationId/messages", h.ListMessages)
	router.POST("/conversations/:conversationId/read", h.MarkRead)
	return router
}

func TestChatHandlerListDefaultsAndOpaqueCursor(t *testing.T) {
	stub := &chatHandlerServiceStub{}
	recorder := httptest.NewRecorder()
	chatTestRouter(stub).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/conversations?cursor=opaque", nil))
	if recorder.Code != http.StatusOK || stub.cursor != "opaque" || stub.limit != 20 {
		t.Fatalf("status=%d cursor=%q limit=%d body=%s", recorder.Code, stub.cursor, stub.limit, recorder.Body.String())
	}
}

func TestChatHandlerMessageQueryParsing(t *testing.T) {
	stub := &chatHandlerServiceStub{}
	recorder := httptest.NewRecorder()
	chatTestRouter(stub).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/conversations/41/messages?before_seq=9&limit=25", nil))
	if recorder.Code != http.StatusOK || stub.conversationID != 41 || stub.beforeSeq != 9 || stub.limit != 25 {
		t.Fatalf("status=%d args=(%d,%d,%d)", recorder.Code, stub.conversationID, stub.beforeSeq, stub.limit)
	}

	invalid := httptest.NewRecorder()
	chatTestRouter(stub).ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/conversations/41/messages?before_seq=0", nil))
	assertChatErrorResponse(t, invalid, http.StatusBadRequest, 1713)
}

func TestChatHandlerMarkReadAcceptsZeroAndRejectsMissingField(t *testing.T) {
	stub := &chatHandlerServiceStub{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/conversations/41/read", strings.NewReader(`{"last_read_seq":0}`))
	request.Header.Set("Content-Type", "application/json")
	chatTestRouter(stub).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || stub.conversationID != 41 || stub.readSeq != 0 {
		t.Fatalf("status=%d args=(%d,%d), body=%s", recorder.Code, stub.conversationID, stub.readSeq, recorder.Body.String())
	}

	invalid := httptest.NewRecorder()
	missing := httptest.NewRequest(http.MethodPost, "/conversations/41/read", strings.NewReader(`{}`))
	missing.Header.Set("Content-Type", "application/json")
	chatTestRouter(stub).ServeHTTP(invalid, missing)
	assertChatErrorResponse(t, invalid, http.StatusBadRequest, 1708)
}

func TestChatHandlerErrorMappings(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   int
	}{
		{service.ErrInvalidConversationRequest, 400, 1708},
		{service.ErrConversationNotFound, 404, 1709},
		{service.ErrFriendshipRequired, 409, 1710},
		{service.ErrInvalidMessageQuery, 400, 1713},
		{service.ErrReadSequenceConflict, 409, 1714},
		{errors.New("database failed"), 500, 1716},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		chatError(ctx, testCase.err)
		assertChatErrorResponse(t, recorder, testCase.status, testCase.code)
	}
}

func assertChatErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, status, code int) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, status, recorder.Body.String())
	}
	var body struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != code {
		t.Fatalf("code = %d, want %d", body.Code, code)
	}
}
