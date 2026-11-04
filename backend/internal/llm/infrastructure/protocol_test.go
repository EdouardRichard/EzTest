package llminfra_test

import (
	"reflect"
	"testing"

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

	// P4-T2 replaced the P4-T1 fail-closed Responses placeholder with the real
	// /responses wire adapter; responses_test.go covers that wire behavior. The
	// factory must keep returning two distinct protocol implementations.

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
