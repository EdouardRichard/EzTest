// Package adapters implements the delivery context's read ports over the
// version, design, execution and delivery application surfaces (spec §2.1:
// cross-context reads go through application ports, never through another
// context's tables).
package adapters

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// The adapter consumes narrow slices of the application ports so unit tests can
// substitute the repositories without depending on the full persistence
// interfaces. The concrete services and repositories of the other contexts
// satisfy these interfaces structurally.

// VersionReader is the version application detail read surface.
type VersionReader interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// RequirementReader is the version requirement read surface: paginated list.
type RequirementReader interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
}

// ClosureReader reads the newest requirement closure round.
type ClosureReader interface {
	Latest(ctx context.Context, requirementID int64) (version.RequirementClosure, bool, error)
}

// DesignReader is the design application case-version read surface: current+
// confirmed rows for execution/report projections and historical rows for
// superseded refs.
type DesignReader interface {
	ListExecutable(ctx context.Context, versionID int64) ([]designapp.ExecutableCaseView, error)
	ReadCaseVersionsByIDs(ctx context.Context, versionID int64, ids []int64) ([]designapp.CaseVersionHistoryView, error)
}

// WaveReader is the execution wave read surface.
type WaveReader interface {
	Get(ctx context.Context, id int64) (execution.RunWave, error)
	List(ctx context.Context, versionID int64, limit int, beforeID *int64) ([]execution.RunWave, bool, error)
}

// CaseRunReader is the execution case_run read surface.
type CaseRunReader interface {
	Get(ctx context.Context, id int64) (execution.CaseRun, error)
	ListAllByWave(ctx context.Context, waveID int64) ([]execution.CaseRun, error)
	LatestByCaseVersions(ctx context.Context, versionID int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error)
}

// ResultReader reads step/assertion results of a case run.
type ResultReader interface {
	ListSteps(ctx context.Context, caseRunID int64) ([]execution.StepResult, error)
	ListAssertions(ctx context.Context, caseRunID int64) ([]execution.AssertionResult, error)
}

// EvidenceReader reads immutable evidence metadata rows.
type EvidenceReader interface {
	ListByCaseRun(ctx context.Context, caseRunID int64, limit int) ([]execution.Evidence, error)
}

// SUTContextReader reads the per-run version SUT contexts.
type SUTContextReader interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error)
}

// IntentReader reads active execution intents (version report readiness).
type IntentReader interface {
	ListActive(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error)
}

// HumanReader reads open human requests (version report readiness).
type HumanReader interface {
	ListOpen(ctx context.Context) ([]execution.HumanRequest, error)
}

// IssueReader reads delivery issues and their logical refs.
type IssueReader interface {
	List(ctx context.Context, versionID int64, f deliveryapp.IssueFilter) ([]delivery.Issue, error)
	ListRefs(ctx context.Context, issueID int64) ([]delivery.IssueRef, error)
}

// Deps groups the application read surfaces of the delivery adapters.
type Deps struct {
	Versions     VersionReader
	Requirements RequirementReader
	Closures     ClosureReader
	Design       DesignReader
	Waves        WaveReader
	CaseRuns     CaseRunReader
	Results      ResultReader
	Evidence     EvidenceReader
	SUTContexts  SUTContextReader
	Intents      IntentReader
	Humans       HumanReader
	Issues       IssueReader
}

// pageSize is the shared page size of the application readers (all repositories
// cap a page at 100).
const pageSize = 100

// evidenceLimit is the per-case-run evidence cap of the execution context.
const evidenceLimit = execution.EvidenceMaxPerCaseRun

// readModel is the shared snapshot assembler of both delivery read adapters.
type readModel struct {
	deps Deps
}

// ExecutionReader implements deliveryapp.ExecutionReader with application-level
// snapshots (issue candidate ingestion and the scoped issue.read tool).
type ExecutionReader struct{ model *readModel }

// NewExecutionReader wires the issue-facing execution reader.
func NewExecutionReader(deps Deps) *ExecutionReader {
	return &ExecutionReader{model: &readModel{deps: deps}}
}

var _ deliveryapp.ExecutionReader = (*ExecutionReader)(nil)

// ReportDataReader implements deliveryapp.ReportDataReader and its optional
// ReportTriggerReader extension. It returns the deterministic §6.1 report input
// (already capped) for the report service.
type ReportDataReader struct{ model *readModel }

// NewReportDataReader wires the report-facing data reader.
func NewReportDataReader(deps Deps) *ReportDataReader {
	return &ReportDataReader{model: &readModel{deps: deps}}
}

var (
	_ deliveryapp.ReportDataReader    = (*ReportDataReader)(nil)
	_ deliveryapp.ReportTriggerReader = (*ReportDataReader)(nil)
)

// caseInfo is the design-side projection of one case version.
type caseInfo struct {
	caseID               int64
	requirementID        int64
	requirementVersionID int64
	title                string
	priority             string
	versionSUTIDs        []int64
}

// assembledRun carries one case_run in both the application and the report
// projection. EvidenceIDs is only populated for the application projection.
type assembledRun struct {
	app   deliveryapp.CaseRunSnapshot
	model delivery.CaseRunSnapshot
	// hasFailedAssertion reports whether the run carries a failed assertion
	// result (the P3 precondition for an issue candidate / failure detail).
	hasFailedAssertion bool
}

// caseRunsOfVersion returns every case_run of a version ordered by ID, reached
// through the version's waves (case_run has no version column).
func (m *readModel) caseRunsOfVersion(ctx context.Context, versionID int64) ([]execution.CaseRun, error) {
	waves, err := m.waves(ctx, versionID)
	if err != nil {
		return nil, err
	}
	runs := make([]execution.CaseRun, 0)
	for _, wave := range waves {
		rows, err := m.deps.CaseRuns.ListAllByWave(ctx, wave.ID)
		if err != nil {
			return nil, fmt.Errorf("list case runs of wave %d: %w", wave.ID, err)
		}
		runs = append(runs, rows...)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, nil
}

// waves drains the descending wave pages of a version.
func (m *readModel) waves(ctx context.Context, versionID int64) ([]execution.RunWave, error) {
	waves := make([]execution.RunWave, 0)
	var before *int64
	for {
		page, hasMore, err := m.deps.Waves.List(ctx, versionID, pageSize, before)
		if err != nil {
			return nil, fmt.Errorf("list waves: %w", err)
		}
		waves = append(waves, page...)
		if !hasMore || len(page) == 0 {
			break
		}
		last := page[len(page)-1].ID
		if last <= 0 {
			break
		}
		before = &last
	}
	sort.Slice(waves, func(i, j int) bool { return waves[i].ID < waves[j].ID })
	return waves, nil
}

// caseIndex maps case version IDs onto their design projection. Current+
// confirmed rows come from ListExecutable; rows referenced by historical runs
// are completed through the historical reader (title/status only).
func (m *readModel) caseIndex(ctx context.Context, versionID int64, runs []execution.CaseRun) (map[int64]caseInfo, error) {
	index := make(map[int64]caseInfo)
	views, err := m.deps.Design.ListExecutable(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("list executable cases: %w", err)
	}
	for _, view := range views {
		if view.CaseVersionID <= 0 {
			continue
		}
		index[view.CaseVersionID] = caseInfo{
			caseID:               view.CaseID,
			requirementID:        view.RequirementID,
			requirementVersionID: view.RequirementVersionID,
			title:                view.Title,
			priority:             view.Priority,
			versionSUTIDs:        append([]int64(nil), view.VersionSUTIDs...),
		}
	}
	missing := make([]int64, 0)
	seen := make(map[int64]bool)
	for _, run := range runs {
		if run.CaseVersionID <= 0 || seen[run.CaseVersionID] {
			continue
		}
		if _, ok := index[run.CaseVersionID]; ok {
			continue
		}
		seen[run.CaseVersionID] = true
		missing = append(missing, run.CaseVersionID)
	}
	if len(missing) == 0 {
		return index, nil
	}
	historical, err := m.deps.Design.ReadCaseVersionsByIDs(ctx, versionID, missing)
	if err != nil {
		return nil, fmt.Errorf("read historical case versions: %w", err)
	}
	for _, row := range historical {
		if row.ID <= 0 {
			continue
		}
		index[row.ID] = caseInfo{title: row.Title}
	}
	return index, nil
}

// buildCaseRun assembles one case_run projection. detail also reads the step
// results and always reads the failed assertions; the report projection reads
// the summary counters for the step/assertion counts.
func (m *readModel) buildCaseRun(ctx context.Context, versionID int64, run execution.CaseRun, info caseInfo, detail bool) (assembledRun, error) {
	var result assembledRun
	runVersionSUTIDs := append([]int64(nil), info.versionSUTIDs...)
	if len(runVersionSUTIDs) == 0 {
		contexts, err := m.deps.SUTContexts.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return assembledRun{}, fmt.Errorf("list SUT contexts of case run %d: %w", run.ID, err)
		}
		seen := make(map[int64]bool, len(contexts))
		for _, item := range contexts {
			if item.VersionSUTID <= 0 || seen[item.VersionSUTID] {
				continue
			}
			seen[item.VersionSUTID] = true
			runVersionSUTIDs = append(runVersionSUTIDs, item.VersionSUTID)
		}
		sort.Slice(runVersionSUTIDs, func(i, j int) bool { return runVersionSUTIDs[i] < runVersionSUTIDs[j] })
	}

	var steps []execution.StepResult
	if detail {
		if m.deps.Results == nil {
			return assembledRun{}, platform.NewError(500, "internal", "delivery adapter has no result reader")
		}
		rows, err := m.deps.Results.ListSteps(ctx, run.ID)
		if err != nil {
			return assembledRun{}, fmt.Errorf("list steps of case run %d: %w", run.ID, err)
		}
		steps = rows
	}
	var assertions []execution.AssertionResult
	needsFailureDetail := detail || run.Status == execution.CaseRunFailed ||
		run.Status == execution.CaseRunBlocked || run.Status == execution.CaseRunInconclusive
	if needsFailureDetail {
		rows, err := m.deps.Results.ListAssertions(ctx, run.ID)
		if err != nil {
			return assembledRun{}, fmt.Errorf("list assertions of case run %d: %w", run.ID, err)
		}
		assertions = rows
	}
	var failedAssertions []execution.AssertionResult
	var firstFailed *execution.AssertionResult
	for index := range assertions {
		if assertions[index].Verdict != execution.AssertionFailed {
			continue
		}
		failedAssertions = append(failedAssertions, assertions[index])
		if firstFailed == nil {
			firstFailed = &assertions[index]
		}
	}
	result.hasFailedAssertion = firstFailed != nil

	evidenceRows := make([]execution.Evidence, 0)
	if m.deps.Evidence != nil {
		rows, err := m.deps.Evidence.ListByCaseRun(ctx, run.ID, evidenceLimit)
		if err != nil {
			return assembledRun{}, fmt.Errorf("list evidence of case run %d: %w", run.ID, err)
		}
		evidenceRows = rows
	}

	stepCount := run.Summary.StepTotal
	stepFailedCount := run.Summary.StepFailed
	if detail && len(steps) > 0 {
		stepCount = len(steps)
		stepFailedCount = 0
		for _, step := range steps {
			if step.Status == execution.StepFailed {
				stepFailedCount++
			}
		}
	}
	assertionCount := run.Summary.AssertionTotal
	assertionFailedCount := run.Summary.AssertionFailed
	if len(assertions) > 0 {
		assertionCount = len(assertions)
		assertionFailedCount = len(failedAssertions)
	}

	category := ""
	severity := ""
	var failedStepSeq, failedAssertionSeq int
	var failedStepResultID int64
	failedAssertionIDs := make([]int64, 0, len(failedAssertions))
	var failedExpected, failedActual, failedReason string
	var versionSUTID *int64
	if firstFailed != nil {
		category = failureCategory(firstFailed.Kind)
		severity = failureSeverity(info.priority)
		failedStepSeq = firstFailed.StepSeq
		failedAssertionSeq = firstFailed.AssertionSeq
		failedExpected = firstFailed.Expected
		failedActual = firstFailed.Actual
		failedReason = firstFailed.Reason
		for _, assertion := range failedAssertions {
			if assertion.ID > 0 {
				failedAssertionIDs = append(failedAssertionIDs, assertion.ID)
			}
		}
		if firstFailed.TargetVersionSUTID != nil && *firstFailed.TargetVersionSUTID > 0 {
			versionSUTID = int64Ptr(*firstFailed.TargetVersionSUTID)
		}
		for _, step := range steps {
			if step.Seq == firstFailed.StepSeq {
				failedStepResultID = step.ID
				break
			}
		}
	}
	if versionSUTID == nil && len(runVersionSUTIDs) > 0 {
		versionSUTID = int64Ptr(runVersionSUTIDs[0])
	}

	evidenceIDs := make([]int64, 0, len(evidenceRows))
	modelEvidence := make([]delivery.EvidenceSnapshot, 0, len(evidenceRows))
	appEvidence := make([]deliveryapp.EvidenceSnapshot, 0, len(evidenceRows))
	for _, row := range evidenceRows {
		if row.ID <= 0 {
			continue
		}
		if row.CaseRunID <= 0 {
			row.CaseRunID = run.ID
		}
		evidenceIDs = append(evidenceIDs, row.ID)
		modelEvidence = append(modelEvidence, delivery.EvidenceSnapshot{
			ID: row.ID, CaseRunID: row.CaseRunID, VersionSUTID: row.VersionSUTID,
			StepSeq: row.StepSeq, AssertionSeq: row.AssertionSeq,
			Kind: string(row.Kind), SHA256: row.SHA256, SizeBytes: row.SizeBytes,
		})
		appEvidence = append(appEvidence, deliveryapp.EvidenceSnapshot{
			ID: row.ID, CaseRunID: row.CaseRunID, VersionSUTID: row.VersionSUTID,
			StepSeq: row.StepSeq, AssertionSeq: row.AssertionSeq,
			Kind: string(row.Kind), SHA256: row.SHA256, SizeBytes: row.SizeBytes,
		})
	}

	result.app = deliveryapp.CaseRunSnapshot{
		ID: run.ID, VersionID: versionID, WaveID: run.WaveID, CaseVersionID: run.CaseVersionID,
		RequirementID: info.requirementID, CaseTitle: info.title,
		VersionSUTID: versionSUTID, VersionSUTIDs: runVersionSUTIDs,
		Status: string(run.Status), FailureClass: string(run.FailureClass),
		Summary:         run.Summary.LastError,
		StepCount:       stepCount,
		StepFailedCount: stepFailedCount,
		AssertionCount:  assertionCount, AssertionFailedCount: assertionFailedCount,
		FailedStepSeq: failedStepSeq, FailedAssertionSeq: failedAssertionSeq,
		FailedStepResultID: failedStepResultID, FailedAssertionResultIDs: failedAssertionIDs,
		FailedExpected: failedExpected, FailedActual: failedActual, FailedReason: failedReason,
		Category: category, Severity: severity, EvidenceIDs: evidenceIDs,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt,
	}
	result.model = delivery.CaseRunSnapshot{
		ID: run.ID, VersionID: versionID, WaveID: run.WaveID, CaseVersionID: run.CaseVersionID,
		CaseTitle: info.title, VersionSUTID: versionSUTID, VersionSUTIDs: runVersionSUTIDs,
		Status: string(run.Status), FailureClass: string(run.FailureClass),
		Summary:         run.Summary.LastError,
		StepCount:       stepCount,
		StepFailedCount: stepFailedCount,
		AssertionCount:  assertionCount, AssertionFailedCount: assertionFailedCount,
		FailedStepSeq: failedStepSeq, FailedAssertionSeq: failedAssertionSeq,
		Category: category, Severity: severity, Evidence: modelEvidence,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt,
	}
	return result, nil
}

// ListFailedApplicationCaseRuns implements deliveryapp.ExecutionReader.
func (r *ExecutionReader) ListFailedApplicationCaseRuns(ctx context.Context, versionID int64) ([]deliveryapp.CaseRunSnapshot, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	runs, err := r.model.caseRunsOfVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	index, err := r.model.caseIndex(ctx, versionID, runs)
	if err != nil {
		return nil, err
	}
	snapshots := make([]deliveryapp.CaseRunSnapshot, 0)
	for _, run := range runs {
		if run.Status != execution.CaseRunFailed || run.FailureClass != execution.FailureClassApplication {
			continue
		}
		assembled, err := r.model.buildCaseRun(ctx, versionID, run, index[run.CaseVersionID], true)
		if err != nil {
			return nil, err
		}
		// P3 emits an issue candidate only for a failed(application) run with
		// failed assertion evidence; a claim without evidence is inconclusive.
		if !assembled.hasFailedAssertion {
			continue
		}
		snapshots = append(snapshots, assembled.app)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].ID < snapshots[j].ID })
	return snapshots, nil
}

// CaseRunSnapshot implements deliveryapp.ExecutionReader.
func (r *ExecutionReader) CaseRunSnapshot(ctx context.Context, caseRunID int64) (deliveryapp.CaseRunSnapshot, error) {
	if caseRunID <= 0 {
		return deliveryapp.CaseRunSnapshot{}, platform.Invalid("case_run_id", "must be a positive decimal ID")
	}
	run, err := r.model.deps.CaseRuns.Get(ctx, caseRunID)
	if err != nil {
		return deliveryapp.CaseRunSnapshot{}, mapExecutionError(err)
	}
	wave, err := r.model.deps.Waves.Get(ctx, run.WaveID)
	if err != nil {
		return deliveryapp.CaseRunSnapshot{}, mapExecutionError(err)
	}
	index, err := r.model.caseIndex(ctx, wave.VersionID, []execution.CaseRun{run})
	if err != nil {
		return deliveryapp.CaseRunSnapshot{}, err
	}
	assembled, err := r.model.buildCaseRun(ctx, wave.VersionID, run, index[run.CaseVersionID], true)
	if err != nil {
		return deliveryapp.CaseRunSnapshot{}, err
	}
	return assembled.app, nil
}

// WaveSnapshot implements deliveryapp.ExecutionReader with the application
// projection of one wave.
func (r *ExecutionReader) WaveSnapshot(ctx context.Context, versionID, waveID int64) (deliveryapp.WaveSnapshot, error) {
	model, err := r.model.waveModel(ctx, versionID, waveID)
	if err != nil {
		return deliveryapp.WaveSnapshot{}, err
	}
	return r.model.appWaveSnapshot(model), nil
}

// VersionSnapshot implements deliveryapp.ExecutionReader with the application
// projection of a version.
func (r *ExecutionReader) VersionSnapshot(ctx context.Context, versionID int64) (deliveryapp.VersionSnapshot, error) {
	model, err := r.model.versionModel(ctx, versionID)
	if err != nil {
		return deliveryapp.VersionSnapshot{}, err
	}
	view, err := r.model.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return deliveryapp.VersionSnapshot{}, mapVersionError(err)
	}
	return r.model.appVersionSnapshot(model, view), nil
}

// waveModel assembles the report-style model of one wave (header, runs, issues
// and version SUTs). It is shared by the app and report projections.
func (m *readModel) waveModel(ctx context.Context, versionID, waveID int64) (delivery.ReportSnapshot, error) {
	if versionID <= 0 || waveID <= 0 {
		return delivery.ReportSnapshot{}, platform.Invalid("wave_id", "must be a positive decimal ID")
	}
	wave, err := m.deps.Waves.Get(ctx, waveID)
	if err != nil {
		return delivery.ReportSnapshot{}, mapExecutionError(err)
	}
	if wave.VersionID != versionID {
		return delivery.ReportSnapshot{}, platform.NotFound("run wave")
	}
	runs, err := m.deps.CaseRuns.ListAllByWave(ctx, waveID)
	if err != nil {
		return delivery.ReportSnapshot{}, fmt.Errorf("list case runs of wave %d: %w", waveID, err)
	}
	index, err := m.caseIndex(ctx, versionID, runs)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	modelRuns, _, _, err := m.buildRuns(ctx, versionID, runs, index, false)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	waveReport := waveReportSnapshot(wave)
	versionSUTs, err := m.versionSUTs(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	issueSnapshots, err := m.issueSnapshots(ctx, versionID, &waveID, runIDSet(runs))
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	return delivery.CapReportSnapshot(delivery.ReportSnapshot{
		Kind:        delivery.ReportKindWave,
		VersionID:   versionID,
		WaveID:      int64Ptr(waveID),
		Waves:       []delivery.WaveSnapshot{waveReport},
		CaseRuns:    modelRuns,
		Issues:      issueSnapshots,
		VersionSUTs: versionSUTs,
	}), nil
}

// versionModel assembles the full deterministic version snapshot.
func (m *readModel) versionModel(ctx context.Context, versionID int64) (delivery.ReportSnapshot, error) {
	if versionID <= 0 {
		return delivery.ReportSnapshot{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	waves, err := m.waves(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	runs := make([]execution.CaseRun, 0)
	for _, wave := range waves {
		rows, err := m.deps.CaseRuns.ListAllByWave(ctx, wave.ID)
		if err != nil {
			return delivery.ReportSnapshot{}, fmt.Errorf("list case runs of wave %d: %w", wave.ID, err)
		}
		runs = append(runs, rows...)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	index, err := m.caseIndex(ctx, versionID, runs)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	modelRuns, _, _, err := m.buildRuns(ctx, versionID, runs, index, false)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	requirements, requirementViews, err := m.requirementSnapshots(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	versionSUTs, err := m.versionSUTs(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	coverage, err := m.coverage(ctx, versionID, requirementViews)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	issueSnapshots, err := m.issueSnapshots(ctx, versionID, nil, nil)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	waveSnapshots := make([]delivery.WaveSnapshot, 0, len(waves))
	for _, wave := range waves {
		waveSnapshots = append(waveSnapshots, waveReportSnapshot(wave))
	}
	return delivery.CapReportSnapshot(delivery.ReportSnapshot{
		Kind:         delivery.ReportKindVersion,
		VersionID:    versionID,
		Requirements: requirements,
		Waves:        waveSnapshots,
		CaseRuns:     modelRuns,
		Issues:       issueSnapshots,
		VersionSUTs:  versionSUTs,
		Coverage:     coverage,
	}), nil
}

// buildRuns assembles every run and flattens its evidence rows.
func (m *readModel) buildRuns(ctx context.Context, versionID int64, runs []execution.CaseRun, index map[int64]caseInfo, detail bool) ([]delivery.CaseRunSnapshot, []deliveryapp.CaseRunSnapshot, []delivery.EvidenceSnapshot, error) {
	modelRuns := make([]delivery.CaseRunSnapshot, 0, len(runs))
	appRuns := make([]deliveryapp.CaseRunSnapshot, 0, len(runs))
	evidenceRows := make([]delivery.EvidenceSnapshot, 0)
	for _, run := range runs {
		assembled, err := m.buildCaseRun(ctx, versionID, run, index[run.CaseVersionID], detail)
		if err != nil {
			return nil, nil, nil, err
		}
		modelRuns = append(modelRuns, assembled.model)
		appRuns = append(appRuns, assembled.app)
		evidenceRows = append(evidenceRows, assembled.model.Evidence...)
	}
	return modelRuns, appRuns, evidenceRows, nil
}

// requirementSnapshots pages the version requirements and joins the newest
// closure round. It also returns the requirement views for coverage.
func (m *readModel) requirementSnapshots(ctx context.Context, versionID int64) ([]delivery.RequirementSnapshot, []versionapp.RequirementView, error) {
	if m.deps.Requirements == nil {
		return nil, nil, platform.NewError(500, "internal", "delivery adapter has no requirement reader")
	}
	views := make([]versionapp.RequirementView, 0)
	var before *int64
	for {
		page, hasMore, err := m.deps.Requirements.List(ctx, versionID, "", pageSize, before)
		if err != nil {
			return nil, nil, fmt.Errorf("list requirements: %w", err)
		}
		views = append(views, page...)
		if !hasMore || len(page) == 0 {
			break
		}
		last := page[len(page)-1].ID
		if last <= 0 {
			break
		}
		before = &last
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	snapshots := make([]delivery.RequirementSnapshot, 0, len(views))
	for _, view := range views {
		snapshot := delivery.RequirementSnapshot{
			ID:               view.ID,
			VersionID:        versionID,
			Title:            view.Title,
			Status:           view.Status,
			ClosedAt:         view.ClosedAt,
			CurrentVersionID: int64PtrOrNil(view.CurrentVersionID),
			UpdatedAt:        view.UpdatedAt,
		}
		if m.deps.Closures != nil {
			closure, found, err := m.deps.Closures.Latest(ctx, view.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("load closure of requirement %d: %w", view.ID, err)
			}
			if found {
				snapshot.ClosureID = int64Ptr(closure.ID)
				snapshot.CloseRound = int32(closure.CloseRound)
				snapshot.ReopenCount = closure.ReopenCount
				if snapshot.ClosedAt == nil {
					closedAt := closure.ClosedAt
					snapshot.ClosedAt = &closedAt
				}
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, views, nil
}

// coverage computes the P3 execution coverage projection: every current+
// confirmed case whose latest run did not pass is a gap.
func (m *readModel) coverage(ctx context.Context, versionID int64, requirements []versionapp.RequirementView) (delivery.CoverageSnapshot, error) {
	cases, err := m.deps.Design.ListExecutable(ctx, versionID)
	if err != nil {
		return delivery.CoverageSnapshot{}, fmt.Errorf("list executable cases: %w", err)
	}
	caseVersionIDs := make([]int64, 0, len(cases))
	for _, item := range cases {
		caseVersionIDs = append(caseVersionIDs, item.CaseVersionID)
	}
	latest := map[int64]execution.CaseRun{}
	if len(caseVersionIDs) > 0 {
		latest, err = m.deps.CaseRuns.LatestByCaseVersions(ctx, versionID, caseVersionIDs)
		if err != nil {
			return delivery.CoverageSnapshot{}, fmt.Errorf("latest case runs: %w", err)
		}
	}
	coverage := delivery.CoverageSnapshot{
		RequirementsTotal: len(requirements),
		CaseVersionsTotal: len(cases),
	}
	for _, item := range cases {
		if run, ok := latest[item.CaseVersionID]; ok && run.Status == execution.CaseRunPassed {
			coverage.CaseVersionsCovered++
		}
	}
	requirementsCovered := make(map[int64]bool, len(requirements))
	for _, item := range cases {
		if run, ok := latest[item.CaseVersionID]; ok && run.Status == execution.CaseRunPassed {
			requirementsCovered[item.RequirementID] = true
		}
	}
	for _, requirement := range requirements {
		if requirementsCovered[requirement.ID] {
			coverage.RequirementsCovered++
		}
	}
	for _, item := range cases {
		run, ok := latest[item.CaseVersionID]
		if ok && run.Status == execution.CaseRunPassed {
			continue
		}
		reason := "execution_not_run"
		if ok {
			reason = "execution_failed_coverage"
		}
		coverage.Gaps = append(coverage.Gaps, delivery.CoverageGapSnapshot{
			RequirementID: item.RequirementID,
			CaseVersionID: item.CaseVersionID,
			Reason:        reason,
		})
	}
	return coverage, nil
}

// versionSUTs maps the version bindings onto report labels.
func (m *readModel) versionSUTs(ctx context.Context, versionID int64) ([]delivery.VersionSUTSnapshot, error) {
	view, err := m.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return nil, mapVersionError(err)
	}
	out := make([]delivery.VersionSUTSnapshot, 0, len(view.Bindings))
	for _, binding := range view.Bindings {
		if binding.ID <= 0 {
			continue
		}
		sutID, _ := strconv.ParseInt(binding.SUT.ID, 10, 64)
		environmentID, _ := strconv.ParseInt(binding.Environment.ID, 10, 64)
		out = append(out, delivery.VersionSUTSnapshot{
			ID:              binding.ID,
			SUTID:           sutID,
			SUTName:         binding.SUT.Name,
			EnvironmentID:   environmentID,
			EnvironmentName: binding.Environment.Name,
		})
	}
	return out, nil
}

// issueSnapshots drains the version issues and joins refs plus the reverse
// merged_into projection. When waveID/runIDs are set only issues referencing
// that wave or one of its runs are returned.
func (m *readModel) issueSnapshots(ctx context.Context, versionID int64, waveID *int64, runIDs map[int64]bool) ([]delivery.IssueSnapshot, error) {
	if m.deps.Issues == nil {
		return nil, nil
	}
	issues := make([]delivery.Issue, 0)
	var before int64
	for {
		page, err := m.deps.Issues.List(ctx, versionID, deliveryapp.IssueFilter{Before: before, Limit: pageSize})
		if err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
		}
		issues = append(issues, page...)
		if len(page) < pageSize {
			break
		}
		last := page[len(page)-1].ID
		if last <= 0 || last == before {
			break
		}
		before = last
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
	refsByIssue := make(map[int64][]delivery.IssueRef, len(issues))
	mergedSources := make(map[int64][]int64)
	for _, issue := range issues {
		refs, err := m.deps.Issues.ListRefs(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("list issue refs %d: %w", issue.ID, err)
		}
		refsByIssue[issue.ID] = refs
		if issue.MergedIntoID != nil && *issue.MergedIntoID > 0 {
			mergedSources[*issue.MergedIntoID] = append(mergedSources[*issue.MergedIntoID], issue.ID)
		}
	}
	snapshots := make([]delivery.IssueSnapshot, 0, len(issues))
	for _, issue := range issues {
		refs := refsByIssue[issue.ID]
		if waveID != nil || runIDs != nil {
			if !issueMatchesWave(issue, refs, waveID, runIDs) {
				continue
			}
		}
		caseRunIDs := make([]int64, 0)
		for _, ref := range refs {
			if ref.Kind == delivery.IssueRefKindCaseRun && ref.RefID > 0 {
				caseRunIDs = append(caseRunIDs, ref.RefID)
			}
		}
		snapshots = append(snapshots, delivery.IssueSnapshot{
			ID:             issue.ID,
			VersionID:      issue.VersionID,
			Title:          issue.Title,
			Status:         issue.Status,
			Category:       issue.Category,
			Severity:       issue.Severity,
			Source:         issue.Source,
			MergedIntoID:   issue.MergedIntoID,
			SourceIssueIDs: mergedSources[issue.ID],
			CaseRunIDs:     caseRunIDs,
			CreatedAt:      issue.CreatedAt,
			UpdatedAt:      issue.UpdatedAt,
		})
	}
	return snapshots, nil
}

// issueMatchesWave reports whether the issue references the wave or one of its
// runs.
func issueMatchesWave(issue delivery.Issue, refs []delivery.IssueRef, waveID *int64, runIDs map[int64]bool) bool {
	_ = issue
	if waveID != nil {
		for _, ref := range refs {
			if ref.Kind == delivery.IssueRefKindWave && ref.RefID == *waveID {
				return true
			}
		}
	}
	for _, ref := range refs {
		if ref.Kind == delivery.IssueRefKindCaseRun && runIDs[ref.RefID] {
			return true
		}
	}
	return false
}

// runIDSet indexes the wave's run IDs.
func runIDSet(runs []execution.CaseRun) map[int64]bool {
	set := make(map[int64]bool, len(runs))
	for _, run := range runs {
		set[run.ID] = true
	}
	return set
}

// waveReportSnapshot projects one execution wave onto the report header.
func waveReportSnapshot(wave execution.RunWave) delivery.WaveSnapshot {
	byVersionSUT := make([]delivery.SUTCountsSnapshot, 0, len(wave.Counts.ByVersionSUT))
	for rawID, counts := range wave.Counts.ByVersionSUT {
		versionSUTID := int64(0)
		if _, err := fmt.Sscanf(rawID, "%d", &versionSUTID); err != nil || versionSUTID <= 0 {
			continue
		}
		byVersionSUT = append(byVersionSUT, delivery.SUTCountsSnapshot{
			VersionSUTID: versionSUTID,
			Total:        counts.Total,
			Passed:       counts.Passed,
			Failed:       counts.Failed,
			Blocked:      counts.Blocked,
			Inconclusive: counts.Inconclusive,
		})
	}
	sort.Slice(byVersionSUT, func(i, j int) bool { return byVersionSUT[i].VersionSUTID < byVersionSUT[j].VersionSUTID })
	return delivery.WaveSnapshot{
		ID:        wave.ID,
		VersionID: wave.VersionID,
		Status:    string(wave.Status),
		Trigger:   wave.Trigger,
		Counts: delivery.WaveCountsSnapshot{
			Total:        wave.Counts.Total,
			Pending:      wave.Counts.Pending,
			Running:      wave.Counts.Running,
			Paused:       wave.Counts.Paused,
			Passed:       wave.Counts.Passed,
			Failed:       wave.Counts.Failed,
			Blocked:      wave.Counts.Blocked,
			Inconclusive: wave.Counts.Inconclusive,
			Skipped:      wave.Counts.Skipped,
			Interrupted:  wave.Counts.Interrupted,
			Superseded:   wave.Counts.Superseded,
			Evidence:     wave.Counts.Evidence,
		},
		ByVersionSUT: byVersionSUT,
		ReportID:     wave.ReportID,
		StartedAt:    wave.StartedAt,
		FinishedAt:   wave.FinishedAt,
	}
}

// failureCategory mirrors the P3-D-12 deterministic category derivation.
func failureCategory(assertionKind string) string {
	switch assertionKind {
	case execution.AssertionKindDB:
		return "db"
	case execution.AssertionKindAPI:
		return "api"
	default:
		return "ui"
	}
}

// failureSeverity mirrors the P3-D-12 deterministic severity derivation.
func failureSeverity(priority string) string {
	switch priority {
	case "P0", "P1":
		return "major"
	case "P2":
		return "minor"
	default:
		return "trivial"
	}
}

// mapExecutionError maps execution not-found onto the delivery 404.
func mapExecutionError(err error) error {
	if err == nil {
		return nil
	}
	if err == execution.ErrNotFound {
		return platform.NotFound("execution entity")
	}
	return err
}

// mapVersionError maps version not-found onto the delivery 404.
func mapVersionError(err error) error {
	if err == nil {
		return nil
	}
	if err == version.ErrNotFound {
		return platform.NotFound("version")
	}
	return err
}

func int64Ptr(value int64) *int64 { return &value }

func int64PtrOrNil(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

// ---------------------------------------------------------------------------
// ReportDataReader (domain report snapshots)
// ---------------------------------------------------------------------------

// WaveSnapshot implements deliveryapp.ReportDataReader.
func (r *ReportDataReader) WaveSnapshot(ctx context.Context, versionID, waveID int64) (delivery.ReportSnapshot, error) {
	return r.model.waveModel(ctx, versionID, waveID)
}

// VersionSnapshot implements deliveryapp.ReportDataReader.
func (r *ReportDataReader) VersionSnapshot(ctx context.Context, versionID int64) (delivery.ReportSnapshot, error) {
	return r.model.versionModel(ctx, versionID)
}

// VersionReportReady implements deliveryapp.ReportTriggerReader: the §7.4
// version-report condition excludes active execution intents and open human
// requests (the requirement/wave part is checked by the service on the
// snapshot).
func (r *ReportDataReader) VersionReportReady(ctx context.Context, versionID int64) (bool, error) {
	if versionID <= 0 {
		return false, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if r.model.deps.Intents != nil {
		active, err := r.model.deps.Intents.ListActive(ctx, versionID)
		if err != nil {
			return false, fmt.Errorf("list active intents: %w", err)
		}
		if len(active) > 0 {
			return false, nil
		}
	}
	if r.model.deps.Humans != nil {
		open, err := r.model.deps.Humans.ListOpen(ctx)
		if err != nil {
			return false, fmt.Errorf("list open human requests: %w", err)
		}
		for _, request := range open {
			if request.VersionID == versionID {
				return false, nil
			}
		}
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Application projections (deliveryapp ExecutionReader)
// ---------------------------------------------------------------------------

// appWaveSnapshot converts the deterministic wave model to the application
// wave projection (kept for the ExecutionReader contract; the report service
// consumes the domain model directly).
func (m *readModel) appWaveSnapshot(model delivery.ReportSnapshot) deliveryapp.WaveSnapshot {
	wave := deliveryapp.WaveSnapshot{VersionID: model.VersionID}
	if len(model.Waves) == 1 {
		source := model.Waves[0]
		counts := map[string]int{
			"total":        source.Counts.Total,
			"pending":      source.Counts.Pending,
			"running":      source.Counts.Running,
			"paused":       source.Counts.Paused,
			"passed":       source.Counts.Passed,
			"failed":       source.Counts.Failed,
			"blocked":      source.Counts.Blocked,
			"inconclusive": source.Counts.Inconclusive,
			"skipped":      source.Counts.Skipped,
			"interrupted":  source.Counts.Interrupted,
			"superseded":   source.Counts.Superseded,
			"evidence":     source.Counts.Evidence,
		}
		bySUT := make(map[int64]deliveryapp.SUTCounts, len(source.ByVersionSUT))
		for _, sut := range source.ByVersionSUT {
			bySUT[sut.VersionSUTID] = deliveryapp.SUTCounts{
				Total: sut.Total, Passed: sut.Passed, Failed: sut.Failed,
				Blocked: sut.Blocked, Inconclusive: sut.Inconclusive,
			}
		}
		wave.ID = source.ID
		wave.Status = source.Status
		wave.Trigger = source.Trigger
		wave.Counts = counts
		wave.ByVersionSUT = bySUT
		wave.ReportID = source.ReportID
		wave.StartedAt = source.StartedAt
		wave.FinishedAt = source.FinishedAt
	}
	for _, run := range model.CaseRuns {
		appRun := deliveryapp.CaseRunSnapshot{
			ID: run.ID, VersionID: run.VersionID, WaveID: run.WaveID, CaseVersionID: run.CaseVersionID,
			CaseTitle: run.CaseTitle, VersionSUTID: run.VersionSUTID, VersionSUTIDs: run.VersionSUTIDs,
			Status: run.Status, FailureClass: run.FailureClass, Summary: run.Summary,
			StepCount: run.StepCount, StepFailedCount: run.StepFailedCount,
			AssertionCount: run.AssertionCount, AssertionFailedCount: run.AssertionFailedCount,
			FailedStepSeq: run.FailedStepSeq, FailedAssertionSeq: run.FailedAssertionSeq,
			Category: run.Category, Severity: run.Severity,
			StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt,
		}
		for _, evidence := range run.Evidence {
			appRun.EvidenceIDs = append(appRun.EvidenceIDs, evidence.ID)
			wave.Evidence = append(wave.Evidence, deliveryapp.EvidenceSnapshot{
				ID: evidence.ID, CaseRunID: evidence.CaseRunID, VersionSUTID: evidence.VersionSUTID,
				StepSeq: evidence.StepSeq, AssertionSeq: evidence.AssertionSeq,
				Kind: evidence.Kind, SHA256: evidence.SHA256, SizeBytes: evidence.SizeBytes,
			})
		}
		wave.CaseRuns = append(wave.CaseRuns, appRun)
	}
	return wave
}

// appVersionSnapshot converts the deterministic version model to the
// application version projection.
func (m *readModel) appVersionSnapshot(model delivery.ReportSnapshot, view versionapp.VersionView) deliveryapp.VersionSnapshot {
	out := deliveryapp.VersionSnapshot{
		ID: view.ID, Title: view.Title, Status: view.Status,
		ClosedAt: view.ClosedAt, CloseReason: view.CloseReason,
		CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}
	for _, requirement := range model.Requirements {
		out.Requirements = append(out.Requirements, deliveryapp.RequirementSnapshot{
			ID: requirement.ID, VersionID: requirement.VersionID,
			CurrentVersionID: requirement.CurrentVersionID, Title: requirement.Title,
			Status: requirement.Status, ClosureID: requirement.ClosureID,
			ClosedAt: requirement.ClosedAt, CloseRound: requirement.CloseRound,
			ReopenCount: requirement.ReopenCount, UpdatedAt: requirement.UpdatedAt,
		})
	}
	for _, sut := range model.VersionSUTs {
		out.VersionSUTs = append(out.VersionSUTs, deliveryapp.VersionSUTSnapshot{
			ID: sut.ID, SUTID: sut.SUTID, SUTName: sut.SUTName,
			EnvironmentID: sut.EnvironmentID, EnvironmentName: sut.EnvironmentName,
		})
	}
	for _, wave := range model.Waves {
		out.Waves = append(out.Waves, m.appWaveSnapshot(delivery.ReportSnapshot{
			VersionID: model.VersionID, Waves: []delivery.WaveSnapshot{wave},
		}))
	}
	for _, run := range model.CaseRuns {
		out.CaseRuns = append(out.CaseRuns, deliveryapp.CaseRunSnapshot{
			ID: run.ID, VersionID: run.VersionID, WaveID: run.WaveID, CaseVersionID: run.CaseVersionID,
			CaseTitle: run.CaseTitle, VersionSUTID: run.VersionSUTID, VersionSUTIDs: run.VersionSUTIDs,
			Status: run.Status, FailureClass: run.FailureClass, Summary: run.Summary,
			StepCount: run.StepCount, StepFailedCount: run.StepFailedCount,
			AssertionCount: run.AssertionCount, AssertionFailedCount: run.AssertionFailedCount,
			FailedStepSeq: run.FailedStepSeq, FailedAssertionSeq: run.FailedAssertionSeq,
			Category: run.Category, Severity: run.Severity,
			StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt,
		})
	}
	out.Coverage = deliveryapp.CoverageSnapshot{
		RequirementsTotal:   model.Coverage.RequirementsTotal,
		RequirementsCovered: model.Coverage.RequirementsCovered,
		CaseVersionsTotal:   model.Coverage.CaseVersionsTotal,
		CaseVersionsCovered: model.Coverage.CaseVersionsCovered,
	}
	for _, gap := range model.Coverage.Gaps {
		out.Coverage.Gaps = append(out.Coverage.Gaps, deliveryapp.CoverageGapSnapshot{
			RequirementID: gap.RequirementID, CaseVersionID: gap.CaseVersionID, Reason: gap.Reason,
		})
	}
	return out
}
