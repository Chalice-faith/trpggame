package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	saveResult    *service.CreateManualSaveResult
	saveErr       error
	saveRequest   *service.CreateManualSaveRequest
	listResult    *service.ListGameSavesResult
	listErr       error
	listRequest   *service.ListGameSavesRequest
	pauseResult   *service.PauseGameResult
	pauseErr      error
	pauseRequest  *service.PauseGameRequest
	resumeResult  *service.ResumeGameResult
	resumeErr     error
	resumeRequest *service.ResumeGameRequest
	loadResult    *service.LoadGameResult
	loadErr       error
	loadRequest   *service.LoadGameRequest
	endResult     *service.EndGameResult
	endErr        error
	endRequest    *service.EndGameRequest
}

func (s *fakeGameStartService) EndGame(
	_ context.Context,
	req *service.EndGameRequest,
) (*service.EndGameResult, error) {
	s.endRequest = req
	return s.endResult, s.endErr
}

func (s *fakeGameStartService) LoadGame(
	_ context.Context,
	req *service.LoadGameRequest,
) (*service.LoadGameResult, error) {
	s.loadRequest = req
	return s.loadResult, s.loadErr
}

func (s *fakeGameStartService) ResumeGame(
	_ context.Context,
	req *service.ResumeGameRequest,
) (*service.ResumeGameResult, error) {
	s.resumeRequest = req
	return s.resumeResult, s.resumeErr
}

func (s *fakeGameStartService) PauseGame(
	_ context.Context,
	req *service.PauseGameRequest,
) (*service.PauseGameResult, error) {
	s.pauseRequest = req
	return s.pauseResult, s.pauseErr
}

func (s *fakeGameStartService) ListGameSaves(
	_ context.Context,
	req *service.ListGameSavesRequest,
) (*service.ListGameSavesResult, error) {
	s.listRequest = req
	return s.listResult, s.listErr
}

func (s *fakeGameStartService) CreateManualSave(
	_ context.Context,
	req *service.CreateManualSaveRequest,
) (*service.CreateManualSaveResult, error) {
	s.saveRequest = req
	return s.saveResult, s.saveErr
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

func TestGameHandlerManualSave(t *testing.T) {
	fakeService := &fakeGameStartService{saveResult: &service.CreateManualSaveResult{
		Save: &model.GameSave{ID: 91, RoomID: 41, SaveName: "进入书房前"},
	}}
	router := manualSaveTestRouter(NewGameHandler(fakeService), uint(7))
	request := httptest.NewRequest(
		http.MethodPost,
		"/games/41/save",
		bytes.NewBufferString(`{"save_name":" 进入书房前 "}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if fakeService.saveRequest == nil || fakeService.saveRequest.UserID != 7 ||
		fakeService.saveRequest.RoomID != 41 || fakeService.saveRequest.SaveName != " 进入书房前 " {
		t.Fatalf("service request = %#v", fakeService.saveRequest)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			SaveID uint `json:"save_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.SaveID != 91 {
		t.Fatalf("response = %#v", response)
	}
}

func TestGameHandlerManualSaveRequiresAuthenticationContext(t *testing.T) {
	for _, identity := range []any{nil, "7", uint(0)} {
		fakeService := &fakeGameStartService{}
		router := manualSaveTestRouter(NewGameHandler(fakeService), identity)
		request := httptest.NewRequest(
			http.MethodPost,
			"/games/41/save",
			bytes.NewBufferString(`{"save_name":"手动存档"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
		if fakeService.saveRequest != nil {
			t.Fatal("invalid authentication context reached service")
		}
	}
}

func TestGameHandlerManualSaveRejectsInvalidContract(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{"invalid room ID", "/games/not-a-number/save", `{"save_name":"存档"}`},
		{"zero room ID", "/games/0/save", `{"save_name":"存档"}`},
		{"malformed JSON", "/games/41/save", `not-json`},
		{"missing save name", "/games/41/save", `{}`},
		{"empty save name", "/games/41/save", `{"save_name":""}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{}
			router := manualSaveTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusBadRequest, 1321)
			if fakeService.saveRequest != nil {
				t.Fatal("invalid contract reached service")
			}
		})
	}
}

func TestGameHandlerManualSaveMapsSafeServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"invalid save", service.ErrInvalidGameSave, http.StatusBadRequest, 1321},
		{"room not found", service.ErrGameRoomNotFound, http.StatusNotFound, 1311},
		{"room not savable", service.ErrGameRoomNotSavable, http.StatusConflict, 1322},
		{
			"runtime unavailable",
			fmt.Errorf("%w: sensitive Redis detail", service.ErrGameRuntimeUnavailable),
			http.StatusServiceUnavailable,
			1318,
		},
		{
			"internal",
			fmt.Errorf("%w: sensitive MySQL detail", service.ErrInternal),
			http.StatusInternalServerError,
			1323,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{saveErr: test.err}
			router := manualSaveTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(
				http.MethodPost,
				"/games/41/save",
				bytes.NewBufferString(`{"save_name":"手动存档"}`),
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

func TestGameHandlerManualSaveRejectsInvalidServiceResult(t *testing.T) {
	tests := []*service.CreateManualSaveResult{
		nil,
		{},
		{Save: &model.GameSave{RoomID: 41}},
		{Save: &model.GameSave{ID: 91, RoomID: 42}},
		{Save: &model.GameSave{ID: 91, RoomID: 41, IsAuto: true}},
	}
	for index, result := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fakeService := &fakeGameStartService{saveResult: result}
			router := manualSaveTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(
				http.MethodPost,
				"/games/41/save",
				bytes.NewBufferString(`{"save_name":"手动存档"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, 1323)
		})
	}
}

func manualSaveTestRouter(handler *GameHandler, identity any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/games/:roomId/save", func(c *gin.Context) {
		if identity != nil {
			c.Set("user_id", identity)
		}
		handler.ManualSave(c)
	})
	return router
}

func TestGameHandlerListSaves(t *testing.T) {
	createdAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	fakeService := &fakeGameStartService{listResult: &service.ListGameSavesResult{
		Items: []service.GameSaveSummary{
			{ID: 92, SaveName: "自动存档-10", RoundNumber: 10, IsAuto: true, CreatedAt: createdAt},
			{ID: 91, SaveName: "进入书房前", RoundNumber: 3, CreatedAt: createdAt.Add(-time.Hour)},
		},
		Total: 2,
	}}
	router := listSavesTestRouter(NewGameHandler(fakeService), uint(7))
	request := httptest.NewRequest(http.MethodGet, "/games/41/saves", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.listRequest == nil || fakeService.listRequest.UserID != 7 ||
		fakeService.listRequest.RoomID != 41 {
		t.Fatalf("service request = %#v", fakeService.listRequest)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			Items []service.GameSaveSummary `json:"items"`
			Total int                       `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.Total != 2 || len(response.Data.Items) != 2 ||
		response.Data.Items[0].ID != 92 || response.Data.Items[0].SaveName != "自动存档-10" ||
		!response.Data.Items[0].IsAuto || !response.Data.Items[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("response = %#v", response)
	}
}

func TestGameHandlerListSavesReturnsStableEmptyList(t *testing.T) {
	fakeService := &fakeGameStartService{listResult: &service.ListGameSavesResult{
		Items: []service.GameSaveSummary{}, Total: 0,
	}}
	router := listSavesTestRouter(NewGameHandler(fakeService), uint(7))
	request := httptest.NewRequest(http.MethodGet, "/games/41/saves", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"items":[]`)) ||
		!bytes.Contains(recorder.Body.Bytes(), []byte(`"total":0`)) {
		t.Fatalf("empty response is unstable: %s", recorder.Body.String())
	}
}

func TestGameHandlerListSavesRequiresAuthenticationContext(t *testing.T) {
	for _, identity := range []any{nil, "7", uint(0)} {
		fakeService := &fakeGameStartService{}
		router := listSavesTestRouter(NewGameHandler(fakeService), identity)
		request := httptest.NewRequest(http.MethodGet, "/games/41/saves", nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
		if fakeService.listRequest != nil {
			t.Fatal("invalid authentication context reached service")
		}
	}
}

func TestGameHandlerListSavesRejectsInvalidRoomID(t *testing.T) {
	for _, path := range []string{"/games/not-a-number/saves", "/games/0/saves"} {
		fakeService := &fakeGameStartService{}
		router := listSavesTestRouter(NewGameHandler(fakeService), uint(7))
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusBadRequest, 1324)
		if fakeService.listRequest != nil {
			t.Fatal("invalid room ID reached service")
		}
	}
}

func TestGameHandlerListSavesMapsSafeServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"invalid query", service.ErrInvalidGameSaveQuery, http.StatusBadRequest, 1324},
		{"room not found", service.ErrGameRoomNotFound, http.StatusNotFound, 1311},
		{
			"internal",
			fmt.Errorf("%w: sensitive MySQL detail", service.ErrInternal),
			http.StatusInternalServerError,
			1325,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{listErr: test.err}
			router := listSavesTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodGet, "/games/41/saves", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, test.wantStatus, test.wantCode)
			if bytes.Contains(recorder.Body.Bytes(), []byte("sensitive")) {
				t.Fatalf("response leaked wrapped error detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestGameHandlerListSavesRejectsInvalidServiceResult(t *testing.T) {
	createdAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []*service.ListGameSavesResult{
		nil,
		{},
		{Items: []service.GameSaveSummary{}, Total: 1},
		{Items: []service.GameSaveSummary{{SaveName: "存档", CreatedAt: createdAt}}, Total: 1},
		{Items: []service.GameSaveSummary{{ID: 91, SaveName: " ", CreatedAt: createdAt}}, Total: 1},
		{Items: []service.GameSaveSummary{{ID: 91, SaveName: "存档", RoundNumber: -1, CreatedAt: createdAt}}, Total: 1},
		{Items: []service.GameSaveSummary{{ID: 91, SaveName: "存档"}}, Total: 1},
	}
	for index, result := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fakeService := &fakeGameStartService{listResult: result}
			router := listSavesTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodGet, "/games/41/saves", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, 1325)
		})
	}
}

func listSavesTestRouter(handler *GameHandler, identity any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/games/:roomId/saves", func(c *gin.Context) {
		if identity != nil {
			c.Set("user_id", identity)
		}
		handler.ListSaves(c)
	})
	return router
}

func TestGameHandlerPauseGame(t *testing.T) {
	fakeService := &fakeGameStartService{pauseResult: &service.PauseGameResult{
		RoomID: 41, Status: model.RoomStatusPaused,
	}}
	router := pauseGameTestRouter(NewGameHandler(fakeService), uint(7))
	request := httptest.NewRequest(http.MethodPost, "/games/41/pause", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.pauseRequest == nil || fakeService.pauseRequest.UserID != 7 ||
		fakeService.pauseRequest.RoomID != 41 {
		t.Fatalf("service request = %#v", fakeService.pauseRequest)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			RoomID uint             `json:"room_id"`
			Status model.RoomStatus `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.RoomID != 41 || response.Data.Status != model.RoomStatusPaused {
		t.Fatalf("response = %#v", response)
	}
}

func TestGameHandlerPauseGameRequiresAuthenticationContext(t *testing.T) {
	for _, identity := range []any{nil, "7", uint(0)} {
		fakeService := &fakeGameStartService{}
		router := pauseGameTestRouter(NewGameHandler(fakeService), identity)
		request := httptest.NewRequest(http.MethodPost, "/games/41/pause", nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
		if fakeService.pauseRequest != nil {
			t.Fatal("invalid authentication context reached service")
		}
	}
}

func TestGameHandlerPauseGameRejectsInvalidRoomID(t *testing.T) {
	for _, path := range []string{"/games/not-a-number/pause", "/games/0/pause"} {
		fakeService := &fakeGameStartService{}
		router := pauseGameTestRouter(NewGameHandler(fakeService), uint(7))
		request := httptest.NewRequest(http.MethodPost, path, nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusBadRequest, 1326)
		if fakeService.pauseRequest != nil {
			t.Fatal("invalid room ID reached service")
		}
	}
}

func TestGameHandlerPauseGameMapsSafeServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"invalid pause", service.ErrInvalidGamePause, http.StatusBadRequest, 1326},
		{"room not found", service.ErrGameRoomNotFound, http.StatusNotFound, 1311},
		{"room not pausable", service.ErrGameRoomNotPausable, http.StatusConflict, 1327},
		{
			"runtime unavailable",
			fmt.Errorf("%w: sensitive Redis detail", service.ErrGameRuntimeUnavailable),
			http.StatusServiceUnavailable,
			1318,
		},
		{
			"internal",
			fmt.Errorf("%w: sensitive MySQL detail", service.ErrInternal),
			http.StatusInternalServerError,
			1328,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{pauseErr: test.err}
			router := pauseGameTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/pause", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, test.wantStatus, test.wantCode)
			if bytes.Contains(recorder.Body.Bytes(), []byte("sensitive")) {
				t.Fatalf("response leaked wrapped error detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestGameHandlerPauseGameRejectsInvalidServiceResult(t *testing.T) {
	tests := []*service.PauseGameResult{
		nil,
		{},
		{RoomID: 42, Status: model.RoomStatusPaused},
		{RoomID: 41, Status: model.RoomStatusPlaying},
	}
	for index, result := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fakeService := &fakeGameStartService{pauseResult: result}
			router := pauseGameTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/pause", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, 1328)
		})
	}
}

func pauseGameTestRouter(handler *GameHandler, identity any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/games/:roomId/pause", func(c *gin.Context) {
		if identity != nil {
			c.Set("user_id", identity)
		}
		handler.PauseGame(c)
	})
	return router
}

func TestGameHandlerResumeGame(t *testing.T) {
	fakeService := &fakeGameStartService{resumeResult: &service.ResumeGameResult{
		RoomID: 41, Status: model.RoomStatusPlaying,
	}}
	router := resumeGameTestRouter(NewGameHandler(fakeService), uint(7))
	request := httptest.NewRequest(http.MethodPost, "/games/41/resume", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.resumeRequest == nil || fakeService.resumeRequest.UserID != 7 ||
		fakeService.resumeRequest.RoomID != 41 {
		t.Fatalf("service request = %#v", fakeService.resumeRequest)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			RoomID uint             `json:"room_id"`
			Status model.RoomStatus `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.RoomID != 41 || response.Data.Status != model.RoomStatusPlaying {
		t.Fatalf("response = %#v", response)
	}
}

func TestGameHandlerResumeGameRequiresAuthenticationContext(t *testing.T) {
	for _, identity := range []any{nil, "7", uint(0)} {
		fakeService := &fakeGameStartService{}
		router := resumeGameTestRouter(NewGameHandler(fakeService), identity)
		request := httptest.NewRequest(http.MethodPost, "/games/41/resume", nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
		if fakeService.resumeRequest != nil {
			t.Fatal("invalid authentication context reached service")
		}
	}
}

func TestGameHandlerResumeGameRejectsInvalidRoomID(t *testing.T) {
	for _, path := range []string{"/games/not-a-number/resume", "/games/0/resume"} {
		fakeService := &fakeGameStartService{}
		router := resumeGameTestRouter(NewGameHandler(fakeService), uint(7))
		request := httptest.NewRequest(http.MethodPost, path, nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusBadRequest, 1329)
		if fakeService.resumeRequest != nil {
			t.Fatal("invalid room ID reached service")
		}
	}
}

func TestGameHandlerResumeGameMapsSafeServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"invalid resume", service.ErrInvalidGameResume, http.StatusBadRequest, 1329},
		{"room not found", service.ErrGameRoomNotFound, http.StatusNotFound, 1311},
		{"room not resumable", service.ErrGameRoomNotResumable, http.StatusConflict, 1330},
		{
			"runtime unavailable",
			fmt.Errorf("%w: sensitive Redis detail", service.ErrGameRuntimeUnavailable),
			http.StatusServiceUnavailable,
			1318,
		},
		{
			"internal",
			fmt.Errorf("%w: sensitive MySQL detail", service.ErrInternal),
			http.StatusInternalServerError,
			1331,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{resumeErr: test.err}
			router := resumeGameTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/resume", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, test.wantStatus, test.wantCode)
			if bytes.Contains(recorder.Body.Bytes(), []byte("sensitive")) {
				t.Fatalf("response leaked wrapped error detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestGameHandlerResumeGameRejectsInvalidServiceResult(t *testing.T) {
	tests := []*service.ResumeGameResult{
		nil,
		{},
		{RoomID: 42, Status: model.RoomStatusPlaying},
		{RoomID: 41, Status: model.RoomStatusPaused},
	}
	for index, result := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fakeService := &fakeGameStartService{resumeResult: result}
			router := resumeGameTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/resume", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, 1331)
		})
	}
}

func resumeGameTestRouter(handler *GameHandler, identity any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/games/:roomId/resume", func(c *gin.Context) {
		if identity != nil {
			c.Set("user_id", identity)
		}
		handler.ResumeGame(c)
	})
	return router
}

func TestGameHandlerLoadGame(t *testing.T) {
	fakeService := &fakeGameStartService{loadResult: &service.LoadGameResult{
		RoomID: 41, SaveID: 91, Status: model.RoomStatusPaused, Turn: 3,
	}}
	router := loadGameTestRouter(NewGameHandler(fakeService), uint(7))
	request := httptest.NewRequest(http.MethodPost, "/games/41/load", bytes.NewBufferString(`{"save_id":91}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.loadRequest == nil || fakeService.loadRequest.UserID != 7 ||
		fakeService.loadRequest.RoomID != 41 || fakeService.loadRequest.SaveID != 91 {
		t.Fatalf("service request = %#v", fakeService.loadRequest)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			RoomID uint             `json:"room_id"`
			SaveID uint             `json:"save_id"`
			Status model.RoomStatus `json:"status"`
			Turn   int              `json:"turn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.RoomID != 41 || response.Data.SaveID != 91 ||
		response.Data.Status != model.RoomStatusPaused || response.Data.Turn != 3 {
		t.Fatalf("response = %#v", response)
	}
}

func TestGameHandlerLoadGameRequiresAuthenticationContext(t *testing.T) {
	for _, identity := range []any{nil, "7", uint(0)} {
		fakeService := &fakeGameStartService{}
		router := loadGameTestRouter(NewGameHandler(fakeService), identity)
		request := httptest.NewRequest(http.MethodPost, "/games/41/load", bytes.NewBufferString(`{"save_id":91}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
		if fakeService.loadRequest != nil {
			t.Fatal("invalid authentication context reached service")
		}
	}
}

func TestGameHandlerLoadGameRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		path string
		body string
	}{
		{"/games/not-a-number/load", `{"save_id":91}`},
		{"/games/0/load", `{"save_id":91}`},
		{"/games/41/load", ``},
		{"/games/41/load", `{`},
		{"/games/41/load", `{}`},
		{"/games/41/load", `{"save_id":0}`},
	}
	for _, test := range tests {
		fakeService := &fakeGameStartService{}
		router := loadGameTestRouter(NewGameHandler(fakeService), uint(7))
		request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusBadRequest, 1332)
		if fakeService.loadRequest != nil {
			t.Fatal("invalid load request reached service")
		}
	}
}

func TestGameHandlerLoadGameMapsSafeServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"invalid load", service.ErrInvalidGameLoad, http.StatusBadRequest, 1332},
		{"room not found", service.ErrGameRoomNotFound, http.StatusNotFound, 1311},
		{"save not found", service.ErrGameSaveNotFound, http.StatusNotFound, 1333},
		{"corrupt save", service.ErrGameSaveCorrupt, http.StatusConflict, 1334},
		{"room not loadable", service.ErrGameRoomNotLoadable, http.StatusConflict, 1335},
		{
			"runtime unavailable", fmt.Errorf("%w: sensitive Redis detail", service.ErrGameRuntimeUnavailable),
			http.StatusServiceUnavailable, 1318,
		},
		{
			"internal", fmt.Errorf("%w: sensitive MySQL detail", service.ErrInternal),
			http.StatusInternalServerError, 1336,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{loadErr: test.err}
			router := loadGameTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/load", bytes.NewBufferString(`{"save_id":91}`))
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

func TestGameHandlerLoadGameRejectsInvalidServiceResult(t *testing.T) {
	tests := []*service.LoadGameResult{
		nil,
		{},
		{RoomID: 42, SaveID: 91, Status: model.RoomStatusPaused, Turn: 3},
		{RoomID: 41, SaveID: 92, Status: model.RoomStatusPaused, Turn: 3},
		{RoomID: 41, SaveID: 91, Status: model.RoomStatusPlaying, Turn: 3},
		{RoomID: 41, SaveID: 91, Status: model.RoomStatusPaused, Turn: -1},
	}
	for index, result := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fakeService := &fakeGameStartService{loadResult: result}
			router := loadGameTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/load", bytes.NewBufferString(`{"save_id":91}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, 1336)
		})
	}
}

func loadGameTestRouter(handler *GameHandler, identity any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/games/:roomId/load", func(c *gin.Context) {
		if identity != nil {
			c.Set("user_id", identity)
		}
		handler.LoadGame(c)
	})
	return router
}

func TestGameHandlerEndGame(t *testing.T) {
	fakeService := &fakeGameStartService{endResult: &service.EndGameResult{
		RoomID: 41, Status: model.RoomStatusEnded,
	}}
	router := endGameTestRouter(NewGameHandler(fakeService), uint(7))
	request := httptest.NewRequest(http.MethodPost, "/games/41/end", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.endRequest == nil || fakeService.endRequest.UserID != 7 ||
		fakeService.endRequest.RoomID != 41 {
		t.Fatalf("service request = %#v", fakeService.endRequest)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			RoomID uint             `json:"room_id"`
			Status model.RoomStatus `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Data.RoomID != 41 || response.Data.Status != model.RoomStatusEnded {
		t.Fatalf("response = %#v", response)
	}
}

func TestGameHandlerEndGameRequiresAuthenticationContext(t *testing.T) {
	for _, identity := range []any{nil, "7", uint(0)} {
		fakeService := &fakeGameStartService{}
		router := endGameTestRouter(NewGameHandler(fakeService), identity)
		request := httptest.NewRequest(http.MethodPost, "/games/41/end", nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
		if fakeService.endRequest != nil {
			t.Fatal("invalid authentication context reached service")
		}
	}
}

func TestGameHandlerEndGameRejectsInvalidRoomID(t *testing.T) {
	for _, path := range []string{"/games/not-a-number/end", "/games/0/end"} {
		fakeService := &fakeGameStartService{}
		router := endGameTestRouter(NewGameHandler(fakeService), uint(7))
		request := httptest.NewRequest(http.MethodPost, path, nil)
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		assertJSONError(t, recorder, http.StatusBadRequest, 1337)
		if fakeService.endRequest != nil {
			t.Fatal("invalid room ID reached service")
		}
	}
}

func TestGameHandlerEndGameMapsSafeServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"invalid end", service.ErrInvalidGameEnd, http.StatusBadRequest, 1337},
		{"room not found", service.ErrGameRoomNotFound, http.StatusNotFound, 1311},
		{"room not endable", service.ErrGameRoomNotEndable, http.StatusConflict, 1338},
		{
			"runtime unavailable", fmt.Errorf("%w: sensitive Redis detail", service.ErrGameRuntimeUnavailable),
			http.StatusServiceUnavailable, 1318,
		},
		{
			"internal", fmt.Errorf("%w: sensitive MySQL detail", service.ErrInternal),
			http.StatusInternalServerError, 1339,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeService := &fakeGameStartService{endErr: test.err}
			router := endGameTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/end", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, test.wantStatus, test.wantCode)
			if bytes.Contains(recorder.Body.Bytes(), []byte("sensitive")) {
				t.Fatalf("response leaked wrapped error detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestGameHandlerEndGameRejectsInvalidServiceResult(t *testing.T) {
	tests := []*service.EndGameResult{
		nil,
		{},
		{RoomID: 42, Status: model.RoomStatusEnded},
		{RoomID: 41, Status: model.RoomStatusPaused},
	}
	for index, result := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			fakeService := &fakeGameStartService{endResult: result}
			router := endGameTestRouter(NewGameHandler(fakeService), uint(7))
			request := httptest.NewRequest(http.MethodPost, "/games/41/end", nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, http.StatusInternalServerError, 1339)
		})
	}
}

func endGameTestRouter(handler *GameHandler, identity any) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/games/:roomId/end", func(c *gin.Context) {
		if identity != nil {
			c.Set("user_id", identity)
		}
		handler.EndGame(c)
	})
	return router
}
