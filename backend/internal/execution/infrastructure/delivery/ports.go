package delivery

import (
	"context"
	"errors"
	"fmt"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliveryagenttool "github.com/EdouardRichard/EzTest/backend/internal/delivery/application/agent_tool"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// AnalysisExecution implements delivery/application.AnalysisExecutionReader over
// the execution read store. The failure_class filter runs in SQL before LIMIT
// (spec §5.6) and EvidenceIDs is populated so the candidate/draft evidence
// whitelist never drops a cited id.
type AnalysisExecution struct {
	reads *ReadStore
}

// NewAnalysisExecution wires the failed-case reader.
func NewAnalysisExecution(reads *ReadStore) *AnalysisExecution {
	return &AnalysisExecution{reads: reads}
}

var _ deliveryapp.AnalysisExecutionReader = (*AnalysisExecution)(nil)

// ListFailedCases implements delivery/application.AnalysisExecutionReader.
func (a *AnalysisExecution) ListFailedCases(ctx context.Context, versionID, batchID int64, limit int) ([]deliveryapp.AnalysisFailedCase, error) {
	if a == nil || a.reads == nil {
		return nil, errors.New("execution delivery: failed-case reader is not configured")
	}
	if limit <= 0 {
		return []deliveryapp.AnalysisFailedCase{}, nil
	}
	rows, err := a.reads.FailedApplicationCases(ctx, versionID, batchID, limit)
	if err != nil {
		return nil, err
	}
	cases := make([]deliveryapp.AnalysisFailedCase, 0, len(rows))
	for _, row := range rows {
		cases = append(cases, deliveryapp.AnalysisFailedCase{
			CaseRunID:     row.CaseRunID,
			CaseNodeID:    row.CaseNodeID,
			FailureClass:  deliverydomain.FailureClassApplication,
			Title:         row.Title,
			FailureReason: row.FailureReason,
			EvidenceIDs:   append([]int64(nil), row.EvidenceIDs...),
		})
	}
	return cases, nil
}

// ReportExecution implements delivery/application.ReportExecutionReader. The
// batch/case-run reads come from execution-owned tables; the report header
// environment is resolved through the execution application's environment port
// and the degraded flag is derived from the batch's delivery analysis runs.
type ReportExecution struct {
	reads        *ReadStore
	environments executionapp.ReportEnvironmentSource
	analyses     deliveryapp.AnalysisStore
}

// NewReportExecution wires the report execution reader. environments and
// analyses are optional: an unwired port leaves the header empty / the report
// not degraded instead of failing generation.
func NewReportExecution(reads *ReadStore, environments executionapp.ReportEnvironmentSource, analyses deliveryapp.AnalysisStore) *ReportExecution {
	return &ReportExecution{reads: reads, environments: environments, analyses: analyses}
}

var _ deliveryapp.ReportExecutionReader = (*ReportExecution)(nil)

// GetReportBatch implements delivery/application.ReportExecutionReader.
func (r *ReportExecution) GetReportBatch(ctx context.Context, versionID, batchID int64) (deliveryapp.ReportBatchSource, error) {
	if r == nil || r.reads == nil {
		return deliveryapp.ReportBatchSource{}, errors.New("execution delivery: report reader is not configured")
	}
	batch, err := r.reads.GetReportBatch(ctx, versionID, batchID)
	if err != nil {
		if errors.Is(err, ErrBatchNotFound) {
			return deliveryapp.ReportBatchSource{}, fmt.Errorf("%w: batch %d", executionapp.ErrBatchNotFound, batchID)
		}
		return deliveryapp.ReportBatchSource{}, err
	}
	flaky, err := r.reads.FlakyCaseIDs(ctx, batchID)
	if err != nil {
		return deliveryapp.ReportBatchSource{}, err
	}
	source := deliveryapp.ReportBatchSource{
		ID:                batch.ID,
		VersionID:         batch.VersionID,
		BatchNo:           batch.BatchNo,
		Kind:              batch.Kind,
		Status:            batch.Status,
		BaselineHash:      batch.BaselineHash,
		GraphHash:         batch.GraphHash,
		CodeBaseCommit:    batch.CodeBaseCommit,
		HeadCommit:        batch.HeadCommit,
		StartedAt:         batch.StartedAt,
		FinishedAt:        batch.FinishedAt,
		CaseCount:         batch.CaseCount,
		PassedCount:       batch.PassedCount,
		FailedCount:       batch.FailedCount,
		BlockedCount:      batch.BlockedCount,
		InconclusiveCount: batch.InconclusiveCount,
		SkippedCount:      batch.SkippedCount,
		InterruptedCount:  batch.InterruptedCount,
		FlakyCaseIDs:      flaky,
		Degraded:          r.degraded(ctx, versionID, batchID),
	}
	if r.environments != nil {
		environment, envErr := r.environments.ReportEnvironment(ctx, versionID)
		if envErr != nil {
			return deliveryapp.ReportBatchSource{}, fmt.Errorf("execution delivery: report environment: %w", envErr)
		}
		source.System = environment.System
		source.Environment = environment.Environment
	}
	return source, nil
}

// ListReportCaseRuns implements delivery/application.ReportExecutionReader.
func (r *ReportExecution) ListReportCaseRuns(ctx context.Context, batchID int64) ([]deliveryapp.ReportCaseRunSource, error) {
	if r == nil || r.reads == nil {
		return nil, errors.New("execution delivery: report reader is not configured")
	}
	rows, err := r.reads.LatestReportCaseRuns(ctx, batchID)
	if err != nil {
		return nil, err
	}
	runs := make([]deliveryapp.ReportCaseRunSource, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, deliveryapp.ReportCaseRunSource{
			CaseRunID:      row.CaseRunID,
			CaseNodeID:     row.CaseNodeID,
			Title:          row.Title,
			Source:         row.Source,
			Status:         row.Status,
			FailureClass:   row.FailureClass,
			Attempt:        row.Attempt,
			StepCount:      row.StepCount,
			AssertionCount: row.AssertionCount,
			EvidenceCount:  row.EvidenceCount,
			UpdatedAt:      row.UpdatedAt,
		})
	}
	return runs, nil
}

// PreviousBatchID implements delivery/application.ReportExecutionReader.
func (r *ReportExecution) PreviousBatchID(ctx context.Context, versionID, batchID int64) (int64, bool, error) {
	if r == nil || r.reads == nil {
		return 0, false, errors.New("execution delivery: report reader is not configured")
	}
	previous, ok, err := r.reads.PreviousBatchID(ctx, versionID, batchID)
	if errors.Is(err, ErrBatchNotFound) {
		return 0, false, fmt.Errorf("%w: batch %d", executionapp.ErrBatchNotFound, batchID)
	}
	if err != nil {
		return 0, false, err
	}
	return previous, ok, nil
}

// degraded reports whether the batch's AI analysis degraded or failed (spec
// §9.7): the report then renders the degraded warning. An unwired store is not
// degraded; a read failure is not degraded either (the report must still render).
func (r *ReportExecution) degraded(ctx context.Context, versionID, batchID int64) bool {
	if r.analyses == nil {
		return false
	}
	runs, err := r.analyses.List(ctx, versionID, batchID)
	if err != nil {
		return false
	}
	for _, run := range runs {
		if run.Status == deliverydomain.AnalysisDegraded || run.Status == deliverydomain.AnalysisFailed {
			return true
		}
	}
	return false
}

// CaseDetail implements delivery/application/agent_tool.CaseDetailReader: one
// failed(application) case run of the calling analysis scope, with recorded
// steps, assertions and evidence ids (metadata only, no artifact bytes).
type CaseDetail struct {
	reads *ReadStore
}

// NewCaseDetail wires the analysis case-detail reader.
func NewCaseDetail(reads *ReadStore) *CaseDetail {
	return &CaseDetail{reads: reads}
}

var _ deliveryagenttool.CaseDetailReader = (*CaseDetail)(nil)

// CaseDetail implements delivery/application/agent_tool.CaseDetailReader.
func (c *CaseDetail) CaseDetail(ctx context.Context, versionID, batchID, caseRunID int64) (deliveryagenttool.CaseDetail, bool, error) {
	if c == nil || c.reads == nil {
		return deliveryagenttool.CaseDetail{}, false, errors.New("execution delivery: case detail reader is not configured")
	}
	header, found, err := c.reads.CaseDetail(ctx, versionID, batchID, caseRunID)
	if err != nil || !found {
		return deliveryagenttool.CaseDetail{}, false, err
	}
	steps, err := c.reads.StepResults(ctx, caseRunID)
	if err != nil {
		return deliveryagenttool.CaseDetail{}, false, err
	}
	assertions, err := c.reads.AssertionResults(ctx, caseRunID)
	if err != nil {
		return deliveryagenttool.CaseDetail{}, false, err
	}
	evidenceIDs, err := c.reads.CaseEvidenceIDs(ctx, caseRunID)
	if err != nil {
		return deliveryagenttool.CaseDetail{}, false, err
	}
	detail := deliveryagenttool.CaseDetail{
		CaseRunID:     header.CaseRunID,
		CaseNodeID:    header.CaseNodeID,
		Title:         header.Title,
		Status:        header.Status,
		FailureClass:  header.FailureClass,
		FailureReason: header.FailureReason,
		Steps:         make([]deliveryagenttool.CaseDetailStep, 0, len(steps)),
		Assertions:    make([]deliveryagenttool.CaseDetailAssertion, 0, len(assertions)),
		EvidenceIDs:   evidenceIDs,
	}
	for _, step := range steps {
		detail.Steps = append(detail.Steps, deliveryagenttool.CaseDetailStep{
			Seq:           step.Seq,
			Action:        step.Action,
			Expected:      step.Expected,
			Actual:        step.Actual,
			Status:        step.Status,
			EvidenceCount: step.EvidenceCount,
		})
	}
	for _, assertion := range assertions {
		detail.Assertions = append(detail.Assertions, deliveryagenttool.CaseDetailAssertion{
			AssertionID:   assertion.AssertionID,
			Kind:          assertion.Kind,
			Target:        assertion.Target,
			Expected:      assertion.Expected,
			Actual:        assertion.Actual,
			Verdict:       assertion.Verdict,
			FailureReason: assertion.FailureReason,
		})
	}
	return detail, true, nil
}

// EvidenceMeta implements delivery/application/agent_tool.EvidenceMetaReader: the
// metadata (kind/sha256/size/mime) of evidence ids owned by the batch. The tool
// already filtered the ids against the failed cases; this reader only drops
// foreign ids.
type EvidenceMeta struct {
	reads *ReadStore
}

// NewEvidenceMeta wires the evidence metadata reader.
func NewEvidenceMeta(reads *ReadStore) *EvidenceMeta {
	return &EvidenceMeta{reads: reads}
}

var _ deliveryagenttool.EvidenceMetaReader = (*EvidenceMeta)(nil)

// EvidenceMeta implements delivery/application/agent_tool.EvidenceMetaReader.
func (e *EvidenceMeta) EvidenceMeta(ctx context.Context, versionID, batchID int64, evidenceIDs []int64) ([]deliveryagenttool.EvidenceMeta, error) {
	if e == nil || e.reads == nil {
		return nil, errors.New("execution delivery: evidence metadata reader is not configured")
	}
	rows, err := e.reads.EvidenceMetadata(ctx, versionID, batchID, evidenceIDs)
	if err != nil {
		return nil, err
	}
	metas := make([]deliveryagenttool.EvidenceMeta, 0, len(rows))
	for _, row := range rows {
		metas = append(metas, deliveryagenttool.EvidenceMeta{
			EvidenceID: row.EvidenceID,
			Kind:       row.Kind,
			SHA256:     row.SHA256,
			SizeBytes:  row.SizeBytes,
			Mime:       row.Mime,
		})
	}
	return metas, nil
}

// AnalysisPort adapts the delivery analysis service to the execution autonomous
// worker's AnalysisPort (spec §6.2/§6.3). A duplicate start is translated to the
// worker sentinel and keeps the existing run id so the chain continues it.
type AnalysisPort struct {
	Service AnalysisStarter
}

// AnalysisStarter is the delivery analysis surface the execution adapter needs.
// *delivery/application.AnalysisService satisfies it.
type AnalysisStarter interface {
	StartFailure(ctx context.Context, versionID, batchID, actor int64) (deliverydomain.AnalysisRun, error)
	StartTriage(ctx context.Context, versionID, batchID, actor int64) (deliverydomain.AnalysisRun, error)
	LatestByKind(ctx context.Context, batchID int64, kind string) (deliverydomain.AnalysisRun, error)
}

// NewAnalysisPort wires the execution-side failure/triage adapter.
func NewAnalysisPort(service AnalysisStarter) *AnalysisPort {
	return &AnalysisPort{Service: service}
}

var _ executionapp.AnalysisPort = (*AnalysisPort)(nil)

// StartFailureAnalysis implements executionapp.AnalysisPort.
func (p *AnalysisPort) StartFailureAnalysis(ctx context.Context, versionID, batchID, actor int64) (int64, error) {
	if p == nil || p.Service == nil {
		return 0, errors.New("execution delivery: analysis service is not configured")
	}
	run, err := p.Service.StartFailure(ctx, versionID, batchID, actor)
	return run.ID, mapAnalysisStartError(err, run.ID)
}

// StartIssueTriage implements executionapp.AnalysisPort.
func (p *AnalysisPort) StartIssueTriage(ctx context.Context, versionID, batchID, actor int64) (int64, error) {
	if p == nil || p.Service == nil {
		return 0, errors.New("execution delivery: analysis service is not configured")
	}
	run, err := p.Service.StartTriage(ctx, versionID, batchID, actor)
	return run.ID, mapAnalysisStartError(err, run.ID)
}

// GetAnalysis implements executionapp.AnalysisPort: the latest run of one
// (batch, kind); a missing run is (nil, nil).
func (p *AnalysisPort) GetAnalysis(ctx context.Context, batchID int64, kind string) (*executionapp.AnalysisView, error) {
	if p == nil || p.Service == nil {
		return nil, errors.New("execution delivery: analysis service is not configured")
	}
	run, err := p.Service.LatestByKind(ctx, batchID, kind)
	if errors.Is(err, deliveryapp.ErrAnalysisRunNotFound) ||
		deliverydomain.AnalysisReason(err) == deliverydomain.ReasonAnalysisNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	view := &executionapp.AnalysisView{
		ID:        run.ID,
		Kind:      run.Kind,
		Status:    run.Status,
		CreatedAt: run.CreatedAt,
		Degraded:  run.Status == deliverydomain.AnalysisDegraded,
	}
	if run.Kind == deliverydomain.AnalysisKindFailure {
		if result, decodeErr := deliverydomain.DecodeFailureResult(run.Result); decodeErr == nil {
			view.CandidateCount = len(result.Candidates)
		}
	}
	return view, nil
}

// mapAnalysisStartError translates a delivery analysis start error into the
// execution autonomy sentinels. A duplicate (run, ErrAnalysisConflict with
// reason analysis_active) keeps the existing run id.
func mapAnalysisStartError(err error, existingRunID int64) error {
	if err == nil {
		return nil
	}
	switch deliverydomain.AnalysisReason(err) {
	case deliverydomain.ReasonAnalysisActive:
		if existingRunID > 0 {
			return fmt.Errorf("%w: existing run %d", executionapp.ErrAutonomyAnalysisActive, existingRunID)
		}
		return executionapp.ErrAutonomyAnalysisActive
	case deliverydomain.ReasonAnalysisNotApplicable:
		return fmt.Errorf("%w: %v", executionapp.ErrAutonomyAnalysisNotApplicable, err)
	default:
		return err
	}
}

// ReportPort adapts the delivery report service to the execution autonomous
// worker's ReportPort (spec §6.2).
type ReportPort struct {
	Service ReportEnsurer
}

// ReportEnsurer is the delivery report surface the execution adapter needs.
// *delivery/application.ReportService satisfies it.
type ReportEnsurer interface {
	Ensure(ctx context.Context, versionID, batchID, actor int64) (deliverydomain.Report, bool, error)
	Regenerate(ctx context.Context, versionID, reportID, actor int64) (deliverydomain.Report, error)
	List(ctx context.Context, versionID int64, filter deliveryapp.ReportFilter) ([]deliverydomain.Report, bool, error)
}

// NewReportPort wires the execution-side report adapter.
func NewReportPort(service ReportEnsurer) *ReportPort {
	return &ReportPort{Service: service}
}

var _ executionapp.ReportPort = (*ReportPort)(nil)

// EnsureReport implements executionapp.ReportPort.
func (p *ReportPort) EnsureReport(ctx context.Context, versionID, batchID, actor int64) (int64, bool, error) {
	if p == nil || p.Service == nil {
		return 0, false, errors.New("execution delivery: report service is not configured")
	}
	report, created, err := p.Service.Ensure(ctx, versionID, batchID, actor)
	return report.ID, created, err
}

// RegenerateReport implements executionapp.ReportPort.
func (p *ReportPort) RegenerateReport(ctx context.Context, versionID, reportID, actor int64) (int64, error) {
	if p == nil || p.Service == nil {
		return 0, errors.New("execution delivery: report service is not configured")
	}
	report, err := p.Service.Regenerate(ctx, versionID, reportID, actor)
	return report.ID, err
}

// LatestReport implements executionapp.ReportPort: the newest revision of the
// batch, or nil when it has none.
func (p *ReportPort) LatestReport(ctx context.Context, versionID, batchID int64) (*executionapp.ReportStateView, error) {
	if p == nil || p.Service == nil {
		return nil, errors.New("execution delivery: report service is not configured")
	}
	batch := batchID
	reports, _, err := p.Service.List(ctx, versionID, deliveryapp.ReportFilter{BatchID: &batch, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return nil, nil
	}
	return &executionapp.ReportStateView{
		ID:       reports[0].ID,
		Status:   reports[0].Status,
		Revision: reports[0].Revision,
	}, nil
}
