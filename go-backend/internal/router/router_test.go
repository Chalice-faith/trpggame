package router

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"trpggame/internal/config"
	"trpggame/internal/handler"
	"trpggame/internal/middleware"
	"trpggame/internal/model"
	"trpggame/internal/service"
)

type routerGameStartService struct {
	request       *service.StartSoloGameRequest
	actionRequest *service.SubmitGameActionRequest
	saveRequest   *service.CreateManualSaveRequest
	listRequest   *service.ListGameSavesRequest
	pauseRequest  *service.PauseGameRequest
	resumeRequest *service.ResumeGameRequest
	loadRequest   *service.LoadGameRequest
	endRequest    *service.EndGameRequest
}

func TestSetupRegistersIndependentWebSocketRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	engine := Setup(
		cfg,
		nil,
		WebSocketHandlers{
			Game: func(c *gin.Context) { c.String(http.StatusOK, "game") },
			IM:   func(c *gin.Context) { c.String(http.StatusOK, "im") },
		},
		nil,
		nil,
		nil,
	)

	for _, tt := range []struct {
		path string
		body string
	}{
		{path: "/ws", body: "game"},
		{path: "/ws/im", body: "im"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, tt.path, nil)
		engine.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || recorder.Body.String() != tt.body {
			t.Fatalf("GET %s = status %d body %q", tt.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestSetupDoesNotLogWebSocketQueryTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousWriter := gin.DefaultWriter
	var output bytes.Buffer
	gin.DefaultWriter = &output
	t.Cleanup(func() { gin.DefaultWriter = previousWriter })

	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	handlerFunc := func(c *gin.Context) {
		if c.Query("result") == "success" {
			c.Status(http.StatusNoContent)
			return
		}
		c.Status(http.StatusUnauthorized)
	}
	engine := Setup(
		cfg,
		nil,
		WebSocketHandlers{Game: handlerFunc, IM: handlerFunc},
		nil,
		nil,
		nil,
	)

	for _, test := range []struct {
		name   string
		path   string
		query  string
		status int
	}{
		{name: "game failure", path: "/ws", query: "token=game-failure-secret", status: http.StatusUnauthorized},
		{name: "game success", path: "/ws", query: "token=game-success-secret&result=success", status: http.StatusNoContent},
		{name: "IM failure", path: "/ws/im", query: "token=im-failure-secret", status: http.StatusUnauthorized},
		{name: "IM success", path: "/ws/im", query: "token=im-success-secret&result=success", status: http.StatusNoContent},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, test.path+"?"+test.query, nil)
		engine.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s status = %d, want %d", test.name, recorder.Code, test.status)
		}
	}
	for _, secretToken := range []string{
		"game-failure-secret",
		"game-success-secret",
		"im-failure-secret",
		"im-success-secret",
	} {
		if strings.Contains(output.String(), secretToken) {
			t.Fatalf("WebSocket token %q leaked into access log: %s", secretToken, output.String())
		}
	}

	restRequest := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml?probe=rest", nil)
	engine.ServeHTTP(httptest.NewRecorder(), restRequest)
	if !strings.Contains(output.String(), "probe=rest") {
		t.Fatalf("REST access log was unexpectedly disabled: %s", output.String())
	}
}

func (s *routerGameStartService) EndGame(
	_ context.Context,
	req *service.EndGameRequest,
) (*service.EndGameResult, error) {
	s.endRequest = req
	return &service.EndGameResult{RoomID: req.RoomID, Status: model.RoomStatusEnded}, nil
}

func (s *routerGameStartService) LoadGame(
	_ context.Context,
	req *service.LoadGameRequest,
) (*service.LoadGameResult, error) {
	s.loadRequest = req
	return &service.LoadGameResult{
		RoomID: req.RoomID, SaveID: req.SaveID, Status: model.RoomStatusPaused, Turn: 3,
	}, nil
}

func (s *routerGameStartService) ResumeGame(
	_ context.Context,
	req *service.ResumeGameRequest,
) (*service.ResumeGameResult, error) {
	s.resumeRequest = req
	return &service.ResumeGameResult{RoomID: req.RoomID, Status: model.RoomStatusPlaying}, nil
}

func (s *routerGameStartService) PauseGame(
	_ context.Context,
	req *service.PauseGameRequest,
) (*service.PauseGameResult, error) {
	s.pauseRequest = req
	return &service.PauseGameResult{RoomID: req.RoomID, Status: model.RoomStatusPaused}, nil
}

func (s *routerGameStartService) ListGameSaves(
	_ context.Context,
	req *service.ListGameSavesRequest,
) (*service.ListGameSavesResult, error) {
	s.listRequest = req
	return &service.ListGameSavesResult{Items: []service.GameSaveSummary{}, Total: 0}, nil
}

func (s *routerGameStartService) CreateManualSave(
	_ context.Context,
	req *service.CreateManualSaveRequest,
) (*service.CreateManualSaveResult, error) {
	s.saveRequest = req
	return &service.CreateManualSaveResult{
		Save: &model.GameSave{ID: 91, RoomID: req.RoomID, SaveName: req.SaveName},
	}, nil
}

func (s *routerGameStartService) SubmitAction(
	_ context.Context,
	req *service.SubmitGameActionRequest,
) (*service.SubmitGameActionResult, error) {
	s.actionRequest = req
	return &service.SubmitGameActionResult{
		Narrative: "行动结果",
		Effects: &service.ActionEffects{
			PlayerStateChanges: map[string]string{},
			Items:              []service.ItemMutation{}, Buffs: []service.BuffMutation{}, Events: []service.KeyEventMutation{},
		},
		CurrentTurn: req.ExpectedTurn + 1,
	}, nil
}

func (s *routerGameStartService) StartSoloGame(
	_ context.Context,
	req *service.StartSoloGameRequest,
) (*service.StartSoloGameResult, error) {
	s.request = req
	return &service.StartSoloGameResult{
		Room:             &model.GameRoom{ID: 41, Status: model.RoomStatusPlaying},
		OpeningNarrative: "开场",
	}, nil
}

func TestSetupRegistersAuthenticatedSoloStartRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:         "router-test-secret",
			AccessTokenTTL: 15,
		},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	gameService := &routerGameStartService{}
	engine := Setup(
		cfg,
		nil,
		WebSocketHandlers{},
		nil,
		nil,
		handler.NewGameHandler(gameService),
	)

	unauthorized := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/games/solo/start",
		bytes.NewBufferString(`{"script_id":11,"character_id":13}`),
	)
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	token, err := middleware.GenerateToken(7, "investigator", cfg.JWT.Secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authorized := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/games/solo/start",
		bytes.NewBufferString(`{"script_id":11,"character_id":13}`),
	)
	authorized.Header.Set("Content-Type", "application/json")
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(authorizedRecorder, authorized)

	if authorizedRecorder.Code != http.StatusCreated {
		t.Fatalf(
			"authorized status = %d, want %d; body = %s",
			authorizedRecorder.Code,
			http.StatusCreated,
			authorizedRecorder.Body.String(),
		)
	}
	if gameService.request == nil || gameService.request.UserID != 7 ||
		gameService.request.ScriptID != 11 || gameService.request.CharacterID != 13 {
		t.Fatalf("game service request = %#v", gameService.request)
	}
}

func TestSetupRegistersAuthenticatedSubmitActionRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	gameService := &routerGameStartService{}
	engine := Setup(cfg, nil, WebSocketHandlers{}, nil, nil, handler.NewGameHandler(gameService))
	body := `{"request_id":"550e8400-e29b-41d4-a716-446655440000","expected_turn":3,"action_text":"调查书房"}`

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/action", bytes.NewBufferString(body))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	token, err := middleware.GenerateToken(7, "investigator", cfg.JWT.Secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/action", bytes.NewBufferString(body))
	authorized.Header.Set("Content-Type", "application/json")
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(authorizedRecorder, authorized)

	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d; body = %s", authorizedRecorder.Code, http.StatusOK, authorizedRecorder.Body.String())
	}
	if gameService.actionRequest == nil || gameService.actionRequest.UserID != 7 ||
		gameService.actionRequest.RoomID != 41 || gameService.actionRequest.ExpectedTurn != 3 ||
		gameService.actionRequest.Action != "调查书房" {
		t.Fatalf("game action request = %#v", gameService.actionRequest)
	}
}

func TestSetupRegistersAuthenticatedManualSaveRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	gameService := &routerGameStartService{}
	engine := Setup(cfg, nil, WebSocketHandlers{}, nil, nil, handler.NewGameHandler(gameService))
	body := `{"save_name":"进入书房前"}`

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/save", bytes.NewBufferString(body))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	token, err := middleware.GenerateToken(7, "investigator", cfg.JWT.Secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/save", bytes.NewBufferString(body))
	authorized.Header.Set("Content-Type", "application/json")
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(authorizedRecorder, authorized)

	if authorizedRecorder.Code != http.StatusCreated {
		t.Fatalf("authorized status = %d, want %d; body = %s", authorizedRecorder.Code, http.StatusCreated, authorizedRecorder.Body.String())
	}
	if gameService.saveRequest == nil || gameService.saveRequest.UserID != 7 ||
		gameService.saveRequest.RoomID != 41 || gameService.saveRequest.SaveName != "进入书房前" {
		t.Fatalf("manual save request = %#v", gameService.saveRequest)
	}
}

func TestSetupRegistersAuthenticatedListSavesRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	gameService := &routerGameStartService{}
	engine := Setup(cfg, nil, WebSocketHandlers{}, nil, nil, handler.NewGameHandler(gameService))

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/v1/games/41/saves", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	token, err := middleware.GenerateToken(7, "investigator", cfg.JWT.Secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authorized := httptest.NewRequest(http.MethodGet, "/api/v1/games/41/saves", nil)
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(authorizedRecorder, authorized)

	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d; body = %s", authorizedRecorder.Code, http.StatusOK, authorizedRecorder.Body.String())
	}
	if gameService.listRequest == nil || gameService.listRequest.UserID != 7 ||
		gameService.listRequest.RoomID != 41 {
		t.Fatalf("list saves request = %#v", gameService.listRequest)
	}
}

func TestSetupRegistersAuthenticatedPauseGameRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	gameService := &routerGameStartService{}
	engine := Setup(cfg, nil, WebSocketHandlers{}, nil, nil, handler.NewGameHandler(gameService))

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/pause", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	token, err := middleware.GenerateToken(7, "investigator", cfg.JWT.Secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/pause", nil)
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(authorizedRecorder, authorized)

	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d; body = %s", authorizedRecorder.Code, http.StatusOK, authorizedRecorder.Body.String())
	}
	if gameService.pauseRequest == nil || gameService.pauseRequest.UserID != 7 ||
		gameService.pauseRequest.RoomID != 41 {
		t.Fatalf("pause request = %#v", gameService.pauseRequest)
	}
}

func TestSetupRegistersAuthenticatedResumeGameRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	gameService := &routerGameStartService{}
	engine := Setup(cfg, nil, WebSocketHandlers{}, nil, nil, handler.NewGameHandler(gameService))

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/resume", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	token, err := middleware.GenerateToken(7, "investigator", cfg.JWT.Secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/resume", nil)
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(authorizedRecorder, authorized)

	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d; body = %s", authorizedRecorder.Code, http.StatusOK, authorizedRecorder.Body.String())
	}
	if gameService.resumeRequest == nil || gameService.resumeRequest.UserID != 7 ||
		gameService.resumeRequest.RoomID != 41 {
		t.Fatalf("resume request = %#v", gameService.resumeRequest)
	}
}

func TestSetupRegistersAuthenticatedLoadGameRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	gameService := &routerGameStartService{}
	engine := Setup(cfg, nil, WebSocketHandlers{}, nil, nil, handler.NewGameHandler(gameService))

	unauthorized := httptest.NewRequest(
		http.MethodPost, "/api/v1/games/41/load", bytes.NewBufferString(`{"save_id":91}`),
	)
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	token, err := middleware.GenerateToken(7, "investigator", cfg.JWT.Secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authorized := httptest.NewRequest(
		http.MethodPost, "/api/v1/games/41/load", bytes.NewBufferString(`{"save_id":91}`),
	)
	authorized.Header.Set("Content-Type", "application/json")
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(authorizedRecorder, authorized)

	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d; body = %s", authorizedRecorder.Code, http.StatusOK, authorizedRecorder.Body.String())
	}
	if gameService.loadRequest == nil || gameService.loadRequest.UserID != 7 ||
		gameService.loadRequest.RoomID != 41 || gameService.loadRequest.SaveID != 91 {
		t.Fatalf("load request = %#v", gameService.loadRequest)
	}
}

func TestSetupRegistersAuthenticatedEndGameRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "router-test-secret", AccessTokenTTL: 15},
		Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
	}
	gameService := &routerGameStartService{}
	engine := Setup(cfg, nil, WebSocketHandlers{}, nil, nil, handler.NewGameHandler(gameService))

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/end", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorizedRecorder.Code, http.StatusUnauthorized)
	}

	token, err := middleware.GenerateToken(7, "investigator", cfg.JWT.Secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	authorized := httptest.NewRequest(http.MethodPost, "/api/v1/games/41/end", nil)
	authorized.Header.Set("Authorization", "Bearer "+token)
	authorizedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(authorizedRecorder, authorized)

	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d; body = %s", authorizedRecorder.Code, http.StatusOK, authorizedRecorder.Body.String())
	}
	if gameService.endRequest == nil || gameService.endRequest.UserID != 7 ||
		gameService.endRequest.RoomID != 41 {
		t.Fatalf("end request = %#v", gameService.endRequest)
	}
}
