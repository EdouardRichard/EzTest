package agentapp_test

// P4-T4: agent_run records the actual model/config/fallback_from plus the token
// and cache accounting (spec §7.3/§7.5). This test drives a full runner run and
// inspects the finished run row the repository would persist, then checks the
// Agent center run view exposes the same observability fields.

import (
	"context"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestRunRecordsActualModel(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.models = []llmapp.ModelInfo{{
		ConfigID:      3,
		ModelID:       77,
		Model:         "actual-llm",
		ContextWindow: 64000,
		MaxOutput:     8192,
	}}
	fixture.chat.responses = []llmapp.ChatResponse{{
		Content:      "请补充登录方式？",
		FinishReason: "stop",
		Usage: map[string]int{
			"prompt_tokens":     120,
			"completion_tokens": 30,
			"total_tokens":      150,
		},
	}}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result status = %q", result.Status)
	}

	run := fixture.repo.lastFinished(t)
	if run.Model != "actual-llm" {
		t.Fatalf("agent_run.model = %q, want the actual model", run.Model)
	}
	if run.ConfigID != 3 {
		t.Fatalf("agent_run.config_id = %d, want 3", run.ConfigID)
	}
	if run.FallbackFrom != "" {
		t.Fatalf("agent_run.fallback_from = %q, want empty (no auto failover, D24)", run.FallbackFrom)
	}
	if run.Tokens["prompt_tokens"] != 120 || run.Tokens["completion_tokens"] != 30 ||
		run.Tokens["total_tokens"] != 150 {
		t.Fatalf("agent_run.tokens = %#v, want the aggregated token usage", run.Tokens)
	}
	if run.Cache == nil {
		t.Fatal("agent_run.cache is nil, want a persisted (possibly empty) object")
	}
	if run.Usage["total_tokens"] != 150 {
		t.Fatalf("agent_run.usage = %#v, want the frozen usage projection", run.Usage)
	}

	// The Agent center run view must expose the persisted observability fields.
	reader := &fakeAgentCenterReader{runs: []agentapp.RunRow{{
		ID:         result.AgentRunID,
		ProfileKey: agentapp.ProfileKeyClarificationHost,
		Status:     string(result.Status),
		Model:      run.Model,
		ConfigID:   run.ConfigID,
		Tokens:     run.Tokens,
		Cache:      run.Cache,
	}}}
	service := agentapp.NewAgentCenterService(reader, nil, nil)
	views, _, err := service.Runs(context.Background(), agentapp.RunFilter{})
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %d", len(views))
	}
	if views[0].Model != "actual-llm" || views[0].ConfigID != 3 {
		t.Fatalf("RunView = %#v, want the recorded model/config", views[0])
	}
	if views[0].Tokens["total_tokens"] != 150 || views[0].Cache == nil {
		t.Fatalf("RunView tokens/cache = %#v/%#v", views[0].Tokens, views[0].Cache)
	}
}

// TestProtocolModelErrorFailsRunWithoutRetry pins the D24 run behavior: a 400
// protocol/capability rejection is a single upstream call, the work item is not
// retried, the run keeps the originally resolved model (never a fallback), and
// the user-visible action asks to check the capability/protocol.
func TestProtocolModelErrorFailsRunWithoutRetry(t *testing.T) {
	fixture := newRunnerFixture(t)
	protocolErr := &platform.Error{
		Status:  400,
		Code:    llmapp.CodeLLMProtocolError,
		Message: "llm provider protocol error (HTTP 400): invalid parameter；建议：检查模型能力/协议",
		Details: map[string]any{"kind": string(llmapp.LLMKindProtocol), "action": llmapp.LLMActionCheckModel},
	}
	fixture.chat.errors = []error{protocolErr}

	_, err := mustRun(t, fixture, hostItem())
	if err == nil {
		t.Fatal("Run succeeded on a non-retryable protocol rejection")
	}
	if fixture.chat.requestCount() != 1 {
		t.Fatalf("llm calls = %d, want exactly 1 (no retry, no model failover)", fixture.chat.requestCount())
	}
	if agentapp.IsRetryable(err) {
		t.Fatal("IsRetryable = true, want false for a protocol/capability rejection")
	}
	run := fixture.repo.lastFinished(t)
	if run.Status != agent.AgentRunFailed || run.TerminalReason == nil || *run.TerminalReason != "model_error" {
		t.Fatalf("run = %#v, want failed/model_error", run)
	}
	if run.FallbackFrom != "" {
		t.Fatalf("agent_run.fallback_from = %q, want empty (never switch model automatically)", run.FallbackFrom)
	}
	failure := agentapp.Classify(err)
	if failure.Kind != agentapp.FailureKindModelError || failure.Retryable || !failure.UserVisible {
		t.Fatalf("failure = %#v, want non-retryable user-visible model_error", failure)
	}
	if failure.Action != agentapp.FailureActionCheckModel {
		t.Fatalf("action = %q, want %q", failure.Action, agentapp.FailureActionCheckModel)
	}
}
