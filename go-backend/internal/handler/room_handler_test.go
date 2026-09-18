package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"trpggame/internal/repo"
)

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
