package router

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
		nil,
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
	engine := Setup(cfg, nil, nil, nil, nil, handler.NewGameHandler(gameService))
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
