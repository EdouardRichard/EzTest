package domain

import (
	"reflect"
	"strings"
	"testing"
)

// TestSupplementProfileSeed pins the supplement-agent profile seed of spec §9.1:
// the stable key, the non-builtin seed flag, the 20/600/120 budget, disabled
// thinking and the five supplement.* tools of spec §9.2.
func TestSupplementProfileSeed(t *testing.T) {
	profile := SupplementProfile()

	if got, want := profile.Key, "supplement-agent"; got != want {
		t.Fatalf("key=%q want %q", got, want)
	}
	if got, want := profile.Name, "补全 Agent"; got != want {
		t.Fatalf("name=%q want %q", got, want)
	}
	if profile.Builtin {
		t.Fatalf("Builtin=true; the seed must never become the builtin default profile")
	}
	if !profile.Enabled {
		t.Fatalf("Enabled=false; the seeded agent profile must be runnable")
	}
	if got, want := profile.Model, ""; got != want {
		t.Fatalf("model=%q want the empty default LLM", got)
	}
	if got, want := profile.MaxTurns, 20; got != want {
		t.Fatalf("maxTurns=%d want %d", got, want)
	}
	if got, want := profile.RunSeconds, 600; got != want {
		t.Fatalf("runSeconds=%d want %d", got, want)
	}
	if got, want := profile.HardTimeoutSeconds, 120; got != want {
		t.Fatalf("hardTimeoutSeconds=%d want %d", got, want)
	}
	if got, want := profile.Thinking, "disabled"; got != want {
		t.Fatalf("thinking=%q want %q", got, want)
	}
	if got, want := profile.ReasoningEffort, ""; got != want {
		t.Fatalf("reasoningEffort=%q want %q (thinking is disabled)", got, want)
	}
	if got, want := profile.MaxOutputTokens, 8192; got != want {
		t.Fatalf("maxOutputTokens=%d want %d", got, want)
	}
	if got, want := profile.SystemPrompt, SupplementPrompt; got != want {
		t.Fatalf("systemPrompt is not SupplementPrompt")
	}
	if len(profile.SystemPrompt) == 0 {
		t.Fatalf("the supplement prompt is empty")
	}
	// The prompt must teach the spec §9.2 payload extensions the tools accept:
	// optional rationale on new cases and the affected-case codeRefs.
	for _, marker := range []string{"rationale", "codeRefs"} {
		if !strings.Contains(profile.SystemPrompt, marker) {
			t.Fatalf("the supplement prompt does not mention %q", marker)
		}
	}

	wantTools := []string{
		"supplement.context_read",
		"supplement.case_propose",
		"supplement.affected_cases_propose",
		"supplement.escalate",
		"supplement.finish",
	}
	if !reflect.DeepEqual(profile.AllowedTools, wantTools) {
		t.Fatalf("allowedTools=%v want %v", profile.AllowedTools, wantTools)
	}
}

// TestSupplementPromptDoesNotHardcodeTheCap pins that the seeded system prompt
// does not embed the spec-default case cap: the per-run limit comes from
// EZTEST_SUPPLEMENT_MAX_CASES and is carried by the run instruction footer, so a
// hardcoded number would contradict the configured value.
func TestSupplementPromptDoesNotHardcodeTheCap(t *testing.T) {
	if strings.Contains(SupplementPrompt, "最多 5 条") {
		t.Fatal("the supplement prompt hardcodes the 5-case cap; the cap comes from EZTEST_SUPPLEMENT_MAX_CASES")
	}
}

// TestSupplementProfileReturnsFreshSlices guarantees one seed call cannot mutate
// the tools of a later call.
func TestSupplementProfileReturnsFreshSlices(t *testing.T) {
	first := SupplementProfile()
	first.AllowedTools[0] = "mutated"

	second := SupplementProfile()
	if second.AllowedTools[0] != "supplement.context_read" {
		t.Fatalf("AllowedTools leaked between calls: %v", second.AllowedTools)
	}
}
