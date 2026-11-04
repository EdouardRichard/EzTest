package agentapp_test

// Task 108 (PO E2E Report B): the report writer must quote the deterministic
// report.data_read projection instead of inventing coverage facts or dismissing
// requirements whose cases passed as "no valid execution".

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

func TestReportWriterPromptBindsNarrativeToDeterministicResults(t *testing.T) {
	profiles := agentapp.P4Profiles()
	writer, ok := profileByKey(profiles, agentapp.ProfileKeyReportWriter)
	if !ok {
		t.Fatal("report_writer profile is missing")
	}
	for _, phrase := range []string{
		"report.data_read",
		"逐字引用",
		"case_results",
		"by_requirement",
		"gaps_by_reason",
		"无有效执行",
		"确定性摘要",
	} {
		if !strings.Contains(writer.SystemPrompt, phrase) {
			t.Fatalf("report_writer prompt must mention %q: %s", phrase, writer.SystemPrompt)
		}
	}
}
