package execution

import "encoding/json"

// CaseStep is the execution-relevant projection of one P2 case_version step.
type CaseStep struct {
	Seq                int
	Action             string
	Expected           string
	TargetVersionSUTID int64
	AccountRole        string
	TargetPageID       *int64
	TargetAPIID        *int64
	Data               json.RawMessage
}

// CaseAssertion is the execution-relevant projection of one P2 case_assertion.
// AssertionSeq is derived by ascending case_assertion ID within a step
// (P3-D-06).
type CaseAssertion struct {
	StepSeq            int
	AssertionSeq       int
	Kind               string
	Expected           string
	TargetVersionSUTID int64
	TargetDatabaseID   *int64
}

// CaseSnapshot is the immutable execution input read from the design context:
// a current+confirmed case version with steps and assertions.
type CaseSnapshot struct {
	CaseID               int64
	CaseVersionID        int64
	RequirementID        int64
	RequirementVersionID int64
	DefaultVersionSUTID  int64
	Title                string
	Description          string
	Preconditions        string
	Priority             string
	VersionSUTIDs        []int64
	Steps                []CaseStep
	Assertions           []CaseAssertion
}

// SUTBinding is the execution-relevant projection of one version SUT binding
// (resources resolved to URLs/endpoints, no credentials).
type SUTBinding struct {
	VersionSUTID    int64
	SUTName         string
	EnvironmentName string
	BaseURL         string
	PageURLs        []string
	APIEndpoints    []string
	AccountRoles    []string
}

// TaskSnapshot is the full task context handed to the case executor runner. It
// carries prior step/assertion results so a resumed run continues from the
// first unfinished step, plus the one-time human response for takeover.
type TaskSnapshot struct {
	Case            CaseSnapshot
	Intent          ExecutionIntent
	CaseRun         CaseRun
	SUTs            []SUTBinding
	PriorSteps      []StepResult
	PriorAssertions []AssertionResult
	HumanResponse   string
}
