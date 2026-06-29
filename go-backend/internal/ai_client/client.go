package ai_client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"trpggame/internal/config"
)

// Client Python AI 服务的 HTTP 客户端
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient 创建 AI 客户端
func NewClient(cfg *config.AIConfig) *Client {
	return &Client{
		baseURL: cfg.BaseURL,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
	}
}

// ParseScriptRequest 剧本解析请求
type ParseScriptRequest struct {
	ScriptID uint   `json:"script_id"`
	FilePath string `json:"file_path"`
}

// ParseScriptResponse 剧本解析响应
type ParseScriptResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ParseScript 请求 AI 服务解析剧本
func (c *Client) ParseScript(ctx context.Context, req *ParseScriptRequest) (*ParseScriptResponse, error) {
	url := c.baseURL + "/api/v1/ai/parse-script"
	return post[ParseScriptResponse](c, ctx, url, req)
}

// GameActionRequest 游戏行动推理请求
type GameActionRequest struct {
	RoomID      uint   `json:"room_id"`
	UserID      uint   `json:"user_id"`
	Action      string `json:"action"`
	ScriptID    uint   `json:"script_id"`
	CharacterID uint   `json:"character_id"`
}

// GameActionResponse 游戏行动推理响应
type GameActionResponse struct {
	Narrative string `json:"narrative"`
	DiceRoll  *struct {
		Type    string `json:"type"`
		Result  int    `json:"result"`
		Success bool   `json:"success"`
	} `json:"dice_roll,omitempty"`
	StatusChanges map[string]any `json:"status_changes,omitempty"`
}

// SubmitAction 提交玩家行动到 AI 推理
func (c *Client) SubmitAction(ctx context.Context, req *GameActionRequest) (*GameActionResponse, error) {
	url := c.baseURL + "/api/v1/ai/inference/action"
	return post[GameActionResponse](c, ctx, url, req)
}

// StartGameRequest 开局请求
type StartGameRequest struct {
	RoomID      uint `json:"room_id"`
	ScriptID    uint `json:"script_id"`
	CharacterID uint `json:"character_id"`
	UserID      uint `json:"user_id"`
}

// StartGameResponse 开局叙事响应
type StartGameResponse struct {
	Narrative string `json:"narrative"`
}

// StartGame 请求 AI 生成开场叙事
func (c *Client) StartGame(ctx context.Context, req *StartGameRequest) (*StartGameResponse, error) {
	url := c.baseURL + "/api/v1/ai/inference/start"
	return post[StartGameResponse](c, ctx, url, req)
}

// post 通用 POST 请求封装
func post[T any](c *Client, ctx context.Context, url string, body any) (*T, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ai service error (%d): %s", resp.StatusCode, string(respBody))
	}

	var result T
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}
