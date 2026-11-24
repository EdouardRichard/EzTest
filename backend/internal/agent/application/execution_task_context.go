package agentapp

import (
	"context"
	"encoding/json"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// ExecutionTask is the P3 case-executor task context rendered into the prompt
// and the context.task_pack tool result. It is built by the execution context
// adapter (CaseRunner.Prepare) and deliberately keeps IDs/plain values so the
// agent runtime never imports execution domain types.
//
// v0.2.4-F T12 (SC-F1): the design contract is natural language. SutDescription
// names the system under test in prose, CoveredACs lists the requirement
// acceptance criteria the case covers, Preconditions is the Given the executor
// must establish via data preparation, and EnvironmentID identifies the bound
// environment whose SUT catalog (SUTs) the executor resolves targets against.
type ExecutionTask struct {
	CaseRunID     int64
	WaveID        int64
	IntentID      int64
	CaseVersionID int64
	EnvironmentID int64

	CaseTitle      string
	Description    string
	SutDescription string
	Preconditions  string
	CoveredACs     []string
	Priority       string
	HumanResponse  string
	UserMessage    string

	SUTs            []ExecutionSUT
	Steps           []ExecutionStep
	Assertions      []ExecutionAssertion
	PriorSteps      []ExecutionPriorStep
	PriorAssertions []ExecutionPriorAssertion
}

// ExecutionSUT is one SUT of the bound environment's catalog shown to the
// model (name/intro/base_url/environment_id). Credentials are never included;
// the model must call account.credentials.
type ExecutionSUT struct {
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

// ExecutionStep is one case step. TargetDescription is the design's prose
// target (v0.2.4-B deleted the design-supplied target SUT/page/API ids); the
// executor locates it against the environment SUT catalog. The SUT a step
// ACTUALLY ran against is recorded on the result (ExecutionPriorStep), not
// prescribed here.
type ExecutionStep struct {
	Seq               int
	Action            string
	Expected          string
	AccountRole       string
	TargetDescription string
}

// ExecutionAssertion is one case assertion (expected is authoritative here; the
// model cannot change it).
type ExecutionAssertion struct {
	StepSeq      int
	AssertionSeq int
	Kind         string
	Expected     string
	TargetSUTID  int64
	// CaseAssertionID is the design case_assertion.id, the stable join key back
	// to the design row. It is populated for every projected assertion.
	CaseAssertionID int64
}

// ExecutionPriorStep is one already-recorded step result for resume.
// TargetSUTID is FACTUAL attribution — the SUT the step actually ran against,
// resolved at record time — not a design-supplied target; resume needs it.
type ExecutionPriorStep struct {
	Seq         int
	Status      string
	Observed    string
	Error       string
	TargetSUTID int64
}

// ExecutionPriorAssertion is one already-recorded assertion result for resume.
// TargetSUTID is FACTUAL attribution (see ExecutionPriorStep).
type ExecutionPriorAssertion struct {
	StepSeq      int
	AssertionSeq int
	Kind         string
	Verdict      string
	Expected     string
	Actual       string
	Reason       string
	TargetSUTID  int64
}

// TaskContextProvider loads the P3 execution task snapshot for a work item. The
// execution context implements it (adapters/task_provider.go) by calling
// CaseRunner.Prepare, which validates the case_run/intent/case current+confirmed
// state and claims the dispatch lease.
type TaskContextProvider interface {
	ExecutionTask(ctx context.Context, item agent.WorkItem) (ExecutionTask, error)
}

// deliveryTaskDetails resolves the domain-owned payload for a P4 work item. It is the
// fallback consulted by Runner.taskContext when the P3 switch has no branch for
// the type: each P4 domain registers a payload-only provider from an init()
// hook with RegisterDeliveryTaskContext. The TaskRef carries the raw work-item payload
// so the provider sees its own IDs (decimal strings) without the runtime parsing
// them; providers get no service deps (see delivery_task_context.go).
func deliveryTaskDetails(ctx context.Context, item agent.WorkItem) (any, bool) {
	payload := map[string]any{}
	if len(item.Payload) > 0 {
		_ = json.Unmarshal(item.Payload, &payload)
	}
	return DeliveryTaskContext(ctx, TaskRef{VersionID: item.VersionID, Type: item.Type, Payload: payload})
}
