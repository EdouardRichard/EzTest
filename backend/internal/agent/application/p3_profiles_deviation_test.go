package agentapp_test

// QA Q1: the failure analyst and report writer prompts must keep an observed
// requirement deviation on the product-defect path instead of dismissing it as
// an automation/script problem.

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

func TestFailureAnalystPromptTriagesObservedRequirementDeviation(t *testing.T) {
	profiles := agentapp.P3Profiles()
	failure, ok := profileByKey(profiles, agentapp.ProfileKeyFailureAnalyst)
	if !ok {
		t.Fatal("failure_analyst profile is missing")
	}
	for _, phrase := range []string{"observed", "需求", "application", "automation", "脚本"} {
		if !strings.Contains(failure.SystemPrompt, phrase) {
			t.Fatalf("failure_analyst prompt must mention %q: %s", phrase, failure.SystemPrompt)
		}
	}
	if strings.Contains(failure.SystemPrompt, "不得把非 application 升级为 application") {
		t.Fatalf("failure_analyst prompt still forbids triaging an observed deviation as application")
	}
}

func TestReportWriterPromptDoesNotDismissApplicationFailures(t *testing.T) {
	profiles := agentapp.P4Profiles()
	writer, ok := profileByKey(profiles, agentapp.ProfileKeyReportWriter)
	if !ok {
		t.Fatal("report_writer profile is missing")
	}
	for _, phrase := range []string{"application", "产品", "缺陷", "脚本"} {
		if !strings.Contains(writer.SystemPrompt, phrase) {
			t.Fatalf("report_writer prompt must mention %q: %s", phrase, writer.SystemPrompt)
		}
	}
}
