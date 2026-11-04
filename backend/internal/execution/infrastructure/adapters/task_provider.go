package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// VisualAssertionLister lists the visual assertion configurations of one case
// version. *visualapp.ConfigService satisfies it. P5 wiring passes it through
// NewTaskProvider. Task 115: the lister is mandatory when the case version
// carries kind=visual assertions — a missing or unresolvable configuration is a
// typed pre-execution failure, never a silent inconclusive. It stays optional so
// P3-only wiring keeps compiling for case versions without visual assertions.
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
	if err := p.attachVisualAssertions(ctx, item.VersionID, snapshot.Case.CaseVersionID, &task); err != nil {
		return agentapp.ExecutionTask{}, err
	}
	return task, nil
}

// attachVisualAssertions joins the active visual configurations to the case
// assertion positions already present in the task context. The join prefers the
// design case_assertion.id carried by the execution snapshot (Task 115) and
// falls back to (step_seq, assertion_seq) for legacy task shapes; it validates
// that the configuration targets the same case version, position and design
// assertion. Every kind=visual assertion of the case version must resolve to an
// active visual_assertion row: a missing, archived or mismatched configuration
// is the typed 409 visual_assertion_unresolved pre-execution failure, so the
// case can never reach the model with an empty visual_assertion_id and later
// aggregate to a silent inconclusive. An unavailable visual context is the
// retryable 503 unavailable failure.
func (p *TaskProviderAdapter) attachVisualAssertions(ctx context.Context, versionID, caseVersionID int64, task *agentapp.ExecutionTask) error {
	if task == nil {
		return nil
	}
	visualPositions := make([]string, 0)
	for _, assertion := range task.Assertions {
		if assertion.Kind == string(execution.AssertionKindVisual) {
			visualPositions = append(visualPositions, fmt.Sprintf("step %d assertion %d", assertion.StepSeq, assertion.AssertionSeq))
		}
	}
	if len(visualPositions) == 0 {
		return nil
	}
	if versionID <= 0 || caseVersionID <= 0 {
		return visualAssertionUnresolvedError(caseVersionID, visualPositions,
			"the case scope does not identify a version and case version")
	}
	if p.visual == nil {
		return visualAssertionUnresolvedError(caseVersionID, visualPositions,
			"the visual assertion resolver is not configured")
	}
	configs, err := p.visual.List(ctx, versionID, &caseVersionID, nil)
	if err != nil {
		var apiErr *platform.Error
		if errors.As(err, &apiErr) {
			return apiErr
		}
		return platform.Unavailable(fmt.Sprintf(
			"visual assertion resolution failed for case version %d: %v", caseVersionID, err))
	}
	byDesignAssertion := make(map[int64]visual.Assertion, len(configs))
	byPosition := make(map[[2]int]visual.Assertion, len(configs))
	for _, config := range configs {
		if config.Status != visual.AssertionActive || config.ID <= 0 ||
			config.CaseVersionID != caseVersionID || config.CaseAssertionID <= 0 {
			continue
		}
		if _, exists := byDesignAssertion[config.CaseAssertionID]; !exists {
			byDesignAssertion[config.CaseAssertionID] = config
		}
		key := [2]int{config.StepSeq, config.AssertionSeq}
		if _, exists := byPosition[key]; !exists {
			byPosition[key] = config
		}
	}
	unresolved := make([]string, 0)
	for i := range task.Assertions {
		assertion := &task.Assertions[i]
		if assertion.Kind != string(execution.AssertionKindVisual) {
			continue
		}
		var (
			config visual.Assertion
			found  bool
		)
		if assertion.CaseAssertionID > 0 {
			config, found = byDesignAssertion[assertion.CaseAssertionID]
		} else {
			config, found = byPosition[[2]int{assertion.StepSeq, assertion.AssertionSeq}]
		}
		if !found || config.StepSeq != assertion.StepSeq || config.AssertionSeq != assertion.AssertionSeq {
			unresolved = append(unresolved, fmt.Sprintf("step %d assertion %d", assertion.StepSeq, assertion.AssertionSeq))
			continue
		}
		assertion.CaseAssertionID = config.CaseAssertionID
		assertion.VisualAssertionID = config.ID
	}
	if len(unresolved) > 0 {
		return visualAssertionUnresolvedError(caseVersionID, unresolved,
			"no active visual_assertion configuration targets these design assertions")
	}
	return nil
}

// visualAssertionUnresolvedError is the typed 409 pre-execution failure for a
// kind=visual assertion that has no resolvable visual_assertion_id. It maps to
// the execution case_blocked failure class (a deterministic, non-retryable case
// scope blocker) instead of a silent inconclusive run.
func visualAssertionUnresolvedError(caseVersionID int64, positions []string, reason string) *platform.Error {
	message := fmt.Sprintf(
		"case version %d has visual assertion(s) without a resolvable visual_assertion_id (%s); configure the visual assertion and approve its baseline before executing the case.",
		caseVersionID, strings.Join(positions, ", "),
	)
	if reason != "" {
		message += " " + reason + "."
	}
	return &platform.Error{
		Status:  http.StatusConflict,
		Code:    "visual_assertion_unresolved",
		Message: message,
		Details: map[string]any{
			"case_version_id":       platformid.Format(caseVersionID),
			"unresolved_assertions": positions,
			"missing":               []string{"visual_assertion_id"},
			"next_action":           "configure the visual assertion for each design visual assertion (and approve its baseline), then rerun the case",
		},
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
			CaseAssertionID:    assertion.CaseAssertionID,
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
