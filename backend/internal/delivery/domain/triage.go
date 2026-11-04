package delivery

import (
	"encoding/json"
	"strings"
)

// TriageOutputClassification returns the classification proposal stored in one
// issue.triage analysis_run.output, or "" when the output is absent, malformed
// or carries no string classification. It never validates the proposal against
// the issue category: the triage contract stores the proposal in the analysis
// output only (spec §5.3).
func TriageOutputClassification(run AnalysisRun) string {
	if len(run.Output) == 0 {
		return ""
	}
	var output struct {
		Classification string `json:"classification"`
	}
	if err := json.Unmarshal(run.Output, &output); err != nil {
		return ""
	}
	return strings.TrimSpace(output.Classification)
}
