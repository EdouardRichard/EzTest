package agentapp_test

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// TestClarificationPromptsTeachNumericResourceTargets pins the O5 model-facing
// guidance after the Curator removal: the clarification Host owns requirement
// drafting and must copy the decimal IDs exposed by sut.list / sut.page_list
// into requirement targets, and must not read a point-in-time requirement.read
// snapshot as "no drafts were created".
func TestClarificationPromptsTeachNumericResourceTargets(t *testing.T) {
	var host string
	for _, profile := range agentapp.P1Profiles() {
		if profile.Key == agentapp.ProfileKeyClarificationHost {
			host = profile.SystemPrompt
		}
	}
	if host == "" {
		t.Fatal("clarification Host profile is missing")
	}
	for _, want := range []string{"sut.page_list", "sut.list", "十进制"} {
		if !strings.Contains(host, want) {
			t.Fatalf("host prompt lacks %q:\n%s", want, host)
		}
	}
	for _, want := range []string{"requirement_card", "快照"} {
		if !strings.Contains(host, want) {
			t.Fatalf("host prompt lacks %q:\n%s", want, host)
		}
	}
}

// TestClarificationPromptsTeachSourceKeyIdempotency pins the duplicate-draft
// guidance added with the server-side source key: the Host must update the
// existing FR draft instead of creating another one now that it owns drafting.
func TestClarificationPromptsTeachSourceKeyIdempotency(t *testing.T) {
	var host string
	for _, profile := range agentapp.P1Profiles() {
		if profile.Key == agentapp.ProfileKeyClarificationHost {
			host = profile.SystemPrompt
		}
	}
	if host == "" {
		t.Fatal("clarification Host profile is missing")
	}
	for _, want := range []string{"同一来源编号", "requirement.draft_update", "不要"} {
		if !strings.Contains(host, want) {
			t.Fatalf("host prompt lacks %q:\n%s", want, host)
		}
	}
}
