package domain_test

import (
	"regexp"
	"strings"
	"testing"

	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestExecutionProfileSeed pins the execution-agent seed of design spec §7.1:
// stable key, Builtin=false, the exact tool allowlist and the run budgets.
func TestExecutionProfileSeed(t *testing.T) {
	profile := executiondomain.ExecutionProfile()

	if got, want := executiondomain.ExecutionProfileKey, "execution-agent"; got != want {
		t.Fatalf("ExecutionProfileKey=%q want %q", got, want)
	}
	if profile.Key != executiondomain.ExecutionProfileKey {
		t.Fatalf("profile key=%q want %q", profile.Key, executiondomain.ExecutionProfileKey)
	}
	if profile.Builtin {
		t.Fatal("execution-agent must have Builtin=false so the P1 default lookup never selects it")
	}
	if !profile.Enabled {
		t.Fatal("execution-agent must be seeded enabled")
	}
	if profile.Name != "执行 Agent" {
		t.Fatalf("profile name=%q want 执行 Agent", profile.Name)
	}
	if profile.SystemPrompt != executiondomain.ExecutionPrompt {
		t.Fatal("profile system prompt must be the ExecutionPrompt constant")
	}

	wantTools := []string{
		"execution.case_context",
		"browser.goto",
		"browser.snapshot",
		"browser.click",
		"browser.fill",
		"browser.press",
		"browser.wait_for",
		"browser.screenshot",
		"browser.console",
		"browser.network",
		"browser.download",
		"http.request",
		"execution.step_record",
		"execution.assertion_record",
		"execution.case_finish",
		"execution.db_assert",
	}
	if len(profile.AllowedTools) != len(wantTools) {
		t.Fatalf("allowed tools=%v want %d entries", profile.AllowedTools, len(wantTools))
	}
	for index, want := range wantTools {
		if profile.AllowedTools[index] != want {
			t.Fatalf("allowed_tools[%d]=%q want %q", index, profile.AllowedTools[index], want)
		}
	}

	if profile.MaxTurns != 30 {
		t.Fatalf("max_turns=%d want 30", profile.MaxTurns)
	}
	if profile.RunSeconds != 900 {
		t.Fatalf("run_seconds=%d want 900", profile.RunSeconds)
	}
	if profile.HardTimeoutSeconds != 120 {
		t.Fatalf("hard_timeout_seconds=%d want 120", profile.HardTimeoutSeconds)
	}
	if profile.MaxOutputTokens != 8192 {
		t.Fatalf("max_output_tokens=%d want 8192", profile.MaxOutputTokens)
	}
	if profile.Thinking != "disabled" {
		t.Fatalf("thinking=%q want disabled (the spec default is false)", profile.Thinking)
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("the seed profile must pass agent domain validation: %v", err)
	}
}

// TestExecutionPromptHasEightNumberedPoints pins the eight-point prompt of
// design spec §7.2 instead of only its opening sentence.
func TestExecutionPromptHasEightNumberedPoints(t *testing.T) {
	points := regexp.MustCompile(`(?m)^[0-9]+\. `).FindAllString(executiondomain.ExecutionPrompt, -1)
	if len(points) != 8 {
		t.Fatalf("prompt has %d numbered points, want 8:\n%s", len(points), executiondomain.ExecutionPrompt)
	}
	phrases := []string{
		"只执行给定用例快照",
		"execution.step_record",
		"证据",
		"application",
		"禁止伪造",
		"cleanup_required=true",
		"allowed_hosts",
		"凭据",
	}
	for _, phrase := range phrases {
		if !strings.Contains(executiondomain.ExecutionPrompt, phrase) {
			t.Fatalf("prompt is missing %q", phrase)
		}
	}
}

// TestExecutionProfileReturnsFreshToolSlice guarantees a caller cannot mutate
// the seed that later startups persist.
func TestExecutionProfileReturnsFreshToolSlice(t *testing.T) {
	first := executiondomain.ExecutionProfile()
	first.AllowedTools[0] = "mutated"
	second := executiondomain.ExecutionProfile()
	if second.AllowedTools[0] != "execution.case_context" {
		t.Fatalf("allowed tools alias a shared slice: %q", second.AllowedTools[0])
	}
}
