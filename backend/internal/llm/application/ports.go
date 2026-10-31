// Package llmapp is the application layer for LLM gateway configuration: config
// CRUD, connectivity testing and the model catalog.
package llmapp

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// RemoteModel is one model reported by an OpenAI-compatible upstream.
type RemoteModel struct {
	ID             string
	ContextWindow  int
	MaxOutput      int
	SupportsTools  bool
	SupportsVision bool
}

// TestResult is the outcome of a connectivity test. Error is only set when the
// client could describe a failure without an error return.
type TestResult struct {
	OK        bool
	LatencyMS int64
	Models    []RemoteModel
	Error     string
}

// Client talks to an OpenAI-compatible upstream. Implementations must never
// include the configured API key (or request headers) in returned errors.
type Client interface {
	ListModels(ctx context.Context, cfg llm.Config) ([]RemoteModel, error)
	Test(ctx context.Context, cfg llm.Config) (TestResult, error)
}

// ConfigInput is a partial config used by both create (nil fields take the P0
// defaults) and update (nil fields keep the stored value; a non-nil empty
// string clears api_key/headers).
type ConfigInput struct {
	Name           *string
	Provider       *string
	Protocol       *string
	BaseURL        *string
	APIKey         *string
	Headers        *map[string]string
	TimeoutSeconds *int
	MaxRetries     *int
	IsDefault      *bool
	Status         *string
}
