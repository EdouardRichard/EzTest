// Package mindmap adapts the mindmap context's frozen execution projection to
// the execution context's MindmapPort (spec §4.2, §5.2). Execution only reads
// the baseline and snapshots and marks case nodes executed; it never writes
// mindmap content. Ids stay int64 in this layer and are only stringified at the
// JSON boundary. Only the mindmap application interface is imported.
package mindmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// errNotConfigured is returned by every adapter call when no mindmap service was
// wired (a partially wired process must fail closed).
var errNotConfigured = errors.New("execution: mindmap adapter is not configured")

// Service is the narrow mindmap application slice the adapter needs.
// *mindmap/application.ExecutionService satisfies it.
type Service interface {
	Baseline(ctx context.Context, versionID int64) (mindmapdomain.ExecutionBaseline, error)
	SnapshotCases(ctx context.Context, versionID int64, caseIDs []int64) ([]mindmapdomain.ExecutionCase, error)
	MarkExecuted(ctx context.Context, versionID int64, caseIDs []int64) error
}

// Adapter implements application.MindmapPort over the mindmap execution
// projection.
type Adapter struct {
	service Service
}

// New returns the adapter. A nil service fails closed on every call.
func New(service Service) *Adapter {
	return &Adapter{service: service}
}

var _ executionapp.MindmapPort = (*Adapter)(nil)

// The production mindmap execution service must keep satisfying the adapter
// contract; a signature drift fails the build here.
var _ Service = (*mindmapapp.ExecutionService)(nil)

// Baseline returns the frozen baseline projection with owned copies.
func (a *Adapter) Baseline(ctx context.Context, versionID int64) (executionapp.BaselineRef, error) {
	if a == nil || a.service == nil {
		return executionapp.BaselineRef{}, errNotConfigured
	}
	baseline, err := a.service.Baseline(ctx, versionID)
	if err != nil {
		return executionapp.BaselineRef{}, mapMindmapError(err)
	}
	return executionapp.BaselineRef{
		Hash:       baseline.GraphHash,
		CodeCommit: copyString(baseline.CodeBaselineCommit),
		CaseIDs:    append([]int64(nil), baseline.CaseIDs...),
	}, nil
}

// SnapshotCases maps every frozen execution case onto the execution context's
// immutable snapshot source view. Steps come ordered by seq and assertions by
// assertion id, so a batch snapshot is reproducible regardless of the mindmap
// read order. Steps and assertions are copies; ids stay int64.
func (a *Adapter) SnapshotCases(ctx context.Context, versionID int64, caseIDs []int64) ([]executionapp.CaseSnapshotSourceView, error) {
	if a == nil || a.service == nil {
		return nil, errNotConfigured
	}
	cases, err := a.service.SnapshotCases(ctx, versionID, caseIDs)
	if err != nil {
		return nil, mapMindmapError(err)
	}
	views := make([]executionapp.CaseSnapshotSourceView, 0, len(cases))
	for _, executionCase := range cases {
		view, err := mapExecutionCase(executionCase)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// MarkExecuted moves reviewed case nodes to executed. It is idempotent at the
// mindmap service (an already executed case is skipped); an empty list is a
// no-op and never calls the service.
func (a *Adapter) MarkExecuted(ctx context.Context, versionID int64, caseNodeIDs []int64) error {
	if a == nil || a.service == nil {
		return errNotConfigured
	}
	if len(caseNodeIDs) == 0 {
		return nil
	}
	return mapMindmapError(a.service.MarkExecuted(ctx, versionID, caseNodeIDs))
}

// mapExecutionCase projects one mindmap ExecutionCase onto the source view.
func mapExecutionCase(executionCase mindmapdomain.ExecutionCase) (executionapp.CaseSnapshotSourceView, error) {
	testData, err := decodeRawObject(executionCase.TestData)
	if err != nil {
		return executionapp.CaseSnapshotSourceView{}, fmt.Errorf(
			"execution: decode case %d test data: %w", executionCase.NodeID, err)
	}

	steps := append([]mindmapdomain.ExecutionStep(nil), executionCase.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Seq < steps[j].Seq })
	snapshotSteps := make([]executiondomain.CaseSnapshotStep, 0, len(steps))
	for _, step := range steps {
		snapshotSteps = append(snapshotSteps, executiondomain.CaseSnapshotStep{
			Seq:      step.Seq,
			Action:   step.Action,
			Expected: step.Expected,
			Data:     decodeStepData(step.Data),
		})
	}

	assertions := append([]mindmapdomain.ExecutionAssertion(nil), executionCase.Assertions...)
	sort.SliceStable(assertions, func(i, j int) bool { return assertions[i].ID < assertions[j].ID })
	snapshotAssertions := make([]executiondomain.CaseSnapshotAssertion, 0, len(assertions))
	for _, assertion := range assertions {
		stepSeq := 0
		if assertion.StepSeq != nil {
			stepSeq = *assertion.StepSeq
		}
		snapshotAssertions = append(snapshotAssertions, executiondomain.CaseSnapshotAssertion{
			ID:         assertion.ID,
			StepSeq:    stepSeq,
			Kind:       assertion.Kind,
			Target:     assertion.Target,
			Expression: assertion.Expression,
			Expected:   assertion.Expected,
		})
	}

	return executionapp.CaseSnapshotSourceView{
		CaseNodeID: executionCase.NodeID,
		Revision:   executionCase.Revision,
		GraphHash:  executionCase.GraphHash,
		Node: executiondomain.CaseSnapshotNode{
			ID:            executionCase.NodeID,
			Title:         executionCase.Title,
			Priority:      executionCase.Priority,
			Preconditions: executionCase.Preconditions,
			TestData:      testData,
		},
		Steps:          snapshotSteps,
		Assertions:     snapshotAssertions,
		RequirementIDs: append([]int64(nil), executionCase.RequirementIDs...),
	}, nil
}

// decodeRawObject decodes a JSON object document. An absent document is nil; a
// malformed one is an error because the snapshot could not be frozen honestly.
func decodeRawObject(raw json.RawMessage) (map[string]any, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	return object, nil
}

// decodeStepData converts the free-form case_step.data document into the
// snapshot's JSON object. The document has no schema, so a non-object or
// malformed value is projected as absent rather than failing the whole batch;
// execution never reads step data to run the case.
func decodeStepData(data string) map[string]any {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return nil
	}
	return object
}

// mapMindmapError translates the mindmap context's errors into execution
// sentinels or ReasonErrors. BatchService relies on the two sentinels; all other
// failures are returned as a stable execution error.
func mapMindmapError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *mindmapdomain.Error
	if !errors.As(err, &domainErr) {
		return err
	}
	switch domainErr.Reason {
	case "baseline_required":
		return executionapp.ErrBaselineRequired
	case "invalid_case_ids":
		return executiondomain.ErrInvalidCaseIDs
	}
	switch domainErr.Kind {
	case mindmapdomain.KindInvalid:
		return executiondomain.NewReasonError("invalid_argument", domainErr.Reason, domainErr.Message, false, nil)
	case mindmapdomain.KindNotFound:
		return executiondomain.NewReasonError("not_found", "not_found", domainErr.Message, false, nil)
	default:
		reason := domainErr.Reason
		if reason == "" {
			reason = domainErr.Kind
		}
		return executiondomain.NewReasonError(reason, reason, domainErr.Message, false, nil)
	}
}

// copyString returns an owned copy of an optional string.
func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
