package providers

import (
	"context"
	"strconv"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// executionPageSize is the page size used while draining the execution
// repository pagination (the repositories cap a page at 100).
const executionPageSize = 100

// SUTContextReader lists the per-run SUT contexts. The execution aggregate
// store satisfies it; the chain only needs the read side, so the provider does
// not depend on the full execution application persistence port.
type SUTContextReader interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error)
}

// ExecutionProvider supplies wave and case-run nodes. It consumes the execution
// application repository ports directly: chains are read-only, and the
// execution context exposes no separate chain projection service.
type ExecutionProvider struct {
	waves executionapp.WaveRepository
	runs  executionapp.CaseRunRepository
	suts  SUTContextReader
}

// NewExecutionProvider wires the wave, case-run and per-SUT-context readers.
func NewExecutionProvider(
	waves executionapp.WaveRepository,
	runs executionapp.CaseRunRepository,
	suts SUTContextReader,
) *ExecutionProvider {
	return &ExecutionProvider{waves: waves, runs: runs, suts: suts}
}

var _ chainapp.ExecutionChainProvider = (*ExecutionProvider)(nil)

// Waves returns every wave of the version with its status/counts metadata.
func (p *ExecutionProvider) Waves(ctx context.Context, versionID int64) ([]chain.Node, error) {
	waves, err := p.listWaves(ctx, versionID)
	if err != nil {
		return nil, err
	}
	nodes := make([]chain.Node, 0, len(waves))
	for _, wave := range waves {
		nodes = append(nodes, waveNode(wave))
	}
	return nodes, nil
}

// CaseRuns returns every case run of the version's waves. The version itself is
// not a case_run column, so the run set is reached through the wave set.
func (p *ExecutionProvider) CaseRuns(ctx context.Context, versionID int64) ([]chain.Node, error) {
	_, nodes, err := p.ExecutionSnapshot(ctx, versionID)
	return nodes, err
}

// ExecutionSnapshot is the P6 one-read fast path: the wave list is read once and
// every wave's runs are mapped in the same walk, so the chain service does not
// enumerate waves twice.
func (p *ExecutionProvider) ExecutionSnapshot(ctx context.Context, versionID int64) ([]chain.Node, []chain.Node, error) {
	waves, err := p.listWaves(ctx, versionID)
	if err != nil {
		return nil, nil, err
	}
	waveNodes := make([]chain.Node, 0, len(waves))
	runNodes := make([]chain.Node, 0)
	for _, wave := range waves {
		waveNodes = append(waveNodes, waveNode(wave))
		runs, err := p.runs.ListAllByWave(ctx, wave.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, run := range runs {
			node, err := p.caseRunNode(ctx, run)
			if err != nil {
				return nil, nil, err
			}
			runNodes = append(runNodes, node)
		}
	}
	return waveNodes, runNodes, nil
}

func (p *ExecutionProvider) listWaves(ctx context.Context, versionID int64) ([]execution.RunWave, error) {
	waves := make([]execution.RunWave, 0)
	var before *int64
	for {
		page, hasMore, err := p.waves.List(ctx, versionID, executionPageSize, before)
		if err != nil {
			return nil, err
		}
		waves = append(waves, page...)
		if !hasMore || len(page) == 0 {
			break
		}
		lastID := page[len(page)-1].ID
		if lastID <= 0 {
			break
		}
		before = &lastID
	}
	return waves, nil
}

func waveNode(wave execution.RunWave) chain.Node {
	node := chain.Node{
		ID:        chain.NodeID(chain.NodeWave, wave.ID),
		Kind:      chain.NodeWave,
		RefID:     strconv.FormatInt(wave.ID, 10),
		Label:     "Wave " + strconv.FormatInt(wave.ID, 10),
		Status:    string(wave.Status),
		WaveID:    int64Ptr(wave.ID),
		ReportID:  wave.ReportID,
		CreatedAt: wave.CreatedAt,
		UpdatedAt: wave.UpdatedAt,
		Meta: map[string]any{
			"trigger":        wave.Trigger,
			"wave_status":    string(wave.Status),
			"counts":         waveCountsMap(wave.Counts),
			"evidence_count": wave.Counts.Evidence,
		},
	}
	if len(wave.Counts.ByVersionSUT) > 0 {
		node.Meta["by_version_sut"] = wave.Counts.ByVersionSUT
	}
	if wave.StartedAt != nil {
		node.Meta["started_at"] = *wave.StartedAt
	}
	if wave.FinishedAt != nil {
		node.Meta["finished_at"] = *wave.FinishedAt
	}
	return node
}

// waveCountsMap flattens the wave counts into the node meta contract keys.
func waveCountsMap(counts execution.WaveCounts) map[string]int {
	return map[string]int{
		"total":        counts.Total,
		"pending":      counts.Pending,
		"running":      counts.Running,
		"paused":       counts.Paused,
		"passed":       counts.Passed,
		"failed":       counts.Failed,
		"blocked":      counts.Blocked,
		"inconclusive": counts.Inconclusive,
		"skipped":      counts.Skipped,
		"interrupted":  counts.Interrupted,
		"superseded":   counts.Superseded,
		"evidence":     counts.Evidence,
	}
}

// caseRunNode maps one case run. Step/assertion/evidence counts come from the
// persisted run summary; version SUT ids come from the run's SUT contexts so
// cross-SUT labels and the sut_id filter work without reading case tables.
func (p *ExecutionProvider) caseRunNode(ctx context.Context, run execution.CaseRun) (chain.Node, error) {
	node := chain.Node{
		ID:            chain.NodeID(chain.NodeCaseRun, run.ID),
		Kind:          chain.NodeCaseRun,
		RefID:         strconv.FormatInt(run.ID, 10),
		Label:         "Case run " + strconv.FormatInt(run.ID, 10),
		Status:        string(run.Status),
		Result:        caseRunResult(run.Status),
		CaseVersionID: optionalInt64Ptr(run.CaseVersionID),
		WaveID:        optionalInt64Ptr(run.WaveID),
		CreatedAt:     run.CreatedAt,
		UpdatedAt:     run.UpdatedAt,
		Meta: map[string]any{
			"attempt":                run.Attempts,
			"attempts":               run.Attempts,
			"step_count":             run.Summary.StepTotal,
			"step_failed_count":      run.Summary.StepFailed,
			"assertion_count":        run.Summary.AssertionTotal,
			"assertion_failed_count": run.Summary.AssertionFailed,
			"evidence_count":         run.Summary.EvidenceCount,
		},
	}
	if run.StartedAt != nil && run.FinishedAt != nil {
		if duration := run.FinishedAt.Sub(*run.StartedAt); duration >= 0 {
			node.Meta["duration_ms"] = duration.Milliseconds()
		}
	}
	if run.FailureClass != "" {
		node.Meta["failure_class"] = string(run.FailureClass)
	}
	if run.Summary.VerdictReason != "" {
		node.Meta["verdict_reason"] = run.Summary.VerdictReason
	}
	if p.suts != nil {
		contexts, err := p.suts.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return chain.Node{}, err
		}
		ids := make([]int64, 0, len(contexts))
		for _, sutContext := range contexts {
			ids = append(ids, sutContext.VersionSUTID)
		}
		node.VersionSUTIDs = sortUniqueIDs(ids)
		if len(node.VersionSUTIDs) == 1 {
			node.VersionSUTID = int64Ptr(node.VersionSUTIDs[0])
		}
	}
	return node, nil
}

// caseRunResult maps a terminal run status onto the result filter value. A
// non-terminal run has no result yet.
func caseRunResult(status execution.CaseRunStatus) string {
	switch status {
	case execution.CaseRunPassed:
		return "passed"
	case execution.CaseRunFailed:
		return "failed"
	case execution.CaseRunBlocked:
		return "blocked"
	case execution.CaseRunInconclusive:
		return "inconclusive"
	case execution.CaseRunSkipped:
		return "skipped"
	case execution.CaseRunInterrupted:
		return "interrupted"
	default:
		return ""
	}
}
