// Package codeintelllm adapts the llm context's default chat-completions
// configuration to the codeintel application LLMCompleter port (design spec
// §6.2/§3.5). The gateway snapshots the default configuration when it is built,
// so the model name reported by Model() is the model the code-intelligence
// cache key and the analysis record use. It never logs prompts, code content or
// secrets: failures are wrapped only with stable error descriptions.
package codeintelllm

import (
	"context"
	"fmt"
	"strings"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// ConfigSource is the narrow slice of llm/application.ConfigService the gateway
// needs: the default gateway configuration with its secrets decrypted for
// internal use.
type ConfigSource interface {
	GetDefault(ctx context.Context) (llm.Config, error)
}

// LLMGatewayConfig wires the codeintel LLM gateway to the llm context. The
// composition root passes llm/application's ConfigService and the same
// client factory it uses for the other LLM clients; that factory is
// responsible for applying the configuration's protocol, retry policy and
// timeout, so codeintel inherits them unchanged.
type LLMGatewayConfig struct {
	// ConfigService resolves the deployment's default LLM configuration.
	ConfigService ConfigSource
	// ClientFactory builds the protocol client for a resolved configuration.
	ClientFactory llmapp.ClientFactory
}

// Gateway is the codeintel LLMCompleter implementation. It is immutable after
// construction and therefore safe for concurrent use.
type Gateway struct {
	model  string
	client llmapp.Client
}

// Compile-time proof that the gateway satisfies the application port.
var _ codeintelapp.LLMCompleter = (*Gateway)(nil)

// NewLLMGateway builds the codeintel LLM gateway from the default llm
// configuration. It returns an error wrapping
// codeintelapp.ErrLLMNotConfigured when no usable default configuration exists
// (missing configuration, unusable protocol or a nil dependency), so the
// caller can degrade the analysis to T0-only results instead of retrying until
// the run times out (design spec §6.2). The underlying llm.ErrConfigNotFound is
// preserved for errors.Is when the configuration service reports it.
func NewLLMGateway(cfg LLMGatewayConfig) (*Gateway, error) {
	if cfg.ConfigService == nil || cfg.ClientFactory == nil {
		return nil, codeintelapp.ErrLLMNotConfigured
	}

	defaultConfig, err := cfg.ConfigService.GetDefault(context.Background())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", codeintelapp.ErrLLMNotConfigured, err)
	}
	model := strings.TrimSpace(defaultConfig.Model)
	if model == "" {
		return nil, fmt.Errorf("%w: the default configuration has no model", codeintelapp.ErrLLMNotConfigured)
	}
	if protocol := defaultConfig.Protocol; protocol != "" && protocol != llm.ProtocolChat {
		return nil, fmt.Errorf("%w: protocol %q cannot serve chat completions",
			codeintelapp.ErrLLMNotConfigured, protocol)
	}

	client := cfg.ClientFactory(defaultConfig)
	if client == nil {
		return nil, fmt.Errorf("%w: the client factory returned no client", codeintelapp.ErrLLMNotConfigured)
	}
	return &Gateway{model: model, client: client}, nil
}

// Complete sends one system + user chat-completions request and returns the
// assistant text. The prompt and the response are never logged or persisted by
// the gateway; upstream errors are wrapped so their llm_* code survives, and
// the upstream cause never contains the prompt (the llm adapter keeps response
// bodies out of its typed errors).
func (g *Gateway) Complete(ctx context.Context, system, user string) (string, error) {
	response, err := g.client.Chat(ctx, llmapp.ChatRequest{
		Model: g.model,
		Messages: []llmapp.Message{
			{Role: "system", Content: []llmapp.Part{{Type: "text", Text: system}}},
			{Role: "user", Content: []llmapp.Part{{Type: "text", Text: user}}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("codeintel: llm chat completion failed: %w", err)
	}
	return response.Text, nil
}

// Model returns the configured model name snapshot. It is part of the
// LLMCompleter contract and feeds the analysis model column and the cache key.
func (g *Gateway) Model() string {
	return g.model
}
