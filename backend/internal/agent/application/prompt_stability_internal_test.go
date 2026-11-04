package agentapp

import (
	"regexp"
	"strings"
	"testing"
)

// TestUnsafeSavedPromptFallsBackToStableSeed pins the no-silent-instability
// policy for prompts saved before Task 81: a body that references a per-run
// variable cannot render into the system prefix, so renderedSystemPrompt falls
// back to the static seeded body. The per-run values are not lost: the task
// block that carries them is appended to the user suffix by the cache-first
// path (Task 80).
func TestUnsafeSavedPromptFallsBackToStableSeed(t *testing.T) {
	in := internalPromptInput()
	in.Profile.SystemPrompt = "[profile:clarification_host] 现在 {{.Now}}；目标 {{.TaskGoal}}"
	in.Task.Goal = "回答用户问题"

	stable := stableSystemContent(in)
	for _, leaked := range []string{"{{.Now}}", "{{.TaskGoal}}", "回答用户问题"} {
		if strings.Contains(stable, leaked) {
			t.Fatalf("per-run %q leaked into the stable prefix:\n%s", leaked, stable)
		}
	}
	if regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`).MatchString(stable) {
		t.Fatalf("per-run timestamp leaked into the stable prefix:\n%s", stable)
	}

	defaultPrompt, ok := DefaultPrompt(ProfileKeyClarificationHost)
	if !ok {
		t.Fatal("default prompt missing")
	}
	firstLine := strings.SplitN(defaultPrompt, "\n", 2)[0]
	if !strings.Contains(stable, firstLine) {
		t.Fatalf("stable prefix did not fall back to the static seed:\n%s", stable)
	}

	// Task semantics are not lost: the per-work-item block still carries them.
	if block := taskContextBlock(in); !strings.Contains(block, "回答用户问题") {
		t.Fatalf("task semantics lost from the task block:\n%s", block)
	}
	// The legacy system layout keeps the static fallback plus the task block.
	if legacy := systemContent(in); !strings.Contains(legacy, firstLine) || !strings.Contains(legacy, "回答用户问题") {
		t.Fatalf("legacy system content lost the fallback or task semantics:\n%s", legacy)
	}
}
