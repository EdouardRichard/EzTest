package llminfra

import (
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// NewAdapter is the test-only protocol adapter constructor kept after W1-1
// removed the unused production factory. Production constructs adapters through
// Client.Chat with newAdapter.
func NewAdapter(cfg llm.Config) (llmapp.Adapter, error) {
	return newAdapter(cfg, NewClient())
}
