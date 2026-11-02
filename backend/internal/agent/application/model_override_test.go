package agentapp_test

import (
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// TestProfileModelOverride pins the P4-T3 profile override path: the runner
// passes agent_profile.model to model resolution and records the actual
// model/config (not the config default) on the agent_run row.
func TestProfileModelOverride(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式与验收标准？"}}
	fixture.chat.models = []llmapp.ModelInfo{{
		ConfigID:      3,
		ModelID:       77,
		Model:         "profile-llm",
		ContextWindow: 32000,
		MaxOutput:     4096,
	}}

	modelName := "profile-llm"
	fixture.repo.mu.Lock()
	profile := fixture.repo.profiles[agentapp.ProfileKeyClarificationHost]
	profile.Model = &modelName
	fixture.repo.profiles[profile.Key] = profile
	fixture.repo.mu.Unlock()

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
	if len(fixture.chat.resolves) != 1 || fixture.chat.resolves[0] != modelName {
		t.Fatalf("resolves = %#v, want [%q]", fixture.chat.resolves, modelName)
	}

	run := fixture.repo.lastFinished(t)
	if run.Model != modelName {
		t.Fatalf("agent_run.model = %q, want the actual model %q", run.Model, modelName)
	}
	if run.ConfigID != 3 {
		t.Fatalf("agent_run.config_id = %d, want 3", run.ConfigID)
	}
	if run.FallbackFrom != "" {
		t.Fatalf("agent_run.fallback_from = %q, want empty (no auto model switch)", run.FallbackFrom)
	}
}
