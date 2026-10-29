package deepseek

import (
	"context"
	"net/http"

	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// modelListResponse is the OpenAI-compatible /v1/models payload. Only the
// fields the design keeps are decoded; unknown provider fields are ignored.
type modelListResponse struct {
	Data []llm.ModelInfo `json:"data"`
}

// ListModels fetches and parses the upstream model catalog.
func (c *client) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	var decoded modelListResponse
	if err := decodeJSON(resp, &decoded); err != nil {
		return nil, err
	}
	if decoded.Data == nil {
		return []llm.ModelInfo{}, nil
	}
	return decoded.Data, nil
}
