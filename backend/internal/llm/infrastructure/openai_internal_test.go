package llminfra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// TestNewClientHasNoGlobalTimeout pins the root-cause fix: a global
// http.Client.Timeout was silently capping every request at 10s, so the
// per-config timeout_seconds could not allow long tool-calling completions.
func TestNewClientHasNoGlobalTimeout(t *testing.T) {
	if got := NewClient().http.Timeout; got != 0 {
		t.Fatalf("NewClient http timeout = %s, want 0 (per-request deadline)", got)
	}
}

// TestDoAppliesConfiguredTimeout proves the replacement bound: calls that use
// Client.do (model discovery/probe) are still bounded by the config timeout.
func TestDoAppliesConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	cfg := llm.Config{
		BaseURL:        server.URL,
		APIKey:         "sk-test",
		TimeoutSeconds: 1,
		Status:         llm.StatusActive,
		Provider:       llm.ProviderOpenAI,
		Protocol:       llm.ProtocolOpenAI,
	}
	client := NewClient()
	started := time.Now()
	_, err := client.ListModels(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("ListModels error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("ListModels took %s, want about 1s", elapsed)
	}
}
