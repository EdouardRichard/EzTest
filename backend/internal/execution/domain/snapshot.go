package execution

import "encoding/json"

// CaseStep is the execution-relevant projection of one P2 case_version step.
//
// v0.2.4-B (T9 design contract): a step no longer carries SUT/page/API ids. It
// names its target in prose (TargetDescription) and the executor locates it, so
// the id fields design used to supply are gone rather than renamed. The SUT a
// step actually ran against is recorded on the StepResult, resolved at record
// time from the model's tool argument or the snapshot's DefaultSUTID.
type CaseStep struct {
	Seq               int
	Action            string
	Expected          string
	TargetDescription string
	AccountRole       string
	Data              json.RawMessage
}

// CaseAssertion is the execution-relevant projection of one P2 case_assertion.
// AssertionSeq is derived by ascending case_assertion ID within a step
// (P3-D-06). CaseAssertionID is the design case_assertion.id; it is zero for
// reads that predate the projection.
//
// SUTID is the design case_assertion.sut_id: only a db assertion carries one,
// and design validates its existence but no longer its ownership, so execution
// checks it against the version's bindings. The former TargetDatabaseID is gone
// with the design column.
type CaseAssertion struct {
	CaseAssertionID int64
	StepSeq         int
	AssertionSeq    int
	Kind            string
	Expected        string
	SUTID           int64
}

// CaseSnapshot is the immutable execution input read from the design context:
// a current+confirmed case version with steps and assertions.
//
// SUTDescription and CoveredACs are the v0.2.4-B natural-language contract
// fields (design §3.2/§3.6): prose for the executor and the requirement's
// acceptance-criteria coverage.
//
// DefaultSUTID and SUTIDs are NOT design-supplied any more — design no longer
// knows which SUTs a case touches. They are resolved and written back by the
// execution application when a run is prepared (see executionSUTBindings):
// SUTIDs is every effective SUT of the version's bound environment and
// DefaultSUTID is the single-binding fallback used to stamp a recorded result
// whose tool call omitted the SUT. They stay zero/empty on a fresh read
// straight from the case reader.
type CaseSnapshot struct {
	CaseID               int64
	CaseVersionID        int64
	RequirementID        int64
	RequirementVersionID int64
	Title                string
	Description          string
	SUTDescription       string
	Preconditions        string
	CoveredACs           []string
	Priority             string
	DefaultSUTID         int64
	SUTIDs               []int64
	Steps                []CaseStep
	Assertions           []CaseAssertion
}

// SUTBinding is the execution-relevant projection of one SUT bound to the
// version (resources resolved to URLs/endpoints, no credentials). Intro is the
// SUT catalog's identity prose and EnvironmentID the bound environment — the
// executor reads them to semantically match the case's SUTDescription against
// the catalog (design §6.1/§6.2).
type SUTBinding struct {
	SUTID           int64
	SUTName         string
	Intro           string
	EnvironmentID   int64
	EnvironmentName string
	BaseURL         string
	PageURLs        []string
	APIEndpoints    []string
	AccountRoles    []string
}

// TaskSnapshot is the full task context handed to the case executor runner. It
// carries prior step/assertion results so a resumed run continues from the
// first unfinished step, plus the one-time human response for takeover.
// EnvironmentID is the version's bound environment (stamped by Prepare from
// the resolved bindings; every binding of a version shares one environment).
type TaskSnapshot struct {
	Case            CaseSnapshot
	Intent          ExecutionIntent
	CaseRun         CaseRun
	EnvironmentID   int64
	SUTs            []SUTBinding
	PriorSteps      []StepResult
	PriorAssertions []AssertionResult
	HumanResponse   string
}
