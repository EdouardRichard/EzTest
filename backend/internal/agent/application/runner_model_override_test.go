package agentapp_test

// Task 55 backend: the runner must honor the conversation-level 0.2.1 model
// override. Precedence is conversation override > profile-bound model > config
// default; an override that no longer resolves fails the run without switching
// to another model (D24), and the resolved model is the one recorded on the
// agent_run row (actual_model) and sent to the provider.

import (
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// bindHostProfileModel binds a model to the clarification Host profile, the
// profile a chat.turn run resolves.
func bindHostProfileModel(t *testing.T, fixture *runnerFixture, modelName string) {
	t.Helper()
	fixture.repo.mu.Lock()
	defer fixture.repo.mu.Unlock()
	profile := fixture.repo.profiles[agentapp.ProfileKeyClarificationHost]
	profile.Model = &modelName
	fixture.repo.profiles[profile.Key] = profile
}

func TestRunnerUsesConversationModelOverride(t *testing.T) {
	models := []llmapp.ModelInfo{
		{ConfigID: 3, ModelID: 77, Model: "profile-llm", ContextWindow: 32000, MaxOutput: 4096},
		{ConfigID: 4, ModelID: 88, Model: "override-llm", ContextWindow: 64000, MaxOutput: 8192},
	}

	t.Run("conversation override wins over the profile model", func(t *testing.T) {
		fixture := newRunnerFixture(t)
		fixture.chat.models = models
		fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式？"}}
		bindHostProfileModel(t, fixture, "profile-llm")
		fixture.conv.modelOverride = "override-llm"

		result, err := mustRun(t, fixture, hostItem())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Status != agent.AgentRunDone {
			t.Fatalf("result = %#v", result)
		}
		if len(fixture.chat.resolves) != 1 || fixture.chat.resolves[0] != "override-llm" {
			t.Fatalf("resolves = %#v, want [override-llm] (conversation override wins)", fixture.chat.resolves)
		}
		if got := fixture.chat.request(0).Model; got != "override-llm" {
			t.Fatalf("chat request model = %q, want the conversation override", got)
		}
		run := fixture.repo.lastFinished(t)
		if run.Model != "override-llm" || run.ConfigID != 4 {
			t.Fatalf("agent_run model/config = %q/%d, want the resolved override override-llm/4", run.Model, run.ConfigID)
		}
		if run.FallbackFrom != "" {
			t.Fatalf("agent_run.fallback_from = %q, want empty (no auto model switch)", run.FallbackFrom)
		}
	})

	t.Run("empty override keeps the profile-bound model", func(t *testing.T) {
		fixture := newRunnerFixture(t)
		fixture.chat.models = models
		fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式？"}}
		bindHostProfileModel(t, fixture, "profile-llm")

		result, err := mustRun(t, fixture, hostItem())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Status != agent.AgentRunDone {
			t.Fatalf("result = %#v", result)
		}
		if len(fixture.chat.resolves) != 1 || fixture.chat.resolves[0] != "profile-llm" {
			t.Fatalf("resolves = %#v, want [profile-llm] (profile fallback)", fixture.chat.resolves)
		}
		run := fixture.repo.lastFinished(t)
		if run.Model != "profile-llm" || run.ConfigID != 3 {
			t.Fatalf("agent_run model/config = %q/%d, want profile-llm/3", run.Model, run.ConfigID)
		}
	})

	t.Run("invalid override fails without an automatic model switch", func(t *testing.T) {
		fixture := newRunnerFixture(t)
		fixture.chat.models = models
		fixture.chat.responses = []llmapp.ChatResponse{{Content: "请补充登录方式？"}}
		bindHostProfileModel(t, fixture, "profile-llm")
		fixture.conv.modelOverride = "retired-llm"
		fixture.chat.modelErr = &platform.Error{
			Status:  400,
			Code:    llmapp.CodeLLMConfigInvalid,
			Message: `llm model "retired-llm" is deprecated or disabled; choose another model`,
		}

		_, err := mustRun(t, fixture, hostItem())
		if err == nil {
			t.Fatal("Run succeeded with an invalid conversation override, want a typed failure")
		}
		// Exactly one resolution attempt, carrying the override: the runner must
		// never fall back to the profile model or the config default (D24).
		if len(fixture.chat.resolves) != 1 || fixture.chat.resolves[0] != "retired-llm" {
			t.Fatalf("resolves = %#v, want exactly [retired-llm] (no fallback)", fixture.chat.resolves)
		}
		if fixture.chat.requestCount() != 0 {
			t.Fatalf("llm calls = %d, want 0 (resolution fails before the run)", fixture.chat.requestCount())
		}
		if agentapp.IsRetryable(err) {
			t.Fatalf("IsRetryable = true for an invalid model override, want false: %v", err)
		}
		var runErr *agentapp.RunError
		if !errors.As(err, &runErr) || runErr.Reason != "ai_unavailable" {
			t.Fatalf("error = %#v, want RunError reason ai_unavailable", err)
		}
	})
}
