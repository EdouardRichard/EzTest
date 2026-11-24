// Package adapters implements the delivery context's read ports over the
// version, design, execution and delivery application surfaces (spec §2.1:
// cross-context reads go through application ports, never through another
// context's tables).
package adapters

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
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

// RequirementReader is the version requirement read surface: paginated list
// plus the per-requirement detail (current revision user story / acceptance
// criteria / sut_description) the report's natural-language context consumes
// (v0.2.4-G SC-G2 C2).
type RequirementReader interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
	Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
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
	// LatestByCaseVersions returns the newest non-superseded run per case
	// version. The report coverage projection derives from the version run
	// snapshot instead of relying on this lookup.
	LatestByCaseVersions(ctx context.Context, versionID int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error)
}

// ResultReader reads step/assertion results of a case run.
type ResultReader interface {
	ListSteps(ctx context.Context, caseRunID int64) ([]execution.StepResult, error)
	ListAssertions(ctx context.Context, caseRunID int64) ([]execution.AssertionResult, error)
}

// EvidenceReader reads immutable evidence metadata rows. SHA256sByCaseRun is
// the NARROW full-set sha read feeding the causally-complete fingerprint digest
// (art.4.6); ListNewestByCaseRun reads only the bounded DISPLAY rows (full
// metadata) up to the report display cap (v0.2.4-G T8 fix round, SC-G7). The
// digest never depends on the capped display list.
type EvidenceReader interface {
	SHA256sByCaseRun(ctx context.Context, caseRunID int64) ([]string, error)
	ListNewestByCaseRun(ctx context.Context, caseRunID int64, limit int) ([]execution.Evidence, error)
}

// SUTContextReader reads the per-run version SUT contexts.
type SUTContextReader interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error)
}

// IntentReader reads execution intents: the active set (version report
// readiness) and a single intent by ID (the per-run ACTUAL frozen environment
// fingerprint/code baseline of the report, v0.2.4-G SC-G5/G6 R-G1 — read back
// as stored, never recomputed).
type IntentReader interface {
	ListActive(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error)
	GetByID(ctx context.Context, intentID int64) (execution.ExecutionIntent, error)
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

// TriageReader reads the latest finished issue.triage analysis run. It is
// optional: without it the report keeps the raw P3 failure classification
// (older wirings and unit fixtures never silently downgrade a defect).
type TriageReader interface {
	LatestByIssue(ctx context.Context, issueID int64) (delivery.AnalysisRun, bool, error)
}

// JudgmentReader reads the design judge read report (verdict/findings/
// review_state per current case) the report consumes for the case/AC coverage
// annotation (v0.2.4-G SC-G4 I3). *designapp.JudgmentService satisfies it. It is
// optional: an unwired port yields no judge annotation (older wirings); the
// production composition root always installs it (DoD no unwired code). A wired
// port that errors fails the snapshot build closed (no silent degradation).
type JudgmentReader interface {
	ListJudgments(ctx context.Context, versionID int64, requirementID *int64) (designapp.JudgmentReportView, error)
}

// WaveFrozenFingerprintReader rebuilds the wave-level frozen environment
// fingerprint and code baseline from the wave's frozen scope snapshot through
// execution.WaveFrozenFingerprint (v0.2.4-G SC-G6 P15, SC-F7 report leg). A legacy
// wave without a carrier is a typed error (fail-closed, no live-read fallback);
// the report surfaces it as a per-wave "环境不可核验" annotation. Optional: an
// unwired port leaves the wave environment fields unset.
type WaveFrozenFingerprintReader interface {
	WaveFrozenFingerprint(wave execution.RunWave) (string, string, error)
}

// WaiverFactReader reads the append-only version facts of one version by kind
// (v0.2.4-G SC-G11, T10). The report projects the admin waive-close reason and
// operator from the kind=waiver, key=version fact so the waived report lists WHY
// and WHO. It mirrors the version FactService.Current surface; optional: an
// unwired port leaves the waiver reason/operator empty (the waived badge still
// renders from the closure_kind/unverified columns). A wired port that errors
// fails the snapshot build closed (no silent degradation, art.7.6).
type WaiverFactReader interface {
	Current(ctx context.Context, versionID int64, kind string) ([]version.Fact, error)
}

// executionWaveFingerprintReader adapts the execution application's pure
// WaveFrozenFingerprint function onto the delivery read port. The delivery
// context consumes the frozen-snapshot reconstruction through this port rather
// than reaching into the execution internals (DDD cross-context read).
type executionWaveFingerprintReader struct{}

// NewExecutionWaveFingerprintReader wires the production wave frozen fingerprint
// consumer (executionapp.WaveFrozenFingerprint).
func NewExecutionWaveFingerprintReader() WaveFrozenFingerprintReader {
	return executionWaveFingerprintReader{}
}

func (executionWaveFingerprintReader) WaveFrozenFingerprint(wave execution.RunWave) (string, string, error) {
	return executionapp.WaveFrozenFingerprint(wave)
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
	Triage       TriageReader
	// Settings reads the report evidence display cap keys (v0.2.4-G SC-G7).
	// Optional: nil keeps the bounded defaults.
	Settings deliveryapp.SettingsReader
	// EvidenceRefs is the evidence_ref reverse-lookup read side (v0.2.4-G T4,
	// SC-G9): the report binds the representative run's failed assertion/step
	// evidence to the defect card. Optional: nil skips the card evidence.
	EvidenceRefs executionapp.EvidenceRefReader
	// Judgments is the design judge read port (v0.2.4-G T9, SC-G4 I3): the report
	// presents verdict/findings/review_state at the case/AC coverage area.
	// Optional: nil yields no judge annotation.
	Judgments JudgmentReader
	// WaveFingerprints is the wave frozen fingerprint consumer (v0.2.4-G T9,
	// SC-G6 P15): the report wave-level environment fingerprint rebuilds from the
	// frozen scope snapshot. Optional: nil leaves the wave environment unset.
	WaveFingerprints WaveFrozenFingerprintReader
	// WaiverFacts reads the version waiver fact (v0.2.4-G T10, SC-G11): the waived
	// report projects the admin waive-close reason/operator. Optional: nil leaves
	// the waiver reason/operator empty (the badge still renders from the columns).
	WaiverFacts WaiverFactReader
}

// pageSize is the shared page size of the application readers (all repositories
// cap a page at 100).
const pageSize = 100

// evidenceLimit bounds the issue-candidate evidence INGESTION projection (the
// app-facing CaseRunSnapshot.EvidenceIDs). It is a write-side ingestion bound,
// independent of the report DISPLAY caps below: the report display cap must
// never reverse-limit issue ingestion (C R-C5). The execution write cap itself
// (execution.evidence_max_per_case_run) defaults to 0 = unlimited.
const evidenceLimit = 100

// Report evidence DISPLAY projection defaults (v0.2.4-G SC-G7). They mirror the
// settings catalog defaults of report.evidence_max_per_case_run /
// report.evidence_max_total and are the fail-safe when the settings center is
// unreadable (a bounded default, never an unbounded projection). 0 = unlimited.
const (
	defaultEvidencePerRunCap = 100
	defaultEvidenceTotalCap  = 2000
)

// evidenceProjection bounds the report evidence DISPLAY projection across the
// runs of one snapshot (per-run AND aggregate dual cap, SC-G7). perRun/total
// <= 0 mean unlimited. used counts displayed rows; truncated records that any
// display was reduced so the snapshot can state it explicitly (art.7.6 — never
// silent). The fingerprint digest is NOT bounded by this: it consumes the full
// evidence sha carrier.
type evidenceProjection struct {
	perRun    int
	total     int
	used      int
	truncated bool
}

// take returns how many of n evidence rows may be displayed under the remaining
// budget, recording truncation when fewer than n fit. A nil projection is
// unlimited (the app-facing projections do not display-cap).
func (p *evidenceProjection) take(n int) int {
	if p == nil {
		return n
	}
	allowed := n
	if p.perRun > 0 && allowed > p.perRun {
		allowed = p.perRun
	}
	if p.total > 0 && p.used+allowed > p.total {
		allowed = p.total - p.used
	}
	if allowed < 0 {
		allowed = 0
	}
	if allowed < n {
		p.truncated = true
	}
	p.used += allowed
	return allowed
}

// runAssembly carries the cross-run state of one snapshot build: a cache of
// intents already fetched (many runs share one intent's frozen fingerprint).
type runAssembly struct {
	intents map[int64]execution.ExecutionIntent
}

// runEvidence is the per-run evidence input of buildCaseRun: the FULL sha
// carrier (narrow read, feeds the fingerprint digest) and the bounded number of
// DISPLAY rows to read (allocated by buildRuns failure-first/newest-first, or
// the write-side ingestion bound on the app detail path).
type runEvidence struct {
	shas         []string
	displayLimit int
}

// evidenceAllocationPriority orders runs for the aggregate display budget:
// non-passed terminal verdicts (failed/blocked/inconclusive) first — the recent
// failures the owner cares about — then passed/process runs. Within a group the
// newest run (largest ID) wins (the caller sorts by ID descending). This is NOT
// ascending first-come: old passed runs never starve the newest failure's
// viewable evidence (T8 fix round, C3).
func evidenceAllocationPriority(run execution.CaseRun) int {
	switch run.Status {
	case execution.CaseRunFailed, execution.CaseRunBlocked, execution.CaseRunInconclusive:
		return 0
	default:
		return 1
	}
}

// evidenceCaps resolves the report evidence display caps from the settings
// center, falling back to the bounded defaults when unreadable (fail-safe).
func (m *readModel) evidenceCaps(ctx context.Context) *evidenceProjection {
	perRun, total := defaultEvidencePerRunCap, defaultEvidenceTotalCap
	if m.deps.Settings != nil {
		if value, err := m.deps.Settings.Int(ctx, "report.evidence_max_per_case_run"); err == nil && value >= 0 {
			perRun = int(value)
		}
		if value, err := m.deps.Settings.Int(ctx, "report.evidence_max_total"); err == nil && value >= 0 {
			total = int(value)
		}
	}
	return &evidenceProjection{perRun: perRun, total: total}
}

// evidenceCarrier reads the full sha carrier of one run (narrow sha-only read).
// A nil evidence port yields a nil carrier (no evidence reader wired).
func (m *readModel) evidenceCarrier(ctx context.Context, caseRunID int64) ([]string, error) {
	if m.deps.Evidence == nil {
		return nil, nil
	}
	shas, err := m.deps.Evidence.SHA256sByCaseRun(ctx, caseRunID)
	if err != nil {
		return nil, fmt.Errorf("list evidence sha256s of case run %d: %w", caseRunID, err)
	}
	return shas, nil
}

// evidenceContentPath is the human-viewable link to the existing evidence
// content endpoint (execution/interfaces/httpapi/routes.go: GET
// /api/versions/{id}/evidence/{evidenceId}/content). It is a URL path, not a
// storage path — the bytes stay behind the execution API (SC-G2/G7 C3).
func evidenceContentPath(versionID, evidenceID int64) string {
	return "/api/versions/" + strconv.FormatInt(versionID, 10) +
		"/evidence/" + strconv.FormatInt(evidenceID, 10) + "/content"
}

// caseStepSnapshots projects the design steps onto the report's natural-language
// step context (seq + action + target_description + step expectation).
func caseStepSnapshots(steps []designapp.ExecutableStepView) []delivery.CaseStepSnapshot {
	out := make([]delivery.CaseStepSnapshot, 0, len(steps))
	for _, step := range steps {
		out = append(out, delivery.CaseStepSnapshot{
			Seq:               step.Seq,
			Action:            step.Action,
			TargetDescription: step.TargetDescription,
			Expected:          step.Expected,
		})
	}
	return out
}

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
	sutIDs               []int64
	// Natural-language context of the case version (v0.2.4-G SC-G2 C2): what it
	// verifies, its preconditions, the system it names in prose, and its step
	// sequence. Historical (superseded) rows carry only the title.
	description    string
	preconditions  string
	sutDescription string
	steps          []delivery.CaseStepSnapshot
	// stepExpected carries the recorded case step expectations so an issue
	// raised from a step observation (no failed assertion) can still show what
	// the product was required to do.
	stepExpected map[int]string
}

// stepExpectations maps the recorded case step expectations by step seq.
func stepExpectations(steps []designapp.ExecutableStepView) map[int]string {
	expected := make(map[int]string, len(steps))
	for _, step := range steps {
		if step.Seq <= 0 {
			continue
		}
		if value := strings.TrimSpace(step.Expected); value != "" {
			expected[step.Seq] = value
		}
	}
	return expected
}

// assembledRun carries one case_run in both the application and the report
// projection. EvidenceIDs is only populated for the application projection.
type assembledRun struct {
	app   deliveryapp.CaseRunSnapshot
	model delivery.CaseRunSnapshot
	// hasFailedAssertion reports whether the run carries a failed assertion
	// result (the P3 precondition for an issue candidate / failure detail).
	hasFailedAssertion bool
	// hasRequirementDeviation reports whether a step recorded application
	// evidence without a failed assertion: a failed step with an observed
	// product behavior (QA Q1) or a failed/blocked step observing a missing
	// required control/feature (Task 110).
	hasRequirementDeviation bool
	// hasExecutorLimitation reports whether a failed/blocked step carries the
	// structured executor capability limitation marker (v0.2.3 X-02). Such a
	// run is a blocked executor condition, never a SUT issue candidate.
	hasExecutorLimitation bool
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
// are completed through the historical reader, which keeps the FULL natural-
// language context (description/preconditions/steps, R-G8b) — not title/status
// only — so a prior run's card never reverts after the case is edited.
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
			// v0.2.4-B (T9 design contract): designapp.ExecutableCaseView no
			// longer carries a SUT id set — a case names its target in prose —
			// so sutIDs stays empty here and every run resolves its SUT ids from
			// its own case_run_sut_context rows in assembleRun below (the
			// fallback that already existed for a case whose projection carried
			// no ids). Field-name adaptation only; no other delivery change.
			stepExpected:   stepExpectations(view.Steps),
			description:    view.Description,
			preconditions:  view.Preconditions,
			sutDescription: view.SutDescription,
			steps:          caseStepSnapshots(view.Steps),
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
		// R-G8b: a historical (superseded/stale) case version keeps its full NL
		// context — the immutable row carries description/preconditions/steps,
		// so a prior run's card never reverts to title-only after the case is
		// edited.
		index[row.ID] = caseInfo{
			title:          row.Title,
			description:    row.Description,
			preconditions:  row.Preconditions,
			sutDescription: row.SutDescription,
			steps:          caseStepSnapshots(row.Steps),
			stepExpected:   stepExpectations(row.Steps),
		}
	}
	return index, nil
}

// buildCaseRun assembles one case_run projection. detail also reads the step
// results and always reads the failed assertions; the report projection reads
// the summary counters for the step/assertion counts. asm carries the evidence
// display budget and the intent cache shared across the runs of one snapshot.
func (m *readModel) buildCaseRun(ctx context.Context, versionID int64, run execution.CaseRun, info caseInfo, detail bool, asm *runAssembly, ev runEvidence) (assembledRun, error) {
	var result assembledRun
	runSUTIDs := append([]int64(nil), info.sutIDs...)
	if len(runSUTIDs) == 0 {
		contexts, err := m.deps.SUTContexts.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return assembledRun{}, fmt.Errorf("list SUT contexts of case run %d: %w", run.ID, err)
		}
		seen := make(map[int64]bool, len(contexts))
		for _, item := range contexts {
			if item.SUTID <= 0 || seen[item.SUTID] {
				continue
			}
			seen[item.SUTID] = true
			runSUTIDs = append(runSUTIDs, item.SUTID)
		}
		sort.Slice(runSUTIDs, func(i, j int) bool { return runSUTIDs[i] < runSUTIDs[j] })
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
	var deviationStep *execution.StepResult
	for index := range steps {
		switch steps[index].Status {
		case execution.StepFailed, execution.StepBlocked:
			if _, limited := execution.StepExecutorLimitation(steps[index]); limited {
				result.hasExecutorLimitation = true
				continue
			}
		}
		if deviationStep == nil && execution.StepShowsApplicationDeviation(steps[index]) {
			deviationStep = &steps[index]
		}
	}
	result.hasRequirementDeviation = deviationStep != nil
	// Legacy rows may only carry the capability conclusion in
	// summary.terminal_cause (no structured step marker); it is the same
	// authoritative block and must keep the run out of the issue scan.
	if run.Summary.TerminalCause == execution.TerminalCauseExecutorCapability {
		result.hasExecutorLimitation = true
	}

	// Evidence reads (v0.2.4-G T8 fix round, SC-G7): the FULL sha carrier (ev.shas,
	// narrow read) feeds the fingerprint digest; only the bounded DISPLAY rows are
	// read as full metadata. The app detail path reads its own write-side ingestion
	// bound (evidenceLimit), independent of the report display cap (C R-C5).
	evidenceSHAs := ev.shas
	displayRows := make([]execution.Evidence, 0)
	if ev.displayLimit > 0 && m.deps.Evidence != nil {
		rows, err := m.deps.Evidence.ListNewestByCaseRun(ctx, run.ID, ev.displayLimit)
		if err != nil {
			return assembledRun{}, fmt.Errorf("list display evidence of case run %d: %w", run.ID, err)
		}
		displayRows = rows
	}
	appRows := displayRows
	if detail && m.deps.Evidence != nil {
		rows, err := m.deps.Evidence.ListNewestByCaseRun(ctx, run.ID, evidenceLimit)
		if err != nil {
			return assembledRun{}, fmt.Errorf("list ingestion evidence of case run %d: %w", run.ID, err)
		}
		appRows = rows
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
	var sutID *int64
	if firstFailed != nil {
		failedAssertionSeq = firstFailed.AssertionSeq
		for _, assertion := range failedAssertions {
			if assertion.ID > 0 {
				failedAssertionIDs = append(failedAssertionIDs, assertion.ID)
			}
		}
	}
	// Task 112: the missing required control/feature recorded before the first
	// failed assertion is the root cause of the run (FR-106-02 step 1), while the
	// later failed assertion is a consequence. Anchor the issue on it so the
	// title/category/expected/observed describe what is actually missing, even
	// when the first failed assertion is an api assertion on a later step.
	missingFeatureAnchor := execution.MissingFeatureAnchor(steps, failedAssertionStep(firstFailed))
	switch {
	case missingFeatureAnchor != nil:
		category = execution.AssertionKindUI
		severity = execution.IssueSeverity(info.priority)
		failedStepSeq = missingFeatureAnchor.Seq
		failedStepResultID = missingFeatureAnchor.ID
		if expectation, ok := info.stepExpected[missingFeatureAnchor.Seq]; ok {
			failedExpected = expectation
		}
		failedActual = execution.StepObservedSummary(*missingFeatureAnchor)
		if missingFeatureAnchor.TargetSUTID != nil && *missingFeatureAnchor.TargetSUTID > 0 {
			sutID = int64Ptr(*missingFeatureAnchor.TargetSUTID)
		}
	case firstFailed != nil:
		category = failureCategory(firstFailed.Kind)
		severity = execution.IssueSeverity(info.priority)
		failedStepSeq = firstFailed.StepSeq
		failedExpected = firstFailed.Expected
		failedActual = firstFailed.Actual
		failedReason = firstFailed.Reason
		if firstFailed.TargetSUTID != nil && *firstFailed.TargetSUTID > 0 {
			sutID = int64Ptr(*firstFailed.TargetSUTID)
		}
		for _, step := range steps {
			if step.Seq == firstFailed.StepSeq {
				failedStepResultID = step.ID
				break
			}
		}
	case deviationStep != nil:
		// QA Q1: a failed step with an observed product behavior is itself the
		// requirement-deviation evidence, so the issue candidate must carry the
		// step ref, a category/severity and the observed summary even when the
		// case executor never recorded a failed assertion. Task 110 extends the
		// same projection to a blocked step observing a missing required
		// control/feature (FR-106-02) and carries the recorded case step
		// expectation next to the observation.
		category = execution.AssertionKindUI
		severity = execution.IssueSeverity(info.priority)
		failedStepSeq = deviationStep.Seq
		failedStepResultID = deviationStep.ID
		if expectation, ok := info.stepExpected[deviationStep.Seq]; ok {
			failedExpected = expectation
		}
		failedActual = execution.StepObservedSummary(*deviationStep)
		if deviationStep.TargetSUTID != nil && *deviationStep.TargetSUTID > 0 {
			sutID = int64Ptr(*deviationStep.TargetSUTID)
		}
	}
	if sutID == nil && len(runSUTIDs) > 0 {
		sutID = int64Ptr(runSUTIDs[0])
	}
	// Task 112: the issue summary is narrative evidence, never a second severity
	// source. Drop a contradictory "severity upgraded to major" claim; if the
	// whole summary was such a claim, fall back to the recorded observation.
	failureSummary := execution.SanitizeFailureSummary(run.Summary.LastError)
	if strings.TrimSpace(failureSummary) == "" && deviationStep != nil {
		failureSummary = execution.SanitizeFailureSummary(execution.StepObservedSummary(*deviationStep))
	}

	// Build the display projection (bounded rows, viewable links) and the
	// app-facing ingestion projection (write-side bound). The sha carrier is the
	// full set read above — never derived from the capped display list.
	modelEvidence := make([]delivery.EvidenceSnapshot, 0, len(displayRows))
	for _, row := range displayRows {
		if row.ID <= 0 {
			continue
		}
		if row.CaseRunID <= 0 {
			row.CaseRunID = run.ID
		}
		modelEvidence = append(modelEvidence, delivery.EvidenceSnapshot{
			ID: row.ID, CaseRunID: row.CaseRunID, SUTID: row.SUTID,
			StepSeq: row.StepSeq, AssertionSeq: row.AssertionSeq,
			Kind: string(row.Kind), SHA256: row.SHA256, SizeBytes: row.SizeBytes,
			ContentPath: evidenceContentPath(versionID, row.ID),
		})
	}
	appCount := len(appRows)
	if appCount > evidenceLimit {
		appCount = evidenceLimit
	}
	evidenceIDs := make([]int64, 0, appCount)
	appEvidence := make([]deliveryapp.EvidenceSnapshot, 0, appCount)
	for _, row := range appRows[:appCount] {
		if row.ID <= 0 {
			continue
		}
		if row.CaseRunID <= 0 {
			row.CaseRunID = run.ID
		}
		evidenceIDs = append(evidenceIDs, row.ID)
		appEvidence = append(appEvidence, deliveryapp.EvidenceSnapshot{
			ID: row.ID, CaseRunID: row.CaseRunID, SUTID: row.SUTID,
			StepSeq: row.StepSeq, AssertionSeq: row.AssertionSeq,
			Kind: string(row.Kind), SHA256: row.SHA256, SizeBytes: row.SizeBytes,
		})
	}

	// Per-run ACTUAL environment fingerprint: the frozen fingerprint of the
	// run's intent, read back as stored (R-G1, art.4.6) — never recomputed and
	// never the wave carrier's "planned environment". Cached across runs.
	var environmentFingerprint, codeBaseline string
	if run.IntentID > 0 && m.deps.Intents != nil {
		intent, cached := asm.intents[run.IntentID]
		if !cached {
			fetched, err := m.deps.Intents.GetByID(ctx, run.IntentID)
			if err != nil {
				return assembledRun{}, fmt.Errorf("read intent %d of case run %d: %w", run.IntentID, run.ID, err)
			}
			intent = fetched
			asm.intents[run.IntentID] = intent
		}
		environmentFingerprint = intent.EnvironmentFingerprint
		codeBaseline = intent.CodeBaseline
	}

	result.app = deliveryapp.CaseRunSnapshot{
		ID: run.ID, VersionID: versionID, WaveID: run.WaveID, CaseVersionID: run.CaseVersionID,
		RequirementID: info.requirementID, CaseTitle: info.title,
		SUTID: sutID, SUTIDs: runSUTIDs,
		Status: string(run.Status), FailureClass: string(run.FailureClass),
		ExecutorCapability: result.hasExecutorLimitation,
		TerminalCause:      run.Summary.TerminalCause,
		Summary:            failureSummary,
		StepCount:          stepCount,
		StepFailedCount:    stepFailedCount,
		AssertionCount:     assertionCount, AssertionFailedCount: assertionFailedCount,
		FailedStepSeq: failedStepSeq, FailedAssertionSeq: failedAssertionSeq,
		FailedStepResultID: failedStepResultID, FailedAssertionResultIDs: failedAssertionIDs,
		FailedExpected: failedExpected, FailedActual: failedActual, FailedReason: failedReason,
		Category: category, Severity: severity, EvidenceIDs: evidenceIDs,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt,
	}
	result.model = delivery.CaseRunSnapshot{
		ID: run.ID, VersionID: versionID, WaveID: run.WaveID, CaseVersionID: run.CaseVersionID,
		RequirementID: info.requirementID,
		CaseTitle:     info.title, SUTID: sutID, SUTIDs: runSUTIDs,
		Status: string(run.Status), FailureClass: string(run.FailureClass),
		TerminalCause:   run.Summary.TerminalCause,
		Summary:         failureSummary,
		StepCount:       stepCount,
		StepFailedCount: stepFailedCount,
		AssertionCount:  assertionCount, AssertionFailedCount: assertionFailedCount,
		FailedStepSeq: failedStepSeq, FailedAssertionSeq: failedAssertionSeq,
		Category: category, Severity: severity, Evidence: modelEvidence,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt,
		// v0.2.4-G T7 carrier population + T8 natural-language context.
		EnvironmentFingerprint: environmentFingerprint,
		CodeBaseline:           codeBaseline,
		EvidenceDroppedCount:   int(run.EvidenceDropped),
		EvidenceSHA256s:        evidenceSHAs,
		CaseDescription:        info.description,
		CasePreconditions:      info.preconditions,
		SUTDescription:         info.sutDescription,
		Steps:                  info.steps,
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
	asm := &runAssembly{intents: make(map[int64]execution.ExecutionIntent)}
	for _, run := range runs {
		if run.Status != execution.CaseRunFailed || run.FailureClass != execution.FailureClassApplication {
			continue
		}
		// The detail path reads its own write-side evidence bound (buildCaseRun
		// detail=true) and never feeds the report fingerprint, so the FULL sha
		// carrier is not read here (H-G4 T8: it feeds only detail=false).
		assembled, err := r.model.buildCaseRun(ctx, versionID, run, index[run.CaseVersionID], true, asm, runEvidence{})
		if err != nil {
			return nil, err
		}
		// An executor capability limitation is a blocked executor condition, not
		// a SUT defect: never a failed(application) issue candidate (X-02).
		if assembled.hasExecutorLimitation {
			continue
		}
		// P3 emits an issue candidate only for a failed(application) run with
		// failed assertion evidence, or (QA Q1) with a failed step whose
		// observed behavior contradicts the requirement; a claim without
		// evidence stays inconclusive.
		if !assembled.hasFailedAssertion && !assembled.hasRequirementDeviation {
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
	// Detail path: no sha carrier read (it feeds only the report fingerprint,
	// detail=false — H-G4 T8).
	assembled, err := r.model.buildCaseRun(ctx, wave.VersionID, run, index[run.CaseVersionID], true,
		&runAssembly{intents: make(map[int64]execution.ExecutionIntent)}, runEvidence{})
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

// waveModel assembles the report-style model of one wave (header, runs, issues,
// version SUTs and the version coverage projection). It is shared by the app and
// report projections. Requirements/cases are version-level, so a wave report
// carries the same coverage projection as the version report instead of the
// zero value (QA Q3: coverage 0/0/0/0 with "无覆盖缺口" while gaps existed).
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
	budget := m.evidenceCaps(ctx)
	// Card/representative evidence has an INDEPENDENT budget so a version that
	// exhausts the run-level aggregate cap still shows viewable evidence on its
	// defect cards (T8 fix round, C3).
	cardBudget := m.evidenceCaps(ctx)
	modelRuns, appRuns, err := m.buildRuns(ctx, versionID, runs, index, false, budget)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	waveReport := waveReportSnapshot(wave)
	view, err := m.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, mapVersionError(err)
	}
	suts := m.sutSnapshots(view)
	issueSnapshots, runTriage, err := m.issueSnapshots(ctx, versionID, &waveID, runIDSet(runs), appRuns, cardBudget)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	applyRunTriage(modelRuns, runTriage)
	requirementViews, err := m.requirementViews(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	details, err := m.requirementDetails(ctx, versionID, requirementViews)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	versionRuns, err := m.caseRunsOfVersion(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	// A wave report has no requirement rows, so the settled projection is only
	// consumed by the version model; requirements/case execution are version
	// level facts shared by both snapshots.
	coverage, _, err := m.coverage(ctx, versionID, requirementViews, versionRuns, flakyRunSet(runTriage), details)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	judgments, err := m.judgmentSnapshots(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	m.applyWaveEnvironment(wave, &waveReport, modelRuns)
	// issueRuns is derived from the UNTRUNCATED issue set (before the 500-cap) so
	// a run whose issue was capped out is never mislabeled "no issue" (T8 fix
	// round, IMPORTANT #3).
	issueRuns := issueRunSet(issueSnapshots)
	snapshot := delivery.CapReportSnapshot(delivery.ReportSnapshot{
		Kind:              delivery.ReportKindWave,
		VersionID:         versionID,
		WaveID:            int64Ptr(waveID),
		Waves:             []delivery.WaveSnapshot{waveReport},
		CaseRuns:          modelRuns,
		Issues:            issueSnapshots,
		SUTs:              suts,
		Coverage:          coverage,
		Judgments:         judgments,
		TruncatedSections: evidenceTruncationSections(budget, cardBudget),
	})
	return finalizeReportProjection(snapshot, issueRuns), nil
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
	budget := m.evidenceCaps(ctx)
	// Independent card/representative evidence budget (T8 fix round, C3).
	cardBudget := m.evidenceCaps(ctx)
	modelRuns, appRuns, err := m.buildRuns(ctx, versionID, runs, index, false, budget)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	requirements, requirementViews, details, err := m.requirementSnapshots(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	view, err := m.deps.Versions.Get(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, mapVersionError(err)
	}
	suts := m.sutSnapshots(view)
	issueSnapshots, runTriage, err := m.issueSnapshots(ctx, versionID, nil, nil, appRuns, cardBudget)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	applyRunTriage(modelRuns, runTriage)
	coverage, settledRequirements, err := m.coverage(ctx, versionID, requirementViews, runs, flakyRunSet(runTriage), details)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	for index := range requirements {
		requirements[index].ExecutionSettled = settledRequirements[requirements[index].ID]
	}
	judgments, err := m.judgmentSnapshots(ctx, versionID)
	if err != nil {
		return delivery.ReportSnapshot{}, err
	}
	waveSnapshots := make([]delivery.WaveSnapshot, 0, len(waves))
	for _, wave := range waves {
		waveSnapshot := waveReportSnapshot(wave)
		m.applyWaveEnvironment(wave, &waveSnapshot, modelRuns)
		waveSnapshots = append(waveSnapshots, waveSnapshot)
	}
	// issueRuns from the UNTRUNCATED issue set (before the 500-cap) so a run
	// whose issue was capped out is never mislabeled "no issue" (IMPORTANT #3).
	issueRuns := issueRunSet(issueSnapshots)
	snapshot := delivery.CapReportSnapshot(delivery.ReportSnapshot{
		Kind:         delivery.ReportKindVersion,
		VersionID:    versionID,
		Requirements: requirements,
		Waves:        waveSnapshots,
		CaseRuns:     modelRuns,
		Issues:       issueSnapshots,
		SUTs:         suts,
		Coverage:     coverage,
		Judgments:    judgments,
		// Version-level closure classification (v0.2.4-G T7 carrier, R-G8):
		// how the version closed and whether it is an admin waiver. Wave
		// diagnostics carry no version closure (waveModel leaves these unset).
		ClosureKind:       view.ClosureKind,
		Unverified:        view.Unverified,
		TruncatedSections: evidenceTruncationSections(budget, cardBudget),
	})
	if err := m.applyWaiverFact(ctx, &snapshot, view.CloseReason); err != nil {
		return delivery.ReportSnapshot{}, err
	}
	return finalizeReportProjection(snapshot, issueRuns), nil
}

// applyWaiverFact projects the admin waiver reason/operator/time onto the report
// snapshot (v0.2.4-G SC-G11, T10 + final fix wave #1). Two independent legs:
//
//   - VERSION leg: gated on the closure_kind COLUMN (never on fact presence): a
//     reopened version is closure_kind=normal and shows no version waiver even
//     though its append-only historical facts survive (五.9). The waiver REASON is
//     projected from the durable version.CloseReason column (already on the
//     VersionView read for closure_kind), NOT only from the best-effort
//     version_fact — waiver_service tolerates a RecordFact failure, so a lost fact
//     must not erase the reason that still exists. The fact supplies the
//     operator/time (and a reason fallback only when the durable column is empty).
//
//   - REQUIREMENT leg: gated on each requirement's CURRENT Waived flag (五.9), NOT
//     on the version closure_kind. A requirement waived then normally closed
//     (closure_kind=normal) still overrode closure completeness, so the report
//     discloses its waiver reason/operator/time from the kind=waiver,
//     key=requirement:<id> fact INDEPENDENT of the version closure classification.
//
// The reason/operator are display/audit metadata, not canonical fingerprint
// members (the waiver decisions closure_kind/unverified and requirement.Waived
// already are). An unwired port leaves operator/time empty (the template discloses
// 豁免上下文不可用/豁免理由不可用, never a silent "—"); a wired port that errors fails
// closed (art.7.6). When several waiver facts coexist for one key (a repeated waive
// appends its own waived_at-discriminated row), the most recent is the current
// accountability context.
func (m *readModel) applyWaiverFact(ctx context.Context, snapshot *delivery.ReportSnapshot, durableReason string) error {
	versionWaived := snapshot.ClosureKind == version.ClosureKindWaived
	anyRequirementWaived := false
	for _, requirement := range snapshot.Requirements {
		if requirement.Waived {
			anyRequirementWaived = true
			break
		}
	}
	if versionWaived {
		// The durable column is the authoritative reason; it survives a lost fact.
		snapshot.WaiverReason = durableReason
	}
	if !versionWaived && !anyRequirementWaived {
		return nil
	}
	if m.deps.WaiverFacts == nil {
		return nil
	}
	facts, err := m.deps.WaiverFacts.Current(ctx, snapshot.VersionID, version.FactKindWaiver)
	if err != nil {
		return fmt.Errorf("read waiver facts of version %d: %w", snapshot.VersionID, err)
	}
	// Index the most recent fact per key (version / requirement:<id>). A malformed
	// waived_at on ANY fact is a data-integrity error and fails closed (art.7.6).
	latestByKey := make(map[string]*version.Fact)
	latestAtByKey := make(map[string]time.Time)
	for index := range facts {
		fact := &facts[index]
		at, err := parseWaivedAt(fact)
		if err != nil {
			return err
		}
		if _, ok := latestByKey[fact.Key]; !ok || at.After(latestAtByKey[fact.Key]) {
			latestByKey[fact.Key] = fact
			latestAtByKey[fact.Key] = at
		}
	}
	if versionWaived {
		if latest := latestByKey["version"]; latest != nil {
			if snapshot.WaiverReason == "" {
				snapshot.WaiverReason = factString(latest.Payload["reason"])
			}
			snapshot.WaiverOperator = factString(latest.Payload["actor_id"])
			snapshot.WaivedAt = latestAtByKey["version"]
		}
	}
	for index := range snapshot.Requirements {
		requirement := &snapshot.Requirements[index]
		if !requirement.Waived {
			continue
		}
		key := version.RequirementFactKeyPrefix + strconv.FormatInt(requirement.ID, 10)
		latest := latestByKey[key]
		if latest == nil {
			continue
		}
		requirement.WaiverReason = factString(latest.Payload["reason"])
		requirement.WaiverOperator = factString(latest.Payload["actor_id"])
		requirement.WaivedAt = latestAtByKey[key]
	}
	return nil
}

// parseWaivedAt parses a waiver fact's waived_at timestamp. An empty value is a
// legacy fact predating the waived_at discriminator (T6 fix round) and yields the
// zero time (the template discloses 未记录); a NON-empty malformed value is a
// data-integrity error and fails closed rather than silently becoming zero
// (art.7.6 — no ignored-error pattern).
func parseWaivedAt(fact *version.Fact) (time.Time, error) {
	raw := factString(fact.Payload["waived_at"])
	if raw == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("waiver fact %d has malformed waived_at %q: %w", fact.ID, raw, err)
	}
	return at, nil
}

// factString renders a waiver fact payload value as a string. The payload stores
// decimal-string IDs and free text (D29); a nil value yields "".
func factString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// issueRunSet indexes every case_run referenced by the (untruncated) issue set.
func issueRunSet(issues []delivery.IssueSnapshot) map[int64]bool {
	runs := make(map[int64]bool)
	for _, issue := range issues {
		for _, id := range issue.CaseRunIDs {
			runs[id] = true
		}
	}
	return runs
}

// evidenceTruncationSections returns the explicit evidence display truncation
// marker when either the run-level or the card-level dual cap reduced any
// display (art.7.6 — never silent).
func evidenceTruncationSections(budget, cardBudget *evidenceProjection) []string {
	if (budget != nil && budget.truncated) || (cardBudget != nil && cardBudget.truncated) {
		return []string{delivery.ReportTruncationEvidence}
	}
	return nil
}

// finalizeReportProjection derives the display-only projections that must stay
// consistent with the CAPPED run list: the non-issue failure explanations
// (SC-G2 C1) and the aggregate evidence total (SC-G7). issueRuns comes from the
// UNTRUNCATED issue set so the 500-issue cap never mislabels a run as "no issue".
// Both projections are derived from fingerprinted facts, so they never enter the
// canonical payload.
func finalizeReportProjection(snapshot delivery.ReportSnapshot, issueRuns map[int64]bool) delivery.ReportSnapshot {
	explanations := make([]delivery.FailureExplanationSnapshot, 0)
	total := 0
	for _, run := range snapshot.CaseRuns {
		total += run.EvidenceTotalCount()
		if !run.NeedsFailureExplanation(issueRuns[run.ID]) {
			continue
		}
		effective := run.EffectiveFailureClass()
		explanations = append(explanations, delivery.FailureExplanationSnapshot{
			CaseRunID:             run.ID,
			CaseVersionID:         run.CaseVersionID,
			RequirementID:         run.RequirementID,
			CaseTitle:             run.CaseTitle,
			Status:                run.Status,
			FailureClass:          run.FailureClass,
			EffectiveFailureClass: effective,
			TerminalCause:         run.TerminalCause,
			Explanation:           delivery.FailureExplanationText(run.Status, effective, run.TerminalCause),
		})
	}
	snapshot.FailureExplanations = explanations
	snapshot.EvidenceTotalCount = total
	return snapshot
}

// buildRuns assembles every run. budget bounds the evidence DISPLAY projection
// across the runs (nil = unlimited); the intent cache is shared so runs of one
// intent fetch its frozen fingerprint once. The full sha carrier of every run is
// read first (narrow sha-only read), then the aggregate display budget is
// allocated FAILURE-FIRST / NEWEST-FIRST (not ascending first-come), so the
// newest failed run's viewable evidence is never starved by old passed runs
// (T8 fix round, C3).
func (m *readModel) buildRuns(ctx context.Context, versionID int64, runs []execution.CaseRun, index map[int64]caseInfo, detail bool, budget *evidenceProjection) ([]delivery.CaseRunSnapshot, []deliveryapp.CaseRunSnapshot, error) {
	asm := &runAssembly{intents: make(map[int64]execution.ExecutionIntent)}
	// Pass 1: the full sha carrier of every run (also yields the true row counts
	// for budget allocation).
	carriers := make([][]string, len(runs))
	if m.deps.Evidence != nil {
		for i, run := range runs {
			shas, err := m.deps.Evidence.SHA256sByCaseRun(ctx, run.ID)
			if err != nil {
				return nil, nil, fmt.Errorf("list evidence sha256s of case run %d: %w", run.ID, err)
			}
			carriers[i] = shas
		}
	}
	// Allocate display allowances in priority order: failures first, newest
	// first within each group. The display list itself stays newest-first per run.
	allowances := make([]int, len(runs))
	if !detail {
		order := make([]int, len(runs))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			left, right := runs[order[a]], runs[order[b]]
			if pa, pb := evidenceAllocationPriority(left), evidenceAllocationPriority(right); pa != pb {
				return pa < pb
			}
			return left.ID > right.ID
		})
		for _, i := range order {
			allowances[i] = budget.take(len(carriers[i]))
		}
	}
	// Pass 2: assemble in ascending run order (deterministic snapshot order).
	modelRuns := make([]delivery.CaseRunSnapshot, 0, len(runs))
	appRuns := make([]deliveryapp.CaseRunSnapshot, 0, len(runs))
	for i, run := range runs {
		ev := runEvidence{shas: carriers[i], displayLimit: allowances[i]}
		assembled, err := m.buildCaseRun(ctx, versionID, run, index[run.CaseVersionID], detail, asm, ev)
		if err != nil {
			return nil, nil, err
		}
		modelRuns = append(modelRuns, assembled.model)
		appRuns = append(appRuns, assembled.app)
	}
	return modelRuns, appRuns, nil
}

// requirementViews drains the version requirement pages ordered by ID. Both the
// version report snapshots and the coverage projection share it.
func (m *readModel) requirementViews(ctx context.Context, versionID int64) ([]versionapp.RequirementView, error) {
	if m.deps.Requirements == nil {
		return nil, platform.NewError(500, "internal", "delivery adapter has no requirement reader")
	}
	views := make([]versionapp.RequirementView, 0)
	var before *int64
	for {
		page, hasMore, err := m.deps.Requirements.List(ctx, versionID, "", pageSize, before)
		if err != nil {
			return nil, fmt.Errorf("list requirements: %w", err)
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
	return views, nil
}

// requirementDetails reads the current-revision detail of every requirement once
// (user story / acceptance criteria / sut_description). It is shared by the
// requirement snapshots (NL context + AC hash) and the AC verification matrix
// (AC full set + g/w/t) so the detail is read a single time per snapshot build.
// A detail read failure fails the build closed — the report never silently drops
// the requirement context (art.7.6).
func (m *readModel) requirementDetails(ctx context.Context, versionID int64, views []versionapp.RequirementView) (map[int64]versionapp.RequirementDetail, error) {
	if m.deps.Requirements == nil {
		return nil, platform.NewError(500, "internal", "delivery adapter has no requirement reader")
	}
	details := make(map[int64]versionapp.RequirementDetail, len(views))
	for _, view := range views {
		detail, err := m.deps.Requirements.Get(ctx, versionID, view.ID)
		if err != nil {
			return nil, fmt.Errorf("read requirement %d detail: %w", view.ID, err)
		}
		details[view.ID] = detail
	}
	return details, nil
}

// requirementSnapshots joins the newest closure round of every requirement. It
// also returns the requirement views and the current-revision details (shared
// with the AC verification matrix) for coverage.
func (m *readModel) requirementSnapshots(ctx context.Context, versionID int64) ([]delivery.RequirementSnapshot, []versionapp.RequirementView, map[int64]versionapp.RequirementDetail, error) {
	views, err := m.requirementViews(ctx, versionID)
	if err != nil {
		return nil, nil, nil, err
	}
	details, err := m.requirementDetails(ctx, versionID, views)
	if err != nil {
		return nil, nil, nil, err
	}
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
			Waived:           view.Waived,
		}
		// Natural-language context + AC hash from the current revision
		// (v0.2.4-G SC-G2 C2 / SC-G5).
		detail := details[view.ID]
		snapshot.UserStory = delivery.UserStorySnapshot{
			AsA:    detail.CurrentVersion.UserStory.AsA,
			IWant:  detail.CurrentVersion.UserStory.IWant,
			SoThat: detail.CurrentVersion.UserStory.SoThat,
		}
		snapshot.SutDescription = detail.CurrentVersion.SutDescription
		// ACHash is computed unconditionally: an empty AC set hashes to a stable
		// non-empty digest, distinguishable from "not populated" (T8 fix round
		// #11). The version domain AC normalization is the single source of the
		// AC hash (no handwritten second normalization, art.5.1).
		criteria := make([]version.AcceptanceCriterion, 0, len(detail.CurrentVersion.AcceptanceCriteria))
		for _, criterion := range detail.CurrentVersion.AcceptanceCriteria {
			criteria = append(criteria, version.AcceptanceCriterion{
				ID:    criterion.ID,
				Given: criterion.Given,
				When:  criterion.When,
				Then:  criterion.Then,
			})
		}
		acHash, err := version.HashAcceptanceCriteria(criteria)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("hash requirement %d acceptance criteria: %w", view.ID, err)
		}
		snapshot.ACHash = acHash
		if m.deps.Closures != nil {
			closure, found, err := m.deps.Closures.Latest(ctx, view.ID)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("load closure of requirement %d: %w", view.ID, err)
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
	return snapshots, views, details, nil
}

// coverageLatestRuns projects the newest non-superseded case_run per case
// version. A superseded run is a cancelled/replaced process row, never an
// execution result: treating it as the latest result erases the coverage of an
// earlier passed run (Task 116 / QA release REL-Q2). A case version whose only
// runs are superseded has no entry here and stays a "not run" gap.
func coverageLatestRuns(runs []execution.CaseRun) map[int64]execution.CaseRun {
	latest := make(map[int64]execution.CaseRun, len(runs))
	for _, run := range runs {
		if run.Status == execution.CaseRunSuperseded {
			continue
		}
		if existing, ok := latest[run.CaseVersionID]; ok && existing.ID >= run.ID {
			continue
		}
		latest[run.CaseVersionID] = run
	}
	return latest
}

// requirementExecutionSettled reports, per requirement ID, whether the
// requirement's execution is settled: the requirement has at least one
// executable case and every executable case version's latest non-superseded run
// is terminal (passed/failed/blocked/inconclusive/skipped). A requirement
// without executable cases is never settled, and a pending/running/interrupted/
// paused run or a missing run leaves the requirement unsettled, so an unexecuted
// requirement can never satisfy the report due gate (R2-NEW-02).
func requirementExecutionSettled(cases []designapp.ExecutableCaseView, latest map[int64]execution.CaseRun) map[int64]bool {
	hasCase := make(map[int64]bool, len(cases))
	unsettled := make(map[int64]bool, len(cases))
	for _, item := range cases {
		hasCase[item.RequirementID] = true
		run, ok := latest[item.CaseVersionID]
		if !ok || !run.Status.Terminal() {
			unsettled[item.RequirementID] = true
		}
	}
	settled := make(map[int64]bool, len(hasCase))
	for requirementID := range hasCase {
		settled[requirementID] = !unsettled[requirementID]
	}
	return settled
}

// coverage computes the P3 execution coverage projection: every current+
// confirmed case whose latest non-superseded run did not pass is a gap. The
// projection is derived from the version's own case_run snapshot, not from a
// separate newest-row query, so a cancelled wave that merely supersedes runs
// cannot overwrite a result the report already counted. A failed run whose
// issue triage is flaky is a flaky gap (never a pass, but not a product
// coverage failure). It also returns the derived per-requirement execution
// settled projection the report due gate consumes.
func (m *readModel) coverage(ctx context.Context, versionID int64, requirements []versionapp.RequirementView, runs []execution.CaseRun, flakyRuns map[int64]bool, details map[int64]versionapp.RequirementDetail) (delivery.CoverageSnapshot, map[int64]bool, error) {
	cases, err := m.deps.Design.ListExecutable(ctx, versionID)
	if err != nil {
		return delivery.CoverageSnapshot{}, nil, fmt.Errorf("list executable cases: %w", err)
	}
	latest := coverageLatestRuns(runs)
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
		switch {
		case ok && flakyRuns[run.ID]:
			reason = delivery.CoverageGapExecutionFlaky
		case ok:
			reason = "execution_failed_coverage"
		}
		coverage.Gaps = append(coverage.Gaps, delivery.CoverageGapSnapshot{
			RequirementID: item.RequirementID,
			CaseVersionID: item.CaseVersionID,
			Reason:        reason,
		})
	}
	coverage.ByRequirement = requirementCoverageRows(requirements, cases, latest, flakyRuns)
	coverage.ACVerification = m.acVerificationMatrix(requirements, cases, latest, details)
	return coverage, requirementExecutionSettled(cases, latest), nil
}

// acVerificationMatrix builds the AC-level verification coverage projection
// (v0.2.4-G SC-G4 I1): per requirement, each acceptance criterion's state
// (verified/unverified/failed/uncovered) overlays the covering confirmed cases'
// latest terminal verdict onto the covered_acs linkage. Link coverage is NOT
// acceptance verification. The covered_acs ids are normalized through the design
// domain union (single source of truth); the AC full set + g/w/t come from the
// requirement's current revision. The matrix is display metadata (the AC content,
// linkage and verdict are all fingerprinted elsewhere), capped explicitly (M4).
func (m *readModel) acVerificationMatrix(requirements []versionapp.RequirementView, cases []designapp.ExecutableCaseView, latest map[int64]execution.CaseRun, details map[int64]versionapp.RequirementDetail) delivery.ACCoverageSnapshot {
	// Group the covering cases by requirement, normalizing covered_acs once.
	casesByRequirement := make(map[int64][]deliveryapp.ACCaseCoverage)
	for _, item := range cases {
		verdict := ""
		if run, ok := latest[item.CaseVersionID]; ok {
			verdict = string(run.Status)
		}
		casesByRequirement[item.RequirementID] = append(casesByRequirement[item.RequirementID], deliveryapp.ACCaseCoverage{
			CaseVersionID: item.CaseVersionID,
			CoveredACs:    design.NormalizeACIDSet(item.CoveredACs),
			LatestVerdict: verdict,
		})
	}
	byRequirement := make([]delivery.RequirementACCoverageSnapshot, 0, len(requirements))
	for _, requirement := range requirements {
		detail := details[requirement.ID]
		criteria := make([]delivery.ACCriterionSnapshot, 0, len(detail.CurrentVersion.AcceptanceCriteria))
		for _, criterion := range detail.CurrentVersion.AcceptanceCriteria {
			criteria = append(criteria, delivery.ACCriterionSnapshot{
				ID:    criterion.ID,
				Given: criterion.Given,
				When:  criterion.When,
				Then:  criterion.Then,
			})
		}
		matrix := deliveryapp.BuildACVerificationMatrix(requirement.ID, criteria, casesByRequirement[requirement.ID])
		byRequirement = append(byRequirement, delivery.RequirementACCoverageSnapshot{
			RequirementID: requirement.ID,
			ACs:           matrix,
		})
	}
	return deliveryapp.SummarizeACCoverage(byRequirement)
}

// judgmentSnapshots consumes the design judge read report (v0.2.4-G SC-G4 I3):
// one projection per current case carrying the derived review_state, the latest
// verdict bound to the current case version (empty when unjudged/stale) and its
// findings. An unwired port yields no annotation; a wired port that errors fails
// the build closed (no silent degradation, art.7.6). The report presents these at
// the case/AC coverage area so a human sees the judge assessment next to coverage.
func (m *readModel) judgmentSnapshots(ctx context.Context, versionID int64) ([]delivery.JudgmentSnapshot, error) {
	if m.deps.Judgments == nil {
		return nil, nil
	}
	report, err := m.deps.Judgments.ListJudgments(ctx, versionID, nil)
	if err != nil {
		return nil, fmt.Errorf("read design judgments of version %d: %w", versionID, err)
	}
	out := make([]delivery.JudgmentSnapshot, 0, len(report.Cases))
	for _, entry := range report.Cases {
		snapshot := delivery.JudgmentSnapshot{
			CaseID:        entry.CaseID,
			CaseVersionID: entry.CurrentVersionID,
			ReviewState:   string(entry.ReviewState),
		}
		if entry.Judgment != nil {
			snapshot.RequirementID = entry.Judgment.RequirementID
			snapshot.Verdict = entry.Judgment.Verdict
			for _, finding := range entry.Judgment.Findings {
				snapshot.Findings = append(snapshot.Findings, delivery.JudgmentFindingSnapshot{
					Dimension: finding.Dimension,
					Severity:  finding.Severity,
					Message:   finding.Message,
					CaseRef:   finding.CaseRef,
				})
			}
			snapshot.ACCoverageMatrix, snapshot.ACCoverageMatrixDroppedCount = judgeACCoverageMatrix(entry.Judgment.ACCoverageMatrix)
		}
		out = append(out, snapshot)
	}
	return out, nil
}

// judgeACCoverageMatrix projects the judge's candidate ac_coverage_matrix
// (map[AC id] -> []case id, stored as map[string]any of []any) onto a typed
// map[string][]string for presentation, and COUNTS the malformed entries it
// discards (a non-array AC value, or a non-string/empty case id) so the discard
// is disclosed, never silent (art.7.6). The judge candidate is display content,
// never the verification truth. A nil/empty matrix yields nil and zero dropped.
func judgeACCoverageMatrix(raw map[string]any) (map[string][]string, int) {
	if len(raw) == 0 {
		return nil, 0
	}
	out := make(map[string][]string, len(raw))
	dropped := 0
	for acID, value := range raw {
		items, ok := value.([]any)
		if !ok {
			// A non-array AC value is malformed; disclose the discard.
			dropped++
			continue
		}
		ids := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok && text != "" {
				ids = append(ids, text)
			} else {
				dropped++
			}
		}
		out[acID] = ids
	}
	return out, dropped
}

// applyWaveEnvironment consumes execution.WaveFrozenFingerprint for one wave
// (v0.2.4-G SC-G6 P15): it rebuilds the wave's frozen ("planned") environment
// fingerprint/code baseline from the scope snapshot, projects the planned per-SUT
// environment items, and discloses any member run whose ACTUAL intent fingerprint
// diverges from the carrier with a readable difference (R-G1/M1). A legacy wave
// (no carrier) fails closed at the fingerprint level and is annotated
// "环境不可核验" per-wave — it never blocks the whole version report (I5). An
// unwired port leaves the fields unset.
func (m *readModel) applyWaveEnvironment(wave execution.RunWave, snapshot *delivery.WaveSnapshot, runs []delivery.CaseRunSnapshot) {
	if m.deps.WaveFingerprints == nil {
		return
	}
	fingerprint, baseline, err := m.deps.WaveFingerprints.WaveFrozenFingerprint(wave)
	if err != nil {
		snapshot.EnvironmentVerifiable = false
		snapshot.EnvironmentUnverifiableReason = waveEnvironmentUnverifiableReason(err)
		return
	}
	snapshot.EnvironmentVerifiable = true
	snapshot.EnvironmentFingerprint = fingerprint
	snapshot.CodeBaseline = baseline
	// Project the planned environment items from the frozen scope document.
	if payload, parseErr := execution.ParseWaveScopeSnapshot(wave.ScopeSnapshot); parseErr == nil {
		for _, sut := range payload.SUTs {
			planned := delivery.WaveSUTEnvironmentSnapshot{
				SUTID:        sut.SUTID,
				BaseURL:      sut.BaseURL,
				PageIDs:      sut.PageIDs,
				APIIDs:       sut.APIIDs,
				DatabaseIDs:  sut.DatabaseIDs,
				AccountRoles: sut.AccountRoles,
			}
			for _, repo := range sut.RepositoryBaselines {
				planned.RepositoryBaselines = append(planned.RepositoryBaselines, delivery.WaveRepoBaselineSnapshot{
					RepoID:    repo.RepoID,
					CommitSHA: repo.CommitSHA,
				})
			}
			snapshot.PlannedEnvironment = append(snapshot.PlannedEnvironment, planned)
		}
	}
	// Carrier-vs-per-run divergence: a member run whose actual intent fingerprint
	// or code baseline differs from the wave carrier. A run with no actual
	// fingerprint (legacy IntentID 0) has no basis to compare and is skipped.
	for _, run := range runs {
		if run.WaveID != wave.ID || run.EnvironmentFingerprint == "" {
			continue
		}
		if run.EnvironmentFingerprint == fingerprint && run.CodeBaseline == baseline {
			continue
		}
		snapshot.EnvironmentDivergences = append(snapshot.EnvironmentDivergences, delivery.WaveEnvironmentDivergenceSnapshot{
			CaseRunID:           run.ID,
			CaseVersionID:       run.CaseVersionID,
			PlannedFingerprint:  fingerprint,
			ActualFingerprint:   run.EnvironmentFingerprint,
			PlannedCodeBaseline: baseline,
			ActualCodeBaseline:  run.CodeBaseline,
			Differences:         delivery.DescribeEnvironmentDivergence(fingerprint, run.EnvironmentFingerprint, baseline, run.CodeBaseline, snapshot.PlannedEnvironment, run.SUTIDs),
		})
	}
}

// waveEnvironmentUnverifiableReason classifies the WaveFrozenFingerprint failure
// into the truthful human-facing reason (v0.2.4-G fix round, art.7.6). The three
// failure classes are distinct facts and must not be conflated: a carrier-vs-
// frozen-input MISMATCH is an integrity (tamper/corruption) signal — post-GD-05
// new waves always carry a carrier, so this is the production error a human most
// needs to see; a genuine no-carrier wave is legacy; a malformed snapshot is a
// parse failure. Fail-closed is preserved either way (verifiable=false, no
// live-read, no version block) — only the reason differs.
func waveEnvironmentUnverifiableReason(err error) string {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case executionapp.WaveFrozenFingerprintMismatchCode, executionapp.WaveFrozenBaselineMismatchCode:
			return "环境不可核验：冻结载体与冻结输入不一致（wave 环境指纹/代码基线载体与冻结快照输入交叉校验失败），疑似篡改或损坏，请复核执行环境完整性。"
		case executionapp.WaveFrozenNoCarrierCode:
			return "环境不可核验：该 wave 无冻结环境指纹载体（legacy wave），无法从冻结快照核验执行环境。"
		case executionapp.WaveFrozenSnapshotMalformedCode:
			return "环境不可核验：冻结快照无法解析（scope snapshot 非可解码 JSON），无法核验执行环境。"
		}
	}
	return "环境不可核验：无法从冻结快照重建环境指纹（重建校验失败），无法核验执行环境。"
}

// requirementCoverageRows derives the per-requirement coverage projection from
// the same executable case versions and latest runs as the aggregate coverage.
// Rows are emitted for every version requirement (including requirements with no
// executable case version) in ascending requirement ID order, so the report
// writer and the narrative guard see the same deterministic facts.
func requirementCoverageRows(requirements []versionapp.RequirementView, cases []designapp.ExecutableCaseView, latest map[int64]execution.CaseRun, flakyRuns map[int64]bool) []delivery.RequirementCoverageSnapshot {
	byID := make(map[int64]*delivery.RequirementCoverageSnapshot, len(requirements)+1)
	row := func(requirementID int64) *delivery.RequirementCoverageSnapshot {
		if existing, ok := byID[requirementID]; ok {
			return existing
		}
		created := &delivery.RequirementCoverageSnapshot{RequirementID: requirementID}
		byID[requirementID] = created
		return created
	}
	for _, requirement := range requirements {
		row(requirement.ID)
	}
	for _, item := range cases {
		entry := row(item.RequirementID)
		entry.CaseVersionsTotal++
		run, ok := latest[item.CaseVersionID]
		switch {
		case ok && run.Status == execution.CaseRunPassed:
			entry.CaseVersionsCovered++
		case ok && flakyRuns[run.ID]:
			entry.CaseVersionsFlakyCoverage++
		case ok:
			entry.CaseVersionsFailedCoverage++
		default:
			entry.CaseVersionsNotRun++
		}
	}
	rows := make([]delivery.RequirementCoverageSnapshot, 0, len(byID))
	for _, entry := range byID {
		rows = append(rows, *entry)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RequirementID < rows[j].RequirementID })
	return rows
}

// sutSnapshots maps the SUTs of the version's bound environment onto report
// labels. v0.2.4-B: the binding row is gone, so the SUT id is the key (ID==SUTID).
// The view is fetched once by the caller (the version model also reads its
// closure classification from it). SUTDescription carries the resource catalog
// introduction so a human knows which system a row concerns (SC-G2 C2).
func (m *readModel) sutSnapshots(view versionapp.VersionView) []delivery.SUTSnapshot {
	suts := view.Binding.Environment.SUTs
	out := make([]delivery.SUTSnapshot, 0, len(suts))
	for _, sut := range suts {
		sutID, _ := strconv.ParseInt(sut.ID, 10, 64)
		if sutID <= 0 {
			continue
		}
		out = append(out, delivery.SUTSnapshot{
			ID:              sutID,
			SUTID:           sutID,
			SUTName:         sut.Name,
			EnvironmentID:   view.EnvironmentID,
			EnvironmentName: view.Binding.Environment.Name,
			SUTDescription:  sut.Intro,
		})
	}
	return out
}

// issueSnapshots drains the version issues and joins refs, the latest finished
// issue.triage projection and the reverse merged_into projection. When
// waveID/runIDs are set only issues referencing that wave or one of its runs
// are returned. It also returns the case_run IDs whose triage is flaky or
// expected_failure: those runs are not product defects for the whole version
// projection (a wave report still uses the version-level coverage gaps; only
// flaky is excluded from the product coverage-failure count).
func (m *readModel) issueSnapshots(ctx context.Context, versionID int64, waveID *int64, runIDs map[int64]bool, appRuns []deliveryapp.CaseRunSnapshot, budget *evidenceProjection) ([]delivery.IssueSnapshot, map[int64]string, error) {
	if m.deps.Issues == nil {
		return nil, nil, nil
	}
	// runIndex resolves the issue's linked runs to their failure detail so the
	// five-element card takes a DEFINED representative run (SC-G2 I4).
	runIndex := make(map[int64]deliveryapp.CaseRunSnapshot, len(appRuns))
	for _, run := range appRuns {
		runIndex[run.ID] = run
	}
	issues := make([]delivery.Issue, 0)
	var before int64
	for {
		page, err := m.deps.Issues.List(ctx, versionID, deliveryapp.IssueFilter{Before: before, Limit: pageSize})
		if err != nil {
			return nil, nil, fmt.Errorf("list issues: %w", err)
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
	runTriage := make(map[int64]string)
	type triageProjection struct {
		status         string
		classification string
	}
	triageByIssue := make(map[int64]triageProjection, len(issues))
	for _, issue := range issues {
		refs, err := m.deps.Issues.ListRefs(ctx, issue.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("list issue refs %d: %w", issue.ID, err)
		}
		refsByIssue[issue.ID] = refs
		if issue.MergedIntoID != nil && *issue.MergedIntoID > 0 {
			mergedSources[*issue.MergedIntoID] = append(mergedSources[*issue.MergedIntoID], issue.ID)
		}
		if m.deps.Triage == nil {
			continue
		}
		run, found, err := m.deps.Triage.LatestByIssue(ctx, issue.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("load issue %d triage: %w", issue.ID, err)
		}
		if !found {
			continue
		}
		projection := triageProjection{
			status:         string(run.Status),
			classification: delivery.TriageOutputClassification(run),
		}
		triageByIssue[issue.ID] = projection
		if projection.status != string(delivery.AnalysisStatusDone) {
			continue
		}
		classification := projection.classification
		if classification != delivery.TriageClassificationFlaky &&
			classification != delivery.TriageClassificationExpectedFailure {
			continue
		}
		for _, ref := range refs {
			if ref.Kind != delivery.IssueRefKindCaseRun || ref.RefID <= 0 {
				continue
			}
			// A run referenced by more than one triaged issue (or by a flaky and
			// an expected_failure issue) resolves deterministically: flaky
			// (automation) wins because it also excludes the run from the
			// product coverage-failure count; otherwise the first classification
			// in issue-ID order is kept.
			if existing, ok := runTriage[ref.RefID]; ok {
				if existing != delivery.TriageClassificationFlaky &&
					classification == delivery.TriageClassificationFlaky {
					runTriage[ref.RefID] = classification
				}
				continue
			}
			runTriage[ref.RefID] = classification
		}
	}
	snapshots := make([]delivery.IssueSnapshot, 0, len(issues))
	// cardEvidence holds each card's FULL representative evidence reverse-lookup,
	// aligned with snapshots by index. The aggregate card display budget is
	// allocated AFTER the loop in newest/failure-first order (final fix wave #6),
	// not ascending-issue first-come, so the newest defect card is never starved.
	cardEvidence := make([][]delivery.EvidenceSnapshot, 0, len(issues))
	cardEvidenceFull := make([]int, 0, len(issues))
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
		triage := triageByIssue[issue.ID]
		snapshot := delivery.IssueSnapshot{
			ID:                   issue.ID,
			VersionID:            issue.VersionID,
			Title:                issue.Title,
			Status:               issue.Status,
			Category:             issue.Category,
			Severity:             issue.Severity,
			Source:               issue.Source,
			MergedIntoID:         issue.MergedIntoID,
			SourceIssueIDs:       mergedSources[issue.ID],
			CaseRunIDs:           caseRunIDs,
			TriageStatus:         triage.status,
			TriageClassification: triage.classification,
			CreatedAt:            issue.CreatedAt,
			UpdatedAt:            issue.UpdatedAt,
			DedupeKey:            issue.DedupeKey,
			// Creation-time structured detail (SC-G3); overridden below by the
			// representative run when one is present in the snapshot.
			FailedExpected: issue.FailedExpected,
			FailedActual:   issue.FailedActual,
			FailedReason:   issue.FailedReason,
		}
		// Representative run (SC-G2 I4): the newest failed linked run present in
		// the snapshot, else the newest linked run. The five-element detail comes
		// from THIS run only — never mixed across runs.
		present := make([]int64, 0, len(caseRunIDs))
		failed := make(map[int64]bool, len(caseRunIDs))
		for _, id := range caseRunIDs {
			if run, ok := runIndex[id]; ok {
				present = append(present, id)
				if run.Status == string(execution.CaseRunFailed) {
					failed[id] = true
				}
			}
		}
		var evidence []delivery.EvidenceSnapshot
		if representativeID := delivery.RepresentativeRunID(present, failed); representativeID > 0 {
			representative := runIndex[representativeID]
			snapshot.RepresentativeCaseRunID = representativeID
			snapshot.FailedExpected = representative.FailedExpected
			snapshot.FailedActual = representative.FailedActual
			snapshot.FailedReason = representative.FailedReason
			rows, err := m.representativeEvidence(ctx, versionID, representative)
			if err != nil {
				return nil, nil, err
			}
			evidence = rows
		}
		snapshots = append(snapshots, snapshot)
		// Retain only the per-run cap's worth of rows: the allocation below never
		// grants a card more than perRun rows, so holding every card's FULL
		// reverse-lookup set until allocation only raises peak memory (H-G4
		// fix-wave #39). The full count is kept for the truncation disclosure.
		fullCount := len(evidence)
		if budget != nil && budget.perRun > 0 && len(evidence) > budget.perRun {
			evidence = evidence[:budget.perRun]
		}
		cardEvidence = append(cardEvidence, evidence)
		cardEvidenceFull = append(cardEvidenceFull, fullCount)
	}
	allocateCardEvidence(snapshots, cardEvidence, cardEvidenceFull, runIndex, budget)
	return snapshots, runTriage, nil
}

// allocateCardEvidence distributes the aggregate card evidence display budget
// across the defect cards NEWEST/FAILURE-FIRST (final fix wave #6, mirroring the
// run-level evidenceAllocationPriority): a card whose representative run is a
// non-passed terminal verdict (failed/blocked/inconclusive) is fed before a
// passed/process one, and within a group the newest representative run (largest
// ID) wins, tie-broken by newest issue ID. Ascending-issue first-come would feed
// the oldest cards and starve the newest defect the owner cares about. A card
// whose evidence is reduced below its full reverse-lookup count is marked
// RepresentativeEvidenceTruncated so the template discloses 证据超出显示上限（已截断）
// instead of falsely claiming 无代表 run 证据 (art.7.6). A nil budget is unlimited.
func allocateCardEvidence(snapshots []delivery.IssueSnapshot, cardEvidence [][]delivery.EvidenceSnapshot, cardEvidenceFull []int, runIndex map[int64]deliveryapp.CaseRunSnapshot, budget *evidenceProjection) {
	order := make([]int, len(snapshots))
	for i := range order {
		order[i] = i
	}
	priority := func(index int) int {
		run, ok := runIndex[snapshots[index].RepresentativeCaseRunID]
		if !ok {
			return 1
		}
		switch run.Status {
		case string(execution.CaseRunFailed), string(execution.CaseRunBlocked), string(execution.CaseRunInconclusive):
			return 0
		default:
			return 1
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		left, right := order[a], order[b]
		if pa, pb := priority(left), priority(right); pa != pb {
			return pa < pb
		}
		if snapshots[left].RepresentativeCaseRunID != snapshots[right].RepresentativeCaseRunID {
			return snapshots[left].RepresentativeCaseRunID > snapshots[right].RepresentativeCaseRunID
		}
		return snapshots[left].ID > snapshots[right].ID
	})
	for _, index := range order {
		rows := cardEvidence[index]
		// The budget is charged against the FULL reverse-lookup count, so the
		// truncation disclosure reflects what was actually bound, not the already
		// trimmed retention (H-G4 fix-wave #39).
		fullCount := cardEvidenceFull[index]
		allowed := budget.take(fullCount)
		if allowed < fullCount {
			snapshots[index].RepresentativeEvidenceTruncated = true
		}
		if allowed < len(rows) {
			rows = rows[:allowed]
		}
		snapshots[index].RepresentativeEvidence = rows
	}
}

// representativeEvidence reverse-looks-up the evidence bound to the
// representative run's failed assertion results and failed step through the
// evidence_ref read side (v0.2.4-G SC-G9, T4 EvidenceRefReader), deduplicated by
// evidence ID. Each row carries a viewable content link. The run-level evidence
// projection reads the evidence table directly (the complete authoritative set);
// this card-level enrichment adds the assertion/step binding the evidence_ref
// ledger records. It returns the FULL reverse-lookup set; the aggregate card
// display budget is allocated across issues NEWEST/FAILURE-FIRST by the caller
// (issueSnapshots, final fix wave #6) — not ascending-issue first-come here, which
// would starve the newest defect card the owner cares about.
func (m *readModel) representativeEvidence(ctx context.Context, versionID int64, run deliveryapp.CaseRunSnapshot) ([]delivery.EvidenceSnapshot, error) {
	if m.deps.EvidenceRefs == nil {
		return nil, nil
	}
	seen := make(map[int64]bool)
	rows := make([]delivery.EvidenceSnapshot, 0)
	collect := func(ownerKind string, ownerID int64) error {
		if ownerID <= 0 {
			return nil
		}
		refs, err := m.deps.EvidenceRefs.ListByOwner(ctx, ownerKind, ownerID)
		if err != nil {
			return fmt.Errorf("reverse-lookup %s %d evidence: %w", ownerKind, ownerID, err)
		}
		for _, ref := range refs {
			evidence := ref.Evidence
			if evidence.ID <= 0 || seen[evidence.ID] {
				continue
			}
			seen[evidence.ID] = true
			rows = append(rows, delivery.EvidenceSnapshot{
				ID: evidence.ID, CaseRunID: evidence.CaseRunID, SUTID: evidence.SUTID,
				StepSeq: evidence.StepSeq, AssertionSeq: evidence.AssertionSeq,
				Kind: string(evidence.Kind), SHA256: evidence.SHA256, SizeBytes: evidence.SizeBytes,
				ContentPath: evidenceContentPath(versionID, evidence.ID),
			})
		}
		return nil
	}
	for _, assertionID := range run.FailedAssertionResultIDs {
		if err := collect(execution.EvidenceOwnerAssertionResult, assertionID); err != nil {
			return nil, err
		}
	}
	// The report projection does not read step results (detail=false), so the
	// failed step result ID is resolved here from the run's steps by the failed
	// step seq — only for the representative run of an issue (a bounded read).
	stepResultID := run.FailedStepResultID
	if stepResultID == 0 && run.FailedStepSeq > 0 && m.deps.Results != nil {
		steps, err := m.deps.Results.ListSteps(ctx, run.ID)
		if err != nil {
			return nil, fmt.Errorf("list steps of representative run %d: %w", run.ID, err)
		}
		for _, step := range steps {
			if step.Seq == run.FailedStepSeq {
				stepResultID = step.ID
				break
			}
		}
	}
	if err := collect(execution.EvidenceOwnerStepResult, stepResultID); err != nil {
		return nil, err
	}
	return rows, nil
}

// flakyRunSet selects the runs whose triage classification is flaky. Only those
// are excluded from the product coverage-failure count; expected_failure stays a
// genuine non-pass (no fake pass, Task 121/122).
func flakyRunSet(runTriage map[int64]string) map[int64]bool {
	if len(runTriage) == 0 {
		return nil
	}
	flaky := make(map[int64]bool)
	for runID, classification := range runTriage {
		if classification == delivery.TriageClassificationFlaky {
			flaky[runID] = true
		}
	}
	return flaky
}

// applyRunTriage marks the report projection of every run referenced by a
// flaky- or expected_failure-triaged issue. The raw FailureClass stays
// untouched; readers use CaseRunSnapshot.EffectiveFailureClass for the
// report-visible conclusion.
func applyRunTriage(runs []delivery.CaseRunSnapshot, runTriage map[int64]string) {
	for index := range runs {
		if classification, ok := runTriage[runs[index].ID]; ok {
			runs[index].TriageClassification = classification
		}
	}
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
	bySUT := make([]delivery.SUTCountsSnapshot, 0, len(wave.Counts.BySUT))
	for rawID, counts := range wave.Counts.BySUT {
		sutID := int64(0)
		if _, err := fmt.Sscanf(rawID, "%d", &sutID); err != nil || sutID <= 0 {
			continue
		}
		bySUT = append(bySUT, delivery.SUTCountsSnapshot{
			SUTID:        sutID,
			Total:        counts.Total,
			Passed:       counts.Passed,
			Failed:       counts.Failed,
			Blocked:      counts.Blocked,
			Inconclusive: counts.Inconclusive,
		})
	}
	sort.Slice(bySUT, func(i, j int) bool { return bySUT[i].SUTID < bySUT[j].SUTID })
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
		BySUT:      bySUT,
		ReportID:   wave.ReportID,
		StartedAt:  wave.StartedAt,
		FinishedAt: wave.FinishedAt,
	}
}

// failureCategory mirrors the P3-D-12 deterministic category derivation.
func failureCategory(assertionKind string) string {
	switch assertionKind {
	case execution.AssertionKindDB:
		return execution.AssertionKindDB
	case execution.AssertionKindAPI:
		return execution.AssertionKindAPI
	default:
		return execution.AssertionKindUI
	}
}

// failedAssertionStep returns the step sequence of a failed assertion, or 0 when
// the run has no failed assertion.
func failedAssertionStep(assertion *execution.AssertionResult) int {
	if assertion == nil {
		return 0
	}
	return assertion.StepSeq
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
			"total":                               source.Counts.Total,
			string(execution.CaseRunPending):      source.Counts.Pending,
			string(execution.CaseRunRunning):      source.Counts.Running,
			string(execution.CaseRunPaused):       source.Counts.Paused,
			string(execution.CaseRunPassed):       source.Counts.Passed,
			string(execution.CaseRunFailed):       source.Counts.Failed,
			string(execution.CaseRunBlocked):      source.Counts.Blocked,
			string(execution.CaseRunInconclusive): source.Counts.Inconclusive,
			string(execution.CaseRunSkipped):      source.Counts.Skipped,
			string(execution.CaseRunInterrupted):  source.Counts.Interrupted,
			string(execution.CaseRunSuperseded):   source.Counts.Superseded,
			"evidence":                            source.Counts.Evidence,
		}
		bySUT := make(map[int64]deliveryapp.SUTCounts, len(source.BySUT))
		for _, sut := range source.BySUT {
			bySUT[sut.SUTID] = deliveryapp.SUTCounts{
				Total: sut.Total, Passed: sut.Passed, Failed: sut.Failed,
				Blocked: sut.Blocked, Inconclusive: sut.Inconclusive,
			}
		}
		wave.ID = source.ID
		wave.Status = source.Status
		wave.Trigger = source.Trigger
		wave.Counts = counts
		wave.BySUT = bySUT
		wave.ReportID = source.ReportID
		wave.StartedAt = source.StartedAt
		wave.FinishedAt = source.FinishedAt
	}
	for _, run := range model.CaseRuns {
		appRun := deliveryapp.CaseRunSnapshot{
			ID: run.ID, VersionID: run.VersionID, WaveID: run.WaveID, CaseVersionID: run.CaseVersionID,
			CaseTitle: run.CaseTitle, SUTID: run.SUTID, SUTIDs: run.SUTIDs,
			Status: run.Status, FailureClass: run.FailureClass, Summary: run.Summary,
			ExecutorCapability: run.ExecutorCapabilityBlocked(), TerminalCause: run.TerminalCause,
			StepCount: run.StepCount, StepFailedCount: run.StepFailedCount,
			AssertionCount: run.AssertionCount, AssertionFailedCount: run.AssertionFailedCount,
			FailedStepSeq: run.FailedStepSeq, FailedAssertionSeq: run.FailedAssertionSeq,
			Category: run.Category, Severity: run.Severity,
			StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, CreatedAt: run.CreatedAt,
		}
		for _, evidence := range run.Evidence {
			appRun.EvidenceIDs = append(appRun.EvidenceIDs, evidence.ID)
			wave.Evidence = append(wave.Evidence, deliveryapp.EvidenceSnapshot{
				ID: evidence.ID, CaseRunID: evidence.CaseRunID, SUTID: evidence.SUTID,
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
			ExecutionSettled: requirement.ExecutionSettled,
		})
	}
	for _, sut := range model.SUTs {
		out.SUTs = append(out.SUTs, deliveryapp.SUTSnapshot{
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
			CaseTitle: run.CaseTitle, SUTID: run.SUTID, SUTIDs: run.SUTIDs,
			Status: run.Status, FailureClass: run.FailureClass, Summary: run.Summary,
			ExecutorCapability: run.ExecutorCapabilityBlocked(), TerminalCause: run.TerminalCause,
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
