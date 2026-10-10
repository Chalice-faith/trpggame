package ai_client

import (
	"context"
	"trpggame/internal/model"
)

func (c *Client) GenerateSummary(ctx context.Context, previous string, messages []model.RuntimeMessage) (string, error) {
	response, err := post[struct {
		Summary string `json:"summary"`
	}](c, ctx, c.baseURL+"/api/v1/ai/memory/summary", struct {
		Previous string                 `json:"previous_summary"`
		Messages []model.RuntimeMessage `json:"messages"`
	}{previous, messages})
	if err != nil {
		return "", err
	}
	return response.Summary, nil
}
