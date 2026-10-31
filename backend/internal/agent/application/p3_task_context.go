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
type ExecutionTask struct {
	CaseRunID     int64
	WaveID        int64
	IntentID      int64
	CaseVersionID int64

	CaseTitle     string
	Description   string
	Preconditions string
	Priority      string
	HumanResponse string
	UserMessage   string

	VersionSUTs     []ExecutionSUT
	Steps           []ExecutionStep
	Assertions      []ExecutionAssertion
	PriorSteps      []ExecutionPriorStep
	PriorAssertions []ExecutionPriorAssertion
}

// ExecutionSUT is one version-SUT binding shown to the model. Credentials are
// never included; the model must call account.credentials.
type ExecutionSUT struct {
	VersionSUTID    int64
	SUTName         string
	EnvironmentName string
	BaseURL         string
	PageURLs        []string
	APIEndpoints    []string
	AccountRoles    []string
}

// ExecutionStep is one case step.
type ExecutionStep struct {
	Seq                int
	Action             string
	Expected           string
	AccountRole        string
	TargetVersionSUTID int64
	PageID             *int64
	APIID              *int64
}

// ExecutionAssertion is one case assertion (expected is authoritative here; the
// model cannot change it).
type ExecutionAssertion struct {
	StepSeq            int
	AssertionSeq       int
	Kind               string
	Expected           string
	TargetVersionSUTID int64
}

// ExecutionPriorStep is one already-recorded step result for resume.
type ExecutionPriorStep struct {
	Seq                int
	Status             string
	Observed           string
	Error              string
	TargetVersionSUTID int64
}

// ExecutionPriorAssertion is one already-recorded assertion result for resume.
type ExecutionPriorAssertion struct {
	StepSeq            int
	AssertionSeq       int
	Kind               string
	Verdict            string
	Expected           string
	Actual             string
	Reason             string
	TargetVersionSUTID int64
}

// TaskContextProvider loads the P3 execution task snapshot for a work item. The
// execution context implements it (adapters/task_provider.go) by calling
// CaseRunner.Prepare, which validates the case_run/intent/case current+confirmed
// state and claims the dispatch lease.
type TaskContextProvider interface {
	ExecutionTask(ctx context.Context, item agent.WorkItem) (ExecutionTask, error)
}

// p4TaskDetails resolves the domain-owned payload for a P4 work item. It is the
// fallback consulted by Runner.taskContext when the P3 switch has no branch for
// the type: each P4 domain registers a payload-only provider from an init()
// hook with RegisterP4TaskContext. The TaskRef carries the raw work-item payload
// so the provider sees its own IDs (decimal strings) without the runtime parsing
// them; providers get no service deps (see p4_task_context.go).
func p4TaskDetails(ctx context.Context, item agent.WorkItem) (any, bool) {
	payload := map[string]any{}
	if len(item.Payload) > 0 {
		_ = json.Unmarshal(item.Payload, &payload)
	}
	return P4TaskContext(ctx, TaskRef{VersionID: item.VersionID, Type: item.Type, Payload: payload})
}
