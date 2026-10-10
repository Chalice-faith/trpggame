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

	"trpggame/internal/model"
	"trpggame/internal/service"
)

type groupHandlerServiceStub struct {
	groupID  uint
	targetID uint
	version  uint64
	role     model.GroupRole
	userIDs  []uint
	cursor   string
	limit    int
	err      error
}

func (s *groupHandlerServiceStub) result() (*service.GroupSummary, error) {
	return &service.GroupSummary{ID: 9, Version: 2}, s.err
}
func (s *groupHandlerServiceStub) Create(context.Context, uint, string, string) (*service.GroupSummary, error) {
	return s.result()
}
func (s *groupHandlerServiceStub) List(_ context.Context, _ uint, cursor string, limit int) (*service.GroupPage, error) {
	s.cursor, s.limit = cursor, limit
	return &service.GroupPage{Items: []service.GroupSummary{}}, s.err
}
func (s *groupHandlerServiceStub) Get(_ context.Context, _, groupID uint) (*service.GroupSummary, error) {
	s.groupID = groupID
	return s.result()
}
func (s *groupHandlerServiceStub) ListMembers(_ context.Context, _, groupID uint, cursor string, limit int) (*service.GroupMemberPage, error) {
	s.groupID, s.cursor, s.limit = groupID, cursor, limit
	return &service.GroupMemberPage{Items: []service.GroupMemberItem{}}, s.err
}
func (s *groupHandlerServiceStub) Update(_ context.Context, _, groupID uint, version uint64, _, _ *string) (*service.GroupSummary, error) {
	s.groupID, s.version = groupID, version
	return s.result()
}
func (s *groupHandlerServiceStub) Invite(_ context.Context, _, groupID uint, userIDs []uint, version uint64) (*service.GroupSummary, error) {
	s.groupID, s.userIDs, s.version = groupID, append([]uint(nil), userIDs...), version
	return s.result()
}
func (s *groupHandlerServiceStub) SetRole(_ context.Context, _, groupID, targetID uint, role model.GroupRole, version uint64) (*service.GroupSummary, error) {
	s.groupID, s.targetID, s.role, s.version = groupID, targetID, role, version
	return s.result()
}
func (s *groupHandlerServiceStub) Remove(_ context.Context, _, groupID, targetID uint, version uint64) (*service.GroupSummary, error) {
	s.groupID, s.targetID, s.version = groupID, targetID, version
	return s.result()
}
func (s *groupHandlerServiceStub) Transfer(_ context.Context, _, groupID, targetID uint, version uint64) (*service.GroupSummary, error) {
	s.groupID, s.targetID, s.version = groupID, targetID, version
	return s.result()
}

func groupTestRouter(stub *groupHandlerServiceStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user_id", uint(7)); c.Next() })
	h := NewGroupHandler(stub)
	router.POST("/groups", h.Create)
	router.GET("/groups", h.List)
	router.GET("/groups/:groupId", h.Get)
	router.PATCH("/groups/:groupId", h.Update)
	router.GET("/groups/:groupId/members", h.ListMembers)
	router.POST("/groups/:groupId/members", h.Invite)
	router.PATCH("/groups/:groupId/members/:userId", h.SetRole)
	router.DELETE("/groups/:groupId/members/:userId", h.Remove)
	router.POST("/groups/:groupId/transfer", h.Transfer)
	return router
}

func TestGroupHandlerParsesPaginationAndVersionedMutations(t *testing.T) {
	stub := &groupHandlerServiceStub{}
	recorder := httptest.NewRecorder()
	groupTestRouter(stub).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/groups/9/members?cursor=15&limit=25", nil))
	if recorder.Code != http.StatusOK || stub.groupID != 9 || stub.cursor != "15" || stub.limit != 25 {
		t.Fatalf("list members status=%d args=(%d,%q,%d)", recorder.Code, stub.groupID, stub.cursor, stub.limit)
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/groups/9/members", strings.NewReader(`{"user_ids":[8,10],"expected_version":3}`))
	request.Header.Set("Content-Type", "application/json")
	groupTestRouter(stub).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || stub.version != 3 || len(stub.userIDs) != 2 {
		t.Fatalf("invite status=%d version=%d ids=%v", recorder.Code, stub.version, stub.userIDs)
	}

	recorder = httptest.NewRecorder()
	groupTestRouter(stub).ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/groups/9/members/8?expected_version=4", nil))
	if recorder.Code != http.StatusOK || stub.targetID != 8 || stub.version != 4 {
		t.Fatalf("remove status=%d target=%d version=%d", recorder.Code, stub.targetID, stub.version)
	}
}

func TestGroupHandlerRejectsMissingExpectedVersion(t *testing.T) {
	stub := &groupHandlerServiceStub{}
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPatch, "/groups/9", `{"name":"new"}`},
		{http.MethodDelete, "/groups/9/members/8", ""},
		{http.MethodPost, "/groups/9/transfer", `{"new_owner_user_id":8}`},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		groupTestRouter(stub).ServeHTTP(recorder, request)
		assertGroupErrorResponse(t, recorder, http.StatusBadRequest, 1800)
	}
}

func TestGroupHandlerErrorMappings(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   int
	}{
		{service.ErrInvalidGroupRequest, 400, 1800},
		{service.ErrGroupNotFound, 404, 1801},
		{service.ErrGroupPermissionDenied, 403, 1802},
		{service.ErrGroupFriendshipRequired, 409, 1803},
		{service.ErrGroupMemberLimitReached, 409, 1804},
		{service.ErrGroupOwnerConflict, 409, 1805},
		{service.ErrGroupVersionConflict, 409, 1806},
		{errors.New("database failed"), 500, 1807},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		groupError(ctx, testCase.err)
		assertGroupErrorResponse(t, recorder, testCase.status, testCase.code)
	}
}

func assertGroupErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, status, code int) {
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
