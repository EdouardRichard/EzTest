package adapters

import (
	"context"
	"encoding/json"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// VisualAssertionLister lists the visual assertion configurations of one case
// version. *visualapp.ConfigService satisfies it. P5 wiring passes it through
// NewTaskProvider; without it the task context still loads (the visual
// assertion IDs stay empty and the server finalizer keeps the case
// inconclusive instead of a false pass).
type VisualAssertionLister interface {
	List(ctx context.Context, versionID int64, caseVersionID, versionSUTID *int64) ([]visual.Assertion, error)
}

// TaskProviderAdapter implements agentapp.TaskContextProvider by calling
// CaseRunner.Prepare, which claims the case_run and validates the current+
// confirmed scope. It is the only bridge between the execution and agent
// contexts (no execution domain types cross into agentapp).
type TaskProviderAdapter struct {
	runner *executionapp.CaseRunner
	visual VisualAssertionLister
}

// NewTaskProvider wires the P3 execution task context provider. The optional
// visual lister attaches the P5 visual_assertion IDs the case executor must
// pass to execution.visual_assert (spec §7.3); it is optional so P3-only wiring
// keeps compiling.
func NewTaskProvider(runner *executionapp.CaseRunner, visual ...VisualAssertionLister) *TaskProviderAdapter {
	provider := &TaskProviderAdapter{runner: runner}
	for _, lister := range visual {
		if lister != nil {
			provider.visual = lister
			break
		}
	}
	return provider
}

var _ agentapp.TaskContextProvider = (*TaskProviderAdapter)(nil)

// ExecutionTask loads and maps the task snapshot for one execute.case work item.
func (p *TaskProviderAdapter) ExecutionTask(ctx context.Context, item agent.WorkItem) (agentapp.ExecutionTask, error) {
	if p.runner == nil {
		return agentapp.ExecutionTask{}, errNotConfigured("case runner")
	}
	snapshot, err := p.runner.Prepare(ctx, item)
	if err != nil {
		return agentapp.ExecutionTask{}, err
	}
	task := mapTaskSnapshot(snapshot)
	p.attachVisualAssertions(ctx, item.VersionID, snapshot.Case.CaseVersionID, &task)
	return task, nil
}

// attachVisualAssertions joins the active visual configurations to the case
// assertion positions already present in the task context. The join is by
// (step_seq, assertion_seq) because that is the only position identity the
// execution snapshot exposes for an assertion; the visual configuration is the
// authoritative source of both the visual_assertion ID and the design
// case_assertion ID. A missing configuration or an unavailable visual service
// leaves the IDs empty; the case-run finalizer then reports
// visual_assertion_not_run instead of passing.
func (p *TaskProviderAdapter) attachVisualAssertions(ctx context.Context, versionID, caseVersionID int64, task *agentapp.ExecutionTask) {
	if p.visual == nil || task == nil || versionID <= 0 || caseVersionID <= 0 {
		return
	}
	hasVisual := false
	for _, assertion := range task.Assertions {
		if assertion.Kind == string(execution.AssertionKindVisual) {
			hasVisual = true
			break
		}
	}
	if !hasVisual {
		return
	}
	configs, err := p.visual.List(ctx, versionID, &caseVersionID, nil)
	if err != nil {
		// Best effort: visual context availability must not block execution of
		// non-visual assertions. The finalizer keeps the case inconclusive.
		return
	}
	byPosition := make(map[[2]int]visual.Assertion, len(configs))
	for _, config := range configs {
		if config.Status != visual.AssertionActive {
			continue
		}
		key := [2]int{config.StepSeq, config.AssertionSeq}
		if _, exists := byPosition[key]; !exists {
			byPosition[key] = config
		}
	}
	for i := range task.Assertions {
		assertion := &task.Assertions[i]
		if assertion.Kind != string(execution.AssertionKindVisual) {
			continue
		}
		config, found := byPosition[[2]int{assertion.StepSeq, assertion.AssertionSeq}]
		if !found {
			continue
		}
		assertion.CaseAssertionID = config.CaseAssertionID
		assertion.VisualAssertionID = config.ID
	}
}

func mapTaskSnapshot(snapshot execution.TaskSnapshot) agentapp.ExecutionTask {
	task := agentapp.ExecutionTask{
		CaseRunID:     snapshot.CaseRun.ID,
		WaveID:        snapshot.CaseRun.WaveID,
		IntentID:      snapshot.CaseRun.IntentID,
		CaseVersionID: snapshot.Case.CaseVersionID,
		CaseTitle:     snapshot.Case.Title,
		Description:   snapshot.Case.Description,
		Preconditions: snapshot.Case.Preconditions,
		Priority:      snapshot.Case.Priority,
		HumanResponse: snapshot.HumanResponse,
		UserMessage: fmt.Sprintf(
			"请按用例《%s》的步骤顺序执行，逐步用 execution.step_record 记录、每条断言用 execution.assertion_record 记录；kind=visual 的断言必须逐条调用 execution.visual_assert（assertionId 取 visual_assertion_id）；需要等待时用 execution.wait；完成后用 execution.case_finish 提交候选终态。",
			snapshot.Case.Title,
		),
		VersionSUTs:     make([]agentapp.ExecutionSUT, 0, len(snapshot.SUTs)),
		Steps:           make([]agentapp.ExecutionStep, 0, len(snapshot.Case.Steps)),
		Assertions:      make([]agentapp.ExecutionAssertion, 0, len(snapshot.Case.Assertions)),
		PriorSteps:      make([]agentapp.ExecutionPriorStep, 0, len(snapshot.PriorSteps)),
		PriorAssertions: make([]agentapp.ExecutionPriorAssertion, 0, len(snapshot.PriorAssertions)),
	}
	for _, sut := range snapshot.SUTs {
		task.VersionSUTs = append(task.VersionSUTs, agentapp.ExecutionSUT{
			VersionSUTID:    sut.VersionSUTID,
			SUTName:         sut.SUTName,
			EnvironmentName: sut.EnvironmentName,
			BaseURL:         sut.BaseURL,
			PageURLs:        append([]string(nil), sut.PageURLs...),
			APIEndpoints:    append([]string(nil), sut.APIEndpoints...),
			AccountRoles:    append([]string(nil), sut.AccountRoles...),
		})
	}
	for _, step := range snapshot.Case.Steps {
		task.Steps = append(task.Steps, agentapp.ExecutionStep{
			Seq:                step.Seq,
			Action:             step.Action,
			Expected:           step.Expected,
			AccountRole:        step.AccountRole,
			TargetVersionSUTID: step.TargetVersionSUTID,
			PageID:             copyInt64(step.TargetPageID),
			APIID:              copyInt64(step.TargetAPIID),
		})
	}
	for _, assertion := range snapshot.Case.Assertions {
		task.Assertions = append(task.Assertions, agentapp.ExecutionAssertion{
			StepSeq:            assertion.StepSeq,
			AssertionSeq:       assertion.AssertionSeq,
			Kind:               assertion.Kind,
			Expected:           assertion.Expected,
			TargetVersionSUTID: assertion.TargetVersionSUTID,
		})
	}
	for _, prior := range snapshot.PriorSteps {
		task.PriorSteps = append(task.PriorSteps, agentapp.ExecutionPriorStep{
			Seq:                prior.Seq,
			Status:             string(prior.Status),
			Observed:           observedSummary(prior.Observed),
			Error:              prior.Error,
			TargetVersionSUTID: derefInt64(prior.TargetVersionSUTID),
		})
	}
	for _, prior := range snapshot.PriorAssertions {
		task.PriorAssertions = append(task.PriorAssertions, agentapp.ExecutionPriorAssertion{
			StepSeq:            prior.StepSeq,
			AssertionSeq:       prior.AssertionSeq,
			Kind:               prior.Kind,
			Verdict:            string(prior.Verdict),
			Expected:           prior.Expected,
			Actual:             prior.Actual,
			Reason:             prior.Reason,
			TargetVersionSUTID: derefInt64(prior.TargetVersionSUTID),
		})
	}
	return task
}

// observedSummary renders step_result.observed as one line for the prompt.
func observedSummary(observed map[string]any) string {
	if len(observed) == 0 {
		return ""
	}
	if summary, ok := observed["summary"].(string); ok && summary != "" {
		return summary
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
