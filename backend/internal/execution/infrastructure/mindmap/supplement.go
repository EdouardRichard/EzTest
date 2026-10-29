// Package mindmap adapts the mindmap context's application ports to the P7
// execution features: the supplement/incremental-design run starter, the
// supplement-batch precondition reader and the recent-batch-result projection
// injected into the supplement agent. The package lives in the execution
// infrastructure because every read it serves is owned by the execution tables;
// the mindmap application only sees its own ports.
package mindmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executiondelivery "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/delivery"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapagenttool "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application/agent_tool"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// SupplementBatches implements mindmap/application.SupplementBatchReader over
// the execution batch reads (spec §5.3/§7.6). A missing batch is the mindmap
// sentinel ErrSupplementBatchNotFound.
type SupplementBatches struct {
	reads *executiondelivery.ReadStore
}

// NewSupplementBatches wires the parent-batch reader.
func NewSupplementBatches(reads *executiondelivery.ReadStore) *SupplementBatches {
	return &SupplementBatches{reads: reads}
}

var _ mindmapapp.SupplementBatchReader = (*SupplementBatches)(nil)

// GetBatch implements mindmap/application.SupplementBatchReader.
func (s *SupplementBatches) GetBatch(ctx context.Context, batchID int64) (mindmapapp.SupplementBatch, error) {
	if s == nil || s.reads == nil {
		return mindmapapp.SupplementBatch{}, errors.New("execution mindmap: batch reader is not configured")
	}
	batch, err := s.reads.GetBatchByID(ctx, batchID)
	if errors.Is(err, executiondelivery.ErrBatchNotFound) {
		return mindmapapp.SupplementBatch{}, fmt.Errorf("%w: batch %d",
			mindmapapp.ErrSupplementBatchNotFound, batchID)
	}
	if err != nil {
		return mindmapapp.SupplementBatch{}, err
	}
	return mindmapapp.SupplementBatch{
		ID:        batch.ID,
		VersionID: batch.VersionID,
		Kind:      batch.Kind,
		Status:    batch.Status,
	}, nil
}

// LatestTerminalBatch implements mindmap/application.SupplementBatchReader.
func (s *SupplementBatches) LatestTerminalBatch(ctx context.Context, versionID int64) (mindmapapp.SupplementBatch, error) {
	if s == nil || s.reads == nil {
		return mindmapapp.SupplementBatch{}, errors.New("execution mindmap: batch reader is not configured")
	}
	batch, err := s.reads.LatestTerminalBatch(ctx, versionID)
	if errors.Is(err, executiondelivery.ErrBatchNotFound) {
		return mindmapapp.SupplementBatch{}, fmt.Errorf("%w: version %d",
			mindmapapp.ErrSupplementBatchNotFound, versionID)
	}
	if err != nil {
		return mindmapapp.SupplementBatch{}, err
	}
	return mindmapapp.SupplementBatch{
		ID:        batch.ID,
		VersionID: batch.VersionID,
		Kind:      batch.Kind,
		Status:    batch.Status,
	}, nil
}

// BatchResults implements the supplement tools' recent-result reader
// (mindmap/application/agent_tool.BatchResultReader) over the execution batch.
type BatchResults struct {
	reads *executiondelivery.ReadStore
}

// NewBatchResults wires the recent-batch-result reader.
func NewBatchResults(reads *executiondelivery.ReadStore) *BatchResults {
	return &BatchResults{reads: reads}
}

var _ mindmapagenttool.BatchResultReader = (*BatchResults)(nil)

// LatestBatchResults implements the supplement tool port.
func (b *BatchResults) LatestBatchResults(ctx context.Context, versionID, batchID int64) (mindmapagenttool.BatchResultSummary, error) {
	if b == nil || b.reads == nil {
		return mindmapagenttool.BatchResultSummary{}, errors.New("execution mindmap: batch result reader is not configured")
	}
	row, err := b.reads.LatestBatchResult(ctx, versionID, batchID)
	if err != nil {
		return mindmapagenttool.BatchResultSummary{}, err
	}
	summary := mindmapagenttool.BatchResultSummary{
		BatchID:      row.BatchID,
		Status:       row.Status,
		Passed:       row.Passed,
		Failed:       row.Failed,
		Blocked:      row.Blocked,
		Inconclusive: row.Inconclusive,
		Skipped:      row.Skipped,
		Cases:        make([]mindmapagenttool.BatchResultCase, 0, len(row.Cases)),
	}
	for _, resultCase := range row.Cases {
		summary.Cases = append(summary.Cases, mindmapagenttool.BatchResultCase{
			CaseNodeID:   resultCase.CaseNodeID,
			Title:        resultCase.Title,
			Status:       resultCase.Status,
			FailureClass: resultCase.FailureClass,
		})
	}
	return summary, nil
}

// RunStarter is the mindmap run-coordinator slice the supplement port needs.
// *mindmap/application.RunService satisfies it.
type RunStarter interface {
	Start(ctx context.Context, versionID, actor int64, cmd mindmapapp.StartRunCommand) (mindmapdomain.CaseDesignRun, error)
}

// BatchCreator is the execution batch use-case slice the supplement port needs.
// *execution/application.BatchService satisfies it.
type BatchCreator interface {
	Create(ctx context.Context, versionID, actor int64, cmd executionapp.CreateBatchCommand) (*executiondomain.RunBatch, error)
}

// SupplementMerger is the mindmap write slice the supplement port needs.
// *execution/infrastructure/mindmap.Adapter satisfies it.
type SupplementMerger interface {
	MarkMerged(ctx context.Context, versionID int64, caseIDs []int64) error
}

// RunByBatchReader is the by-batch P7 run lookup. *mindmap/infrastructure/
// postgres.RunByBatchReader satisfies it.
type RunByBatchReader interface {
	LatestByBatch(ctx context.Context, batchID int64) (mindmapdomain.CaseDesignRun, bool, error)
}

// SupplementPort implements execution/application.SupplementPort (spec §6.2):
// it starts the mindmap supplement/incremental-design runs, creates the child
// supplement batch and merges the adopted nodes.
type SupplementPort struct {
	Runs       RunStarter
	Batches    BatchCreator
	Mindmap    SupplementMerger
	RunByBatch RunByBatchReader
}

// NewSupplementPort wires the execution-side supplement adapter.
func NewSupplementPort(runs RunStarter, batches BatchCreator, merger SupplementMerger, runByBatch RunByBatchReader) *SupplementPort {
	return &SupplementPort{Runs: runs, Batches: batches, Mindmap: merger, RunByBatch: runByBatch}
}

var _ executionapp.SupplementPort = (*SupplementPort)(nil)

// StartSupplement implements executionapp.SupplementPort.
func (p *SupplementPort) StartSupplement(ctx context.Context, versionID, batchID, actor int64) (int64, error) {
	return p.startRun(ctx, versionID, batchID, actor, mindmapapp.RunKindSupplement)
}

// StartIncrementalDesign implements executionapp.SupplementPort.
func (p *SupplementPort) StartIncrementalDesign(ctx context.Context, versionID, batchID, actor int64) (int64, error) {
	return p.startRun(ctx, versionID, batchID, actor, mindmapapp.RunKindIncrementalDesign)
}

// startRun starts one P7 mindmap run; a run_in_progress conflict is translated
// to the worker sentinel so the chain waits for the stored run. A concurrent
// duplicate supplement/incremental_design run can also lose the
// uk_case_design_run_supplement_batch unique index instead of the per-kind
// pre-check; that race is mapped to the same wait sentinel so the worker never
// treats it as an error.
func (p *SupplementPort) startRun(ctx context.Context, versionID, batchID, actor int64, kind string) (int64, error) {
	if p == nil || p.Runs == nil {
		return 0, errors.New("execution mindmap: run service is not configured")
	}
	run, err := p.Runs.Start(ctx, versionID, actor, mindmapapp.StartRunCommand{Kind: kind, BatchID: batchID})
	if err != nil {
		if mindmapReason(err) == "run_in_progress" ||
			strings.Contains(err.Error(), "uk_case_design_run_supplement_batch") {
			return 0, fmt.Errorf("%w: version %d kind %s", executionapp.ErrAutonomyRunInProgress, versionID, kind)
		}
		return 0, err
	}
	return run.ID, nil
}

// GetSupplementRun implements executionapp.SupplementPort: the newest supplement
// or incremental_design run of the parent batch, or nil when none exists.
func (p *SupplementPort) GetSupplementRun(ctx context.Context, batchID int64) (*executionapp.SupplementRunView, error) {
	if p == nil || p.RunByBatch == nil {
		return nil, errors.New("execution mindmap: supplement run reader is not configured")
	}
	run, found, err := p.RunByBatch.LatestByBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	result := decodeSupplementResult(run.Result)
	return &executionapp.SupplementRunView{
		ID:          run.ID,
		Kind:        run.Kind,
		Status:      run.Status,
		NewCaseIDs:  result.NewCaseIDs,
		Degraded:    result.Degraded || run.Status == mindmapdomain.RunDegraded || run.Status == mindmapdomain.RunFailed,
		Escalations: result.Escalations,
	}, nil
}

// CreateSupplementBatch implements executionapp.SupplementPort.
func (p *SupplementPort) CreateSupplementBatch(ctx context.Context, versionID, parentBatchID int64, caseIDs []int64, actor int64) (*executiondomain.RunBatch, error) {
	if p == nil || p.Batches == nil {
		return nil, errors.New("execution mindmap: batch service is not configured")
	}
	parent := parentBatchID
	batch, err := p.Batches.Create(ctx, versionID, actor, executionapp.CreateBatchCommand{
		CaseIDs:       caseIDs,
		Kind:          string(executiondomain.BatchKindSupplement),
		ParentBatchID: &parent,
	})
	if err != nil {
		if permanentSupplementCreateError(err) {
			return nil, fmt.Errorf("%w: %v", executionapp.ErrAutonomySupplementRejected, err)
		}
		return nil, err
	}
	return batch, nil
}

// permanentSupplementCreateError reports a supplement-batch creation error that
// can never succeed on retry because the case set itself is invalid. The worker
// maps it to ErrAutonomySupplementRejected and settles the chain degraded
// instead of retrying the same case set on every tick.
func permanentSupplementCreateError(err error) bool {
	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr == nil {
		return false
	}
	return reasonErr.Reason == "invalid_case_ids" || reasonErr.Reason == "too_many_cases"
}

// MarkNodesMerged implements executionapp.SupplementPort. BatchService.Create
// already marks the selected nodes; this second call is idempotent and only
// logged by the worker.
func (p *SupplementPort) MarkNodesMerged(ctx context.Context, versionID int64, caseIDs []int64, _ int64) error {
	if p == nil || p.Mindmap == nil {
		return errors.New("execution mindmap: mindmap merger is not configured")
	}
	return p.Mindmap.MarkMerged(ctx, versionID, caseIDs)
}

// supplementResult is the run-result subset the worker reads: the materialized
// new case ids, the degraded flag and the validated requirement-gap escalations.
// Ids are accepted as JSON strings or numbers because the agent tool store and
// the materializer disagree.
type supplementResult struct {
	NewCaseIDs  []int64
	Degraded    bool
	Escalations []executionapp.RequirementGapEscalation
}

// supplementResultEscalation is the stored escalation shape (spec §9.2): title,
// detail and the requirement ids proving the gap.
type supplementResultEscalation struct {
	Title          string   `json:"title"`
	Detail         string   `json:"detail"`
	RequirementIDs []jsonID `json:"requirementIds"`
}

// jsonID is a decimal id encoded as a JSON string or number.
type jsonID int64

// UnmarshalJSON implements json.Unmarshaler.
func (j *jsonID) UnmarshalJSON(raw []byte) error {
	trimmed := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if trimmed == "" || trimmed == "null" {
		*j = 0
		return nil
	}
	var value int64
	if _, err := fmt.Sscan(trimmed, &value); err != nil {
		return fmt.Errorf("invalid id %q", trimmed)
	}
	*j = jsonID(value)
	return nil
}

// decodeSupplementResult tolerantly decodes the run result; a malformed
// document yields no cases, no escalations and no degraded flag.
func decodeSupplementResult(raw []byte) supplementResult {
	var document struct {
		NewCaseIDs  []jsonID                     `json:"newCaseIds"`
		Degraded    bool                         `json:"degraded"`
		Escalations []supplementResultEscalation `json:"escalations"`
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		_ = json.Unmarshal(raw, &document)
	}
	result := supplementResult{Degraded: document.Degraded}
	for _, id := range document.NewCaseIDs {
		if id > 0 {
			result.NewCaseIDs = append(result.NewCaseIDs, int64(id))
		}
	}
	for _, escalation := range document.Escalations {
		requirementIDs := make([]int64, 0, len(escalation.RequirementIDs))
		for _, id := range escalation.RequirementIDs {
			if id > 0 {
				requirementIDs = append(requirementIDs, int64(id))
			}
		}
		if len(requirementIDs) == 0 || strings.TrimSpace(escalation.Title) == "" {
			continue
		}
		result.Escalations = append(result.Escalations, executionapp.RequirementGapEscalation{
			Title:          escalation.Title,
			Detail:         escalation.Detail,
			RequirementIDs: requirementIDs,
		})
	}
	return result
}

// mindmapReason returns the stable reason of a mindmap domain error, or "".
func mindmapReason(err error) string {
	var domainErr *mindmapdomain.Error
	if errors.As(err, &domainErr) && domainErr != nil {
		return domainErr.Reason
	}
	return ""
}
