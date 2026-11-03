package agentapp_test

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// TestClarificationPromptsTeachNumericResourceTargets pins the O5 model-facing
// guidance: the Curator must copy the decimal IDs exposed by sut.list /
// sut.page_list into requirement targets, and the Host must not read a
// point-in-time requirement.read snapshot as "no drafts were created" while the
// parallel Curator is writing.
func TestClarificationPromptsTeachNumericResourceTargets(t *testing.T) {
	var host, curator string
	for _, profile := range agentapp.P1Profiles() {
		switch profile.Key {
		case agentapp.ProfileKeyClarificationHost:
			host = profile.SystemPrompt
		case agentapp.ProfileKeyRequirementCurator:
			curator = profile.SystemPrompt
		}
	}
	if curator == "" || host == "" {
		t.Fatalf("clarification profiles missing: host_present=%v curator_present=%v", host != "", curator != "")
	}
	for _, want := range []string{"sut.page_list", "sut.list", "十进制"} {
		if !strings.Contains(curator, want) {
			t.Fatalf("curator prompt lacks %q:\n%s", want, curator)
		}
	}
	for _, want := range []string{"requirement_card", "快照"} {
		if !strings.Contains(host, want) {
			t.Fatalf("host prompt lacks %q:\n%s", want, host)
		}
	}
}
