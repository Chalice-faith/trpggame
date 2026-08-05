package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"trpggame/internal/model"
	"trpggame/internal/service"
)

type fakeGameStartService struct {
	result        *service.StartSoloGameResult
	err           error
	request       *service.StartSoloGameRequest
	actionResult  *service.SubmitGameActionResult
	actionErr     error
	actionRequest *service.SubmitGameActionRequest
}

func (s *fakeGameStartService) SubmitAction(
	_ context.Context,
	req *service.SubmitGameActionRequest,
) (*service.SubmitGameActionResult, error) {
	s.actionRequest = req
	return s.actionResult, s.actionErr
}

func (s *fakeGameStartService) StartSoloGame(
	_ context.Context,
	req *service.StartSoloGameRequest,
) (*service.StartSoloGameResult, error) {
	s.request = req
	return s.result, s.err
}

func TestGameHandlerStartSoloGame(t *testing.T) {
	fakeService := &fakeGameStartService{result: &service.StartSoloGameResult{
		Room: &model.GameRoom{
			ID:     41,
			Status: model.RoomStatusPlaying,
		},
		OpeningNarrative: "你站在古宅门前。",
	}}
	handler := NewGameHandler(fakeService)
	router := startSoloGameTestRouter(handler, uint(7))
	request := httptest.NewRequest(
		http.MethodPost,
		"/games/solo/start",
		bytes.NewBufferString(`{"script_id":11,"character_id":13}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if fakeService.request == nil || fakeService.request.UserID != 7 ||
		fakeService.request.ScriptID != 11 || fakeService.request.CharacterID != 13 {
		t.Fatalf("service request = %#v", fakeService.request)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			RoomID           uint             `json:"room_id"`
			GameStatus       model.RoomStatus `json:"game_status"`
			OpeningNarrative string           `json:"opening_narrative"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.RoomID != 41 ||
		response.Data.GameStatus != model.RoomStatusPlaying ||
		response.Data.OpeningNarrative != "你站在古宅门前。" {
		t.Fatalf("response = %#v", response)
	}
}

func TestGameHandlerStartSoloGameRequiresAuthenticationContext(t *testing.T) {
	fakeService := &fakeGameStartService{}
	handler := NewGameHandler(fakeService)

	for _, identity := range []any{nil, "7", uint(0)} {
		router := startSoloGameTestRouter(handler, identity)
		request := httptest.NewRequest(
			http.MethodPost,
			"/games/solo/start",
			bytes.NewBufferString(`{"script_id":11,"character_id":13}`),
		)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
	}
	if fakeService.request != nil {
		t.Fatal("invalid authentication context reached service")
	}
}

func TestGameHandlerStartSoloGameRejectsInvalidJSONContract(t *testing.T) {
	tests := []string{
		`not-json`,
		`{}`,
		`{"script_id":0,"character_id":13}`,
		`{"script_id":11,"character_id":0}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			fakeService := &fakeGameStartService{}
			handler := NewGameHandler(fakeService)
			router := startSoloGameTestRouter(handler, uint(7))
			request := httptest.NewRequest(
				http.MethodPost,
				"/games/solo/start",
				bytes.NewBufferString(body),
			)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusBadRequest, 1300)
			if fakeService.request != nil {
				t.Fatal("invalid JSON contract reached service")
			}
		})
	}
}

func TestGameHandlerStartSoloGameMapsSafeServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"invalid request", service.ErrInvalidGameRequest, http.StatusBadRequest, 1300},
		{"script not found", service.ErrScriptNotFound, http.StatusNotFound, 1301},
		{"script not ready", service.ErrScriptNotReady, http.StatusConflict, 1302},
		{"character not found", service.ErrCharacterNotFound, http.StatusNotFound, 1303},
		{
			"AI unavailable",
			fmt.Errorf("%w: sensitive upstream response", service.ErrAIUnavailable),
			http.StatusServiceUnavailable,
			1304,
		},
		{"empty opening", service.ErrEmptyOpeningNarrative, http.StatusServiceUnavailable, 1304},
		{"status conflict", service.ErrGameStartConflict, http.StatusConflict, 1305},
		{
			"internal",
			fmt.Errorf("%w: sensitive mysql detail", service.ErrInternal),
			http.StatusInternalServerError,
			1306,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{err: test.err}
			handler := NewGameHandler(fakeService)
			router := startSoloGameTestRouter(handler, uint(7))
			request := httptest.NewRequest(
				http.MethodPost,
				"/games/solo/start",
				bytes.NewBufferString(`{"script_id":11,"character_id":13}`),
			)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, test.wantStatus, test.wantCode)
			if bytes.Contains(recorder.Body.Bytes(), []byte("sensitive")) {
				t.Fatalf("response leaked wrapped error detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestGameHandlerStartSoloGameRejectsInvalidServiceResult(t *testing.T) {
	tests := []*service.StartSoloGameResult{
		nil,
		{},
		{Room: &model.GameRoom{ID: 41, Status: model.RoomStatusWaiting}, OpeningNarrative: "开场"},
		{Room: &model.GameRoom{ID: 41, Status: model.RoomStatusPlaying}},
		{Room: &model.GameRoom{ID: 41, Status: model.RoomStatusPlaying}, OpeningNarrative: "  \t"},
	}
	for index, result := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			handler := NewGameHandler(&fakeGameStartService{result: result})
			router := startSoloGameTestRouter(handler, uint(7))
			request := httptest.NewRequest(
				http.MethodPost,
				"/games/solo/start",
				bytes.NewBufferString(`{"script_id":11,"character_id":13}`),
			)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, 1306)
		})
	}
}

func startSoloGameTestRouter(handler *GameHandler, identity any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/games/solo/start", func(c *gin.Context) {
		if identity != nil {
			c.Set("user_id", identity)
		}
		handler.StartSoloGame(c)
	})
	return router
}

func TestGameHandlerSubmitAction(t *testing.T) {
	fakeService := &fakeGameStartService{actionResult: validSubmitActionResult()}
	handler := NewGameHandler(fakeService)
	router := submitActionTestRouter(handler, uint(7))
	request := httptest.NewRequest(
		http.MethodPost,
		"/games/41/action",
		bytes.NewBufferString(`{"request_id":"550e8400-e29b-41d4-a716-446655440000","expected_turn":3,"action_text":"调查书房"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.actionRequest == nil || fakeService.actionRequest.UserID != 7 ||
		fakeService.actionRequest.RoomID != 41 || fakeService.actionRequest.ExpectedTurn != 3 ||
		fakeService.actionRequest.Action != "调查书房" ||
		fakeService.actionRequest.RequestID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("service request = %#v", fakeService.actionRequest)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			Narrative   string                  `json:"narrative"`
			DiceRoll    *service.ActionDiceRoll `json:"dice_roll"`
			Effects     *service.ActionEffects  `json:"effects"`
			CurrentTurn int                     `json:"current_turn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.Narrative != "你找到了一把钥匙。" ||
		response.Data.CurrentTurn != 4 || response.Data.DiceRoll == nil ||
		response.Data.DiceRoll.Result != 17 || response.Data.Effects == nil ||
		len(response.Data.Effects.Items) != 1 {
		t.Fatalf("response = %#v", response)
	}
}

func TestGameHandlerSubmitActionRequiresAuthenticationContext(t *testing.T) {
	for _, identity := range []any{nil, "7", uint(0)} {
		fakeService := &fakeGameStartService{}
		router := submitActionTestRouter(NewGameHandler(fakeService), identity)
		request := httptest.NewRequest(
			http.MethodPost,
			"/games/41/action",
			bytes.NewBufferString(`{"request_id":"550e8400-e29b-41d4-a716-446655440000","expected_turn":0,"action_text":"行动"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
		if fakeService.actionRequest != nil {
			t.Fatal("invalid authentication context reached service")
		}
	}
}

func TestGameHandlerSubmitActionRejectsInvalidContract(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{"invalid room ID", "/games/not-a-number/action", validActionBody()},
		{"zero room ID", "/games/0/action", validActionBody()},
		{"malformed JSON", "/games/41/action", `not-json`},
		{"missing request ID", "/games/41/action", `{"expected_turn":0,"action_text":"行动"}`},
		{"missing expected turn", "/games/41/action", `{"request_id":"550e8400-e29b-41d4-a716-446655440000","action_text":"行动"}`},
		{"negative expected turn", "/games/41/action", `{"request_id":"550e8400-e29b-41d4-a716-446655440000","expected_turn":-1,"action_text":"行动"}`},
		{"missing action", "/games/41/action", `{"request_id":"550e8400-e29b-41d4-a716-446655440000","expected_turn":0}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{}
			router := submitActionTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusBadRequest, 1310)
			if fakeService.actionRequest != nil {
				t.Fatal("invalid contract reached service")
			}
		})
	}
}

func TestGameHandlerSubmitActionMapsSafeServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"invalid action", service.ErrInvalidGameAction, http.StatusBadRequest, 1310},
		{"room not found", service.ErrGameRoomNotFound, http.StatusNotFound, 1311},
		{"player not found", service.ErrGamePlayerNotFound, http.StatusNotFound, 1312},
		{"room not playing", service.ErrGameRoomNotPlaying, http.StatusConflict, 1313},
		{"turn conflict", service.ErrGameActionConflict, http.StatusConflict, 1314},
		{"request conflict", service.ErrActionRequestConflict, http.StatusConflict, 1315},
		{"insufficient items", service.ErrInsufficientItems, http.StatusConflict, 1316},
		{"AI unavailable", fmt.Errorf("%w: sensitive upstream", service.ErrAIUnavailable), http.StatusServiceUnavailable, 1317},
		{"empty narrative", service.ErrEmptyActionNarrative, http.StatusServiceUnavailable, 1317},
		{"runtime unavailable", service.ErrGameRuntimeUnavailable, http.StatusServiceUnavailable, 1318},
		{"invalid effects", service.ErrInvalidActionEffects, http.StatusBadGateway, 1319},
		{"internal", fmt.Errorf("%w: sensitive mysql", service.ErrInternal), http.StatusInternalServerError, 1320},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{actionErr: test.err}
			router := submitActionTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/action", bytes.NewBufferString(validActionBody()))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, test.wantStatus, test.wantCode)
			if bytes.Contains(recorder.Body.Bytes(), []byte("sensitive")) {
				t.Fatalf("response leaked wrapped error detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestGameHandlerSubmitActionRejectsInvalidServiceResult(t *testing.T) {
	tests := []*service.SubmitGameActionResult{
		nil,
		{},
		{Narrative: "叙事", CurrentTurn: 1},
		{Narrative: "叙事", Effects: &service.ActionEffects{}},
	}
	for index, result := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fakeService := &fakeGameStartService{actionResult: result}
			router := submitActionTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/action", bytes.NewBufferString(validActionBody()))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, 1320)
		})
	}
}

func validSubmitActionResult() *service.SubmitGameActionResult {
	return &service.SubmitGameActionResult{
		Narrative: "你找到了一把钥匙。",
		DiceRoll: &service.ActionDiceRoll{
			Type: "D20", Result: 17, Target: 12, Success: true,
			Description: "检定成功", Reason: "调查书房",
		},
		Effects: &service.ActionEffects{
			PlayerStateChanges: map[string]string{},
			Items:              []service.ItemMutation{{Name: "钥匙", QuantityDelta: 1}},
			Buffs:              []service.BuffMutation{}, Events: []service.KeyEventMutation{},
		},
		CurrentTurn: 4,
	}
}

func validActionBody() string {
	return `{"request_id":"550e8400-e29b-41d4-a716-446655440000","expected_turn":0,"action_text":"行动"}`
}

func submitActionTestRouter(handler *GameHandler, identity any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/games/:roomId/action", func(c *gin.Context) {
		if identity != nil {
			c.Set("user_id", identity)
		}
		handler.SubmitAction(c)
	})
	return router
}
