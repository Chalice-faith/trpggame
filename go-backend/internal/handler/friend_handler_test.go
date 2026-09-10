package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"trpggame/internal/model"
	"trpggame/internal/service"
)

type friendHandlerServiceStub struct {
	direction string
	status    model.FriendshipStatus
	cursor    uint
	limit     int
}

func (*friendHandlerServiceStub) SearchUsers(context.Context, uint, string) ([]service.UserSearchItem, error) {
	return []service.UserSearchItem{}, nil
}
func (*friendHandlerServiceStub) SendRequest(context.Context, uint, uint) (*service.FriendRequestItem, error) {
	return &service.FriendRequestItem{}, nil
}
func (s *friendHandlerServiceStub) ListRequests(_ context.Context, _ uint, direction string, status model.FriendshipStatus, cursor uint, limit int) (*service.FriendRequestPage, error) {
	s.direction, s.status, s.cursor, s.limit = direction, status, cursor, limit
	return &service.FriendRequestPage{Items: []service.FriendRequestItem{}}, nil
}
func (*friendHandlerServiceStub) RespondRequest(context.Context, uint, uint, bool) (*service.FriendRequestItem, error) {
	return &service.FriendRequestItem{}, nil
}
func (*friendHandlerServiceStub) ListFriends(context.Context, uint, uint, int) (*service.FriendPage, error) {
	return &service.FriendPage{Items: []service.FriendItem{}}, nil
}
func (*friendHandlerServiceStub) DeleteFriend(context.Context, uint, uint) error { return nil }

func TestFriendHandlerListRequestsDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &friendHandlerServiceStub{}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user_id", uint(7)); c.Next() })
	router.GET("/api/v1/friend-requests", NewFriendHandler(stub).ListRequests)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/friend-requests", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.direction != "incoming" || stub.status != model.FriendshipStatusPending || stub.cursor != 0 || stub.limit != 20 {
		t.Fatalf("query = direction=%q status=%q cursor=%d limit=%d", stub.direction, stub.status, stub.cursor, stub.limit)
	}
}

func TestFriendHandlerRejectsInvalidPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &friendHandlerServiceStub{}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user_id", uint(7)); c.Next() })
	router.GET("/api/v1/friends", NewFriendHandler(stub).ListFriends)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/friends?cursor=0&limit=101", nil))
	assertFriendErrorResponse(t, recorder, http.StatusBadRequest, 1607)
}

func TestFriendErrorMappings(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   int
	}{
		{service.ErrInvalidFriendRequest, 400, 1600},
		{service.ErrFriendTargetNotFound, 404, 1601},
		{service.ErrCannotFriendSelf, 400, 1602},
		{service.ErrFriendRequestMissing, 404, 1603},
		{service.ErrFriendForbidden, 403, 1604},
		{service.ErrFriendStateConflict, 409, 1605},
		{service.ErrFriendshipNotFound, 404, 1606},
		{service.ErrInvalidFriendQuery, 400, 1607},
		{errors.New("database failed"), 500, 1699},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		friendError(ctx, testCase.err)
		assertFriendErrorResponse(t, recorder, testCase.status, testCase.code)
	}
}

func assertFriendErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, status, code int) {
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
