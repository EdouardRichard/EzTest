package agentapp_test

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// TestClarificationPromptsTeachNaturalLanguageRequirementContract pins the
// model-facing guidance for the v0.2.4-B requirement contract (design v1.3.1
// §5.1). It replaces the O5 "teach numeric resource targets" guard: a
// requirement draft no longer takes resource IDs at all, so the Host must be
// told to describe the system under test in prose and to leave AC ids to the
// server -- and must NOT be told to copy catalog IDs into targets, because the
// tool now rejects those fields outright. The point-in-time requirement.read
// snapshot guidance is unchanged and stays pinned here.
func TestClarificationPromptsTeachNaturalLanguageRequirementContract(t *testing.T) {
	var host string
	for _, profile := range agentapp.BaseProfiles() {
		if profile.Key == agentapp.ProfileKeyClarificationHost {
			host = profile.SystemPrompt
		}
	}
	if host == "" {
		t.Fatal("clarification Host profile is missing")
	}
	for _, want := range []string{"sut_description", "user_story", "acceptance_criteria", "given/when/then", "sut.page_list", "sut.list"} {
		if !strings.Contains(host, want) {
			t.Fatalf("host prompt lacks %q:\n%s", want, host)
		}
	}
	// The removed fields must not be presented as something to fill in.
	for _, gone := range []string{"targets", "target_pages", "target_apis", "target_version_suts", "十进制"} {
		if strings.Contains(host, gone) {
			t.Fatalf("host prompt still teaches the removed %q:\n%s", gone, host)
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
	for _, profile := range agentapp.BaseProfiles() {
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
