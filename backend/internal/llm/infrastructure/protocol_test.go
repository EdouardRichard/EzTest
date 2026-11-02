package llminfra_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	llminfra "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
)

// protocolConfig returns a valid config for one protocol value. It reuses the
// OpenAI test fixture and overrides only the protocol under test.
func protocolConfig(protocol string) llm.Config {
	cfg := openAITestConfig("http://127.0.0.1:1/v1")
	cfg.Protocol = protocol
	return cfg
}

func TestProtocolFactory(t *testing.T) {
	ctx := context.Background()
	request := llmapp.ChatRequest{Messages: []llmapp.ChatMessage{{Role: "user", Content: "hi"}}}

	chatAdapter, err := llminfra.NewAdapter(protocolConfig(string(llm.ProtocolOpenAIChatCompletions)))
	if err != nil {
		t.Fatalf("NewAdapter(chat completions): %v", err)
	}
	responsesAdapter, err := llminfra.NewAdapter(protocolConfig(string(llm.ProtocolOpenAIResponses)))
	if err != nil {
		t.Fatalf("NewAdapter(responses): %v", err)
	}
	if chatAdapter == nil || responsesAdapter == nil {
		t.Fatal("NewAdapter returned a nil adapter")
	}
	if reflect.TypeOf(chatAdapter) == reflect.TypeOf(responsesAdapter) {
		t.Fatalf("both protocols returned the same adapter type %T", chatAdapter)
	}

	// The Responses wire adapter is P4-T2. P4-T1 must fail closed instead of
	// silently posting a chat-completions request.
	if _, err := responsesAdapter.Chat(ctx, request); !errors.Is(err, llmapp.ErrNotImplemented) {
		t.Fatalf("responses Chat error = %v, want ErrNotImplemented", err)
	}
	if err := responsesAdapter.Stream(ctx, request, func(llmapp.Delta) {}); !errors.Is(err, llmapp.ErrNotImplemented) {
		t.Fatalf("responses Stream error = %v, want ErrNotImplemented", err)
	}

	if _, err := llminfra.NewAdapter(protocolConfig("anthropic")); err == nil {
		t.Fatal("NewAdapter(unknown protocol) returned no error")
	}

	// Old stored values must keep working as chat completions without a data
	// migration.
	for _, legacy := range []string{llm.ProtocolOpenAI, llm.ProtocolOpenAICompatible} {
		adapter, err := llminfra.NewAdapter(protocolConfig(legacy))
		if err != nil {
			t.Fatalf("NewAdapter(%q): %v", legacy, err)
		}
		if reflect.TypeOf(adapter) != reflect.TypeOf(chatAdapter) {
			t.Fatalf("NewAdapter(%q) type = %T, want the chat-completions adapter %T", legacy, adapter, chatAdapter)
		}
	}
}
