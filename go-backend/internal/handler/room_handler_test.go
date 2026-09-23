package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"trpggame/internal/model"
	"trpggame/internal/repo"
	"trpggame/internal/service"
)

type roomStateServiceStub struct {
	RoomService
	state *service.MultiplayerGameState
	err   error
	user  uint
	room  uint
}

func (s *roomStateServiceStub) GetMultiplayerState(_ context.Context, userID, roomID uint) (*service.MultiplayerGameState, error) {
	s.user, s.room = userID, roomID
	return s.state, s.err
}

func TestRoomHandlerMutationsRequireExpectedVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name  string
		path  string
		body  string
		serve func(*RoomHandler, *gin.Context)
	}{
		{"character", "/rooms/9/character", `{"character_id":3}`, (*RoomHandler).SelectCharacter},
		{"ready", "/rooms/9/ready", `{"ready":false}`, (*RoomHandler).SetReady},
		{"leave", "/rooms/9/leave", `{}`, (*RoomHandler).Leave},
		{"remove", "/rooms/9/members/8/remove", `{}`, (*RoomHandler).Remove},
		{"transfer", "/rooms/9/transfer", `{"new_owner_user_id":8}`, (*RoomHandler).Transfer},
		{"start", "/rooms/9/start", `{}`, (*RoomHandler).Start},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Set("user_id", uint(7))
			context.Params = gin.Params{{Key: "roomId", Value: "9"}, {Key: "userId", Value: "8"}}
			context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			context.Request.Header.Set("Content-Type", "application/json")
			test.serve(NewRoomHandler(nil), context)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			var body struct {
				Code int `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != 1900 {
				t.Fatalf("body = %s, err = %v", response.Body.String(), err)
			}
		})
	}
}

func TestRoomHandlerVersionConflictIncludesCurrentVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	roomError(context, &repo.RoomVersionError{Current: 17})
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Code           int    `json:"code"`
		CurrentVersion uint64 `json:"current_version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != 1908 || body.CurrentVersion != 17 {
		t.Fatalf("body = %s, err = %v", response.Body.String(), err)
	}
}

func TestRoomHandlerReturnsMultiplayerRuntimeState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deadline := time.Date(2026, 9, 22, 8, 2, 0, 0, time.UTC)
	stub := &roomStateServiceStub{state: &service.MultiplayerGameState{
		Seq: 12,
		MultiplayerRuntimeSnapshot: &model.MultiplayerRuntimeSnapshot{
			Version: 2, RoomID: 41, Status: model.RoomStatusPlaying,
			Generation: "bc624606-57a3-49c5-bf51-f5a04e5f299f",
			TurnOrder:  []uint{7, 8}, CurrentActorID: 7, DeadlineAt: &deadline,
			Players: []model.MultiplayerRuntimePlayer{
				{UserID: 7, CharacterID: 101, PlayerState: map[string]string{}, Items: []model.RuntimeItem{}, Buffs: []model.RuntimeBuff{}},
				{UserID: 8, CharacterID: 102, PlayerState: map[string]string{}, Items: []model.RuntimeItem{}, Buffs: []model.RuntimeBuff{}},
			},
			RecentMessages: []model.RuntimeMessage{{Role: "assistant", Content: "opening"}},
		},
	}}
	response := httptest.NewRecorder()
	requestContext, _ := gin.CreateTestContext(response)
	requestContext.Set("user_id", uint(8))
	requestContext.Params = gin.Params{{Key: "roomId", Value: "41"}}
	requestContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/games/41/state", nil)

	NewRoomHandler(stub).GameState(requestContext)

	if response.Code != http.StatusOK || stub.user != 8 || stub.room != 41 {
		t.Fatalf("status=%d user=%d room=%d body=%s", response.Code, stub.user, stub.room, response.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Seq     int64 `json:"seq"`
			Version int   `json:"version"`
			RoomID  uint  `json:"room_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != 0 || body.Data.Seq != 12 || body.Data.Version != 2 || body.Data.RoomID != 41 {
		t.Fatalf("body=%s err=%v", response.Body.String(), err)
	}
}

func TestRoomHandlerMapsMultiplayerRuntimeFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	requestContext, _ := gin.CreateTestContext(response)
	requestContext.Set("user_id", uint(8))
	requestContext.Params = gin.Params{{Key: "roomId", Value: "41"}}
	requestContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/games/41/state", nil)

	NewRoomHandler(&roomStateServiceStub{err: service.ErrMultiplayerRuntimeUnavailable}).GameState(requestContext)

	var body struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusServiceUnavailable || body.Code != 1920 {
		t.Fatalf("status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
}
