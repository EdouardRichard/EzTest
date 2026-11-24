package delivery

import (
	"context"
	"fmt"
	"sort"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Report input size caps (spec §6.1). A report never includes more than these
// rows of each list; the capped lists keep the first N by ascending ID and the
// snapshot records the truncation so the renderer can state it visibly.
const (
	MaxReportIssues   = 500
	MaxReportWaves    = 200
	MaxReportCaseRuns = 2000
)

// Report-visible triage/coverage classifications (Task 118, Task 122). The
// platform issue triage is authoritative for the failure conclusion: a
// failed(application) case_run whose issue triage is done with
// classification=flaky is automation flakiness, and one classified
// expected_failure (for example a run that stopped on a real token budget
// before the step under observation) is an expected failure that must be rerun
// rather than a product defect. The raw P3 failure_class stays available on
// the snapshot; the report-visible value is derived through
// CaseRunSnapshot.EffectiveFailureClass.
const (
	// TriageClassificationFlaky is the issue.triage classification that marks a
	// failure as flaky/automation instead of a product defect.
	TriageClassificationFlaky = "flaky"
	// TriageClassificationExpectedFailure is the issue.triage classification
	// that marks a failure as an expected/not-judged failure (budget or missing
	// observation) instead of a product defect.
	TriageClassificationExpectedFailure = "expected_failure"
	// FailureClassAutomation is the report-visible failure class of a run whose
	// issue triage is flaky.
	FailureClassAutomation = "automation"
	// FailureClassExpectedFailure is the report-visible failure class of a run
	// whose issue triage is expected_failure. It is deliberately not
	// "application" and not "automation": the run is neither a product defect
	// nor flaky automation noise; it needs a rerun to produce a conclusion.
	FailureClassExpectedFailure = "expected_failure"
	// FailureClassBlocked is the report-visible failure class of a run blocked
	// by an executor capability limitation (v0.2.3 X-02). It stays blocked: the
	// executor could not perform the action, so it is never a SUT defect.
	FailureClassBlocked = "blocked"
	// TerminalCauseExecutorCapability is the shared stable cause of a run blocked
	// by an executor capability limitation. Execution, issue candidates and the
	// report/triage vocabulary use the same code (no DB enum/migration); the
	// execution domain owns the constant.
	TerminalCauseExecutorCapability = execution.TerminalCauseExecutorCapability
	// CoverageGapExecutionFlaky explains an uncovered case version whose latest
	// run failed but the platform triage classified that failure as flaky. It
	// is a flaky gap, not a product coverage failure.
	CoverageGapExecutionFlaky = "execution_flaky_coverage"
)

// Stable section names of ReportSnapshot.TruncatedSections.
const (
	ReportTruncationIssues   = "issues"
	ReportTruncationWaves    = "waves"
	ReportTruncationCaseRuns = "case_runs"
	// ReportTruncationEvidence marks the evidence DISPLAY projection reduced by
	// the per-run/aggregate caps (v0.2.4-G SC-G7). The fingerprint evidence
	// digest is never truncated (it consumes the full sha carrier); this marker
	// only describes the bounded display list.
	ReportTruncationEvidence = "evidence"
)

// ReportSnapshot is the deterministic report input of spec §6.1. It is built
// from application ports before rendering and is the single source of the
// report input fingerprint. Field order is part of the canonical contract and
// must not be rearranged. GeneratedAt is the generation as-of time and does not
// participate in the fingerprint (spec §6.1).
type ReportSnapshot struct {
	Kind         ReportKind
	VersionID    int64
	WaveID       *int64
	Requirements []RequirementSnapshot
	Waves        []WaveSnapshot
	CaseRuns     []CaseRunSnapshot
	Issues       []IssueSnapshot
	SUTs         []SUTSnapshot
	Coverage     CoverageSnapshot
	GeneratedAt  time.Time
	// ReportID/ReportRevision/InputFingerprint are the P7 render metadata of
	// the revision being rendered. They feed the template v2 appendix only and
	// are deliberately not part of the canonical fingerprint payload
	// (report_fingerprint.go builds its own fixed struct), so they can never
	// change an input fingerprint.
	ReportID         int64
	ReportRevision   int32
	InputFingerprint string
	// TruncatedSections lists the input lists reduced to their §6.1 cap, in a
	// stable order (issues, waves, case_runs, evidence). It is derived metadata,
	// not part of the fingerprint payload; the renderer prints it as a visible
	// truncation note. A ReportDataReader that already capped a list at the
	// boundary sets the matching entry because the service cannot infer
	// overflow from an exactly-capped list.
	TruncatedSections []string
	// ClosureKind/Unverified are the version-level closure classification
	// (v0.2.4-G P22, R-G8): how the version closed (the version domain
	// ClosureKinds list is the authority: normal|waived) and whether that
	// closure is an admin waiver carrying unverified requirements. A waiver
	// changes the report conclusion, so both ARE conclusion inputs and
	// participate in the canonical fingerprint. The report read model
	// populates them from the version row; an unset ClosureKind is hashed
	// verbatim (wave diagnostics carry no version closure).
	ClosureKind string
	Unverified  bool
	// FailureExplanations projects WHY every terminal non-passed run that has no
	// defect issue did not pass (v0.2.4-G SC-G2 C1): blocked/inconclusive/
	// environment/automation/expected_failure runs never create an issue, so a
	// human asking "why did case X fail" is answered here instead of by a card.
	// It is derived display metadata (every input — status, failure class,
	// terminal cause, triage — is already a fingerprint conclusion input), not
	// part of the canonical payload.
	FailureExplanations []FailureExplanationSnapshot
	// EvidenceTotalCount is the aggregate number of evidence rows across the
	// snapshot's runs BEFORE the display caps (v0.2.4-G SC-G7). It is derived
	// display metadata (the per-run digest already fingerprints every count);
	// the renderer states it next to the explicit truncation note.
	EvidenceTotalCount int
	// Judgments presents the design judge facts (verdict/findings/review_state)
	// of the version's current cases at the case/AC coverage area (v0.2.4-G
	// SC-G4 I3), consumed through the cross-context judge read port. Derived
	// display metadata (review_state is an instant projection, art.V.11), not a
	// canonical payload member.
	Judgments []JudgmentSnapshot
	// WaiverReason/WaiverOperator/WaivedAt project the admin waive-close fact
	// (version_fact kind=waiver, key=version) so the waived report lists WHY the
	// version was waived and WHO waived it (v0.2.4-G SC-G11, T10 render). They are
	// display/audit metadata derived from the append-only waiver fact: the waiver
	// DECISION (ClosureKind/Unverified) is already a canonical fingerprint member,
	// while the reason/operator are the human-readable accountability context, not
	// payload members. WaiverOperator is the operator's decimal account ID (the
	// fact stores actor_id; name resolution is an identity read concern). Empty
	// when the version is not waived or no waiver fact is present; the renderer
	// gates the waived section on ClosureKind, never on fact presence (a reopened
	// version shows no waiver even though its historical facts survive).
	WaiverReason   string
	WaiverOperator string
	WaivedAt       time.Time
}

// CapReportSnapshot enforces the spec §6.1 report input caps: at most 500
// issues, 200 waves and 2000 case_runs, keeping the first N by ascending ID.
// The input is not mutated. TruncatedSections is merged with any truncation
// markers a ReportDataReader already reported, so a pre-capped list at the
// boundary still renders its visible note.
func CapReportSnapshot(snapshot ReportSnapshot) ReportSnapshot {
	sections := append([]string(nil), snapshot.TruncatedSections...)
	var cappedIssues, cappedWaves, cappedCaseRuns bool
	snapshot.Issues, cappedIssues = capReportRows(snapshot.Issues, MaxReportIssues, func(row IssueSnapshot) int64 { return row.ID })
	snapshot.Waves, cappedWaves = capReportRows(snapshot.Waves, MaxReportWaves, func(row WaveSnapshot) int64 { return row.ID })
	snapshot.CaseRuns, cappedCaseRuns = capReportRows(snapshot.CaseRuns, MaxReportCaseRuns, func(row CaseRunSnapshot) int64 { return row.ID })
	if cappedIssues {
		sections = append(sections, ReportTruncationIssues)
	}
	if cappedWaves {
		sections = append(sections, ReportTruncationWaves)
	}
	if cappedCaseRuns {
		sections = append(sections, ReportTruncationCaseRuns)
	}
	snapshot.TruncatedSections = canonicalTruncationSections(sections)
	return snapshot
}

// capReportRows returns rows unchanged when in budget and otherwise a copy
// sorted by ascending ID truncated to limit. Slicing a sorted copy keeps the
// original snapshot (and therefore reader caches) untouched.
func capReportRows[T any](rows []T, limit int, id func(T) int64) ([]T, bool) {
	if len(rows) <= limit {
		return rows, false
	}
	sorted := make([]T, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool { return id(sorted[i]) < id(sorted[j]) })
	return sorted[:limit], true
}

// canonicalTruncationSections deduplicates markers and orders the known
// sections canonically (issues, waves, case_runs, evidence) so the rendered
// note is deterministic; unknown reader-provided markers follow in input order.
func canonicalTruncationSections(sections []string) []string {
	if len(sections) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(sections))
	result := make([]string, 0, len(sections))
	for _, name := range []string{ReportTruncationIssues, ReportTruncationWaves, ReportTruncationCaseRuns, ReportTruncationEvidence} {
		for _, candidate := range sections {
			if candidate == name && !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
		}
	}
	for _, name := range sections {
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

// SectionOptions controls which report sections are rendered (spec §6.2). A
// false section is omitted from the HTML entirely. Appendix only exists in
// template version 2 (report.section_appendix); template v1 ignores it so its
// rendered bytes stay frozen.
type SectionOptions struct {
	Summary  bool
	Results  bool
	Issues   bool
	Coverage bool
	Evidence bool
	Appendix bool
}

// ReportRenderer renders one report revision from a deterministic snapshot and
// an optional plain-text narrative. The application layer depends on this
// interface; infrastructure/render implements it.
type ReportRenderer interface {
	Render(ctx context.Context, snapshot ReportSnapshot, narrative string, opts SectionOptions) ([]byte, error)
}

// ReportTemplateVersioner is an optional extension of ReportRenderer. A
// renderer that knows the built-in template version it renders reports it here,
// so a new revision records exactly the version its bytes were produced with
// (P7-D-03). A renderer without this method keeps the frozen P4 version "1".
type ReportTemplateVersioner interface {
	TemplateVersion() string
}

// RequirementSnapshot is one version requirement projection with its latest
// closure summary (spec §6.1/§7.2).
type RequirementSnapshot struct {
	ID               int64
	VersionID        int64
	CurrentVersionID *int64
	Title            string
	Status           string
	ClosureID        *int64
	ClosedAt         *time.Time
	CloseRound       int32
	ReopenCount      int
	UpdatedAt        time.Time
	// ExecutionSettled is derived execution metadata: true when the requirement
	// has at least one current+confirmed case version and every such case
	// version's latest non-superseded case_run is terminal (blocked,
	// inconclusive and skipped count as terminal). It is deliberately NOT part of
	// the canonical fingerprint payload (report_fingerprint.go): adding it there
	// would change every stored fingerprint and mass-mark published reports
	// stale. The report due gate uses it so a requirement whose closure predicate
	// can never pass (blocked/inconclusive case, failed case without an issue
	// yet) does not block the version report forever (R2-NEW-02).
	ExecutionSettled bool
	// Waived is the admin requirement-level waiver flag (requirement.Waived,
	// v0.2.4-G SC-G11). It overrides the requirement closure predicate, so it
	// IS a conclusion input and participates in the canonical fingerprint
	// (P22). The report read model populates it from the requirement row.
	Waived bool
	// WaiverReason/WaiverOperator/WaivedAt project the requirement-level admin
	// waiver fact (version_fact kind=waiver, key=requirement:<id>) so the report
	// discloses WHY this requirement's closure completeness was overridden and
	// WHO overrode it (v0.2.4-G SC-G11, final fix wave #1). They are display/audit
	// metadata derived from the append-only waiver fact: the waiver DECISION
	// (Waived) is already a canonical fingerprint member, while the reason/operator
	// are the human-readable accountability context, not payload members. The
	// disclosure renders whenever Waived is true, INDEPENDENT of the version
	// closure_kind (a requirement waived then normally closed still overrode
	// completeness). Empty when no requirement waiver fact is present; the renderer
	// then discloses 豁免理由不可用/操作者未记录, never a silent claim of no waiver.
	WaiverReason   string
	WaiverOperator string
	WaivedAt       time.Time
	// ACHash is the content hash of the requirement's current acceptance
	// criteria set (normalized by the version domain AC rules). Report
	// conclusions quote AC given/when/then, so an AC content change must mark
	// dependent reports stale (art.4.6, P22). The report read model populates
	// it from the current requirement_version.
	ACHash string
	// UserStory is the requirement's three-part natural-language user story
	// (v0.2.4-G SC-G2 C2): who wants what and why, so a human reading a defect
	// card understands the requirement without looking up any ID. It is display
	// context derived from the frozen current revision (a content change mints
	// a new CurrentVersionID, which IS fingerprinted), not a payload member.
	UserStory UserStorySnapshot
	// SutDescription is the requirement's natural-language description of the
	// system under test (v0.2.4-G SC-G2 C2). Display context, frozen with the
	// current revision like UserStory.
	SutDescription string
}

// UserStorySnapshot is the report projection of a requirement's §5.1 user
// story (as_a / i_want / so_that). It mirrors the version read face; the
// authoritative definition stays in the version domain.
type UserStorySnapshot struct {
	AsA    string
	IWant  string
	SoThat string
}

// WaveSnapshot is one wave projection of a report. Counts are fixed fields (no
// maps) so they can be fingerprinted deterministically; BySUT is sorted
// by SUT ID before hashing and rendering.
type WaveSnapshot struct {
	ID         int64
	VersionID  int64
	Status     string
	Trigger    string
	Counts     WaveCountsSnapshot
	BySUT      []SUTCountsSnapshot
	ReportID   *int64
	StartedAt  *time.Time
	FinishedAt *time.Time
	// EnvironmentFingerprint/CodeBaseline are the wave's FROZEN ("planned")
	// environment fingerprint and code baseline, rebuilt from the wave scope
	// snapshot through execution.WaveFrozenFingerprint (v0.2.4-G SC-G6 P15).
	// They are the "planned environment" CONTEXT — the per-run ACTUAL fingerprint
	// (CaseRunSnapshot.EnvironmentFingerprint) is the fingerprint member
	// (art.4.6, R-G1). These wave-level fields are derived display metadata, not
	// canonical payload members (canonicalWave keeps its fixed fields).
	EnvironmentFingerprint string
	CodeBaseline           string
	// EnvironmentVerifiable is false for a legacy wave whose frozen snapshot has
	// no fingerprint carrier: WaveFrozenFingerprint fails closed (typed error, no
	// live-read fallback) and the report annotates the wave "环境不可核验"
	// per-wave rather than blocking the whole version report (SC-G6 I5).
	EnvironmentVerifiable         bool
	EnvironmentUnverifiableReason string
	// PlannedEnvironment projects the wave's frozen per-SUT environment items
	// (base_url/asset sets/account roles/code baseline) for human reading.
	PlannedEnvironment []WaveSUTEnvironmentSnapshot
	// EnvironmentDivergences discloses member runs whose actual intent
	// fingerprint/baseline differs from the wave carrier (SC-G6 R-G1/M1), with a
	// readable difference. Disclosed, never blocks publication.
	EnvironmentDivergences []WaveEnvironmentDivergenceSnapshot
}

// WaveCountsSnapshot is the fixed projection of run_wave.counts (spec §6.1).
type WaveCountsSnapshot struct {
	Total        int
	Pending      int
	Running      int
	Paused       int
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
	Skipped      int
	Interrupted  int
	Superseded   int
	Evidence     int
}

// SUTCountsSnapshot is one SUT's section of a wave's by_sut
// counts (spec §6.1).
type SUTCountsSnapshot struct {
	SUTID        int64
	Total        int
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
}

// CaseRunSnapshot is one case_run projection of a report with its failure
// position, step/assertion counters and evidence metadata (spec §6.1).
type CaseRunSnapshot struct {
	ID            int64
	VersionID     int64
	WaveID        int64
	CaseVersionID int64
	// RequirementID is the design requirement the case version belongs to. It
	// connects a case result with the per-requirement coverage projection and
	// is derived metadata: it is not part of the canonical fingerprint payload.
	RequirementID int64
	CaseTitle     string
	SUTID         *int64
	SUTIDs        []int64
	Status        string
	FailureClass  string
	// TerminalCause is the execution-persisted stable cause of the run
	// (case_run.summary.terminal_cause). The report/triage vocabulary shares it
	// with execution: executor_capability marks a run blocked by an executor
	// capability limitation, never a SUT defect (v0.2.3 X-02).
	TerminalCause string
	// TriageClassification is the latest finished platform triage
	// classification of the issue that references this run (empty when none).
	// A flaky classification overrides the report-visible classification; the
	// raw FailureClass remains the execution fact.
	TriageClassification string
	Summary              string
	StepCount            int
	StepFailedCount      int
	AssertionCount       int
	AssertionFailedCount int
	FailedStepSeq        int
	FailedAssertionSeq   int
	Category             string
	Severity             string
	Evidence             []EvidenceSnapshot
	StartedAt            *time.Time
	FinishedAt           *time.Time
	CreatedAt            time.Time
	// EnvironmentFingerprint/CodeBaseline are the ACTUAL environment this run
	// executed under: the frozen fingerprint of the run's intent
	// (case_run.IntentID -> intent.environment_fingerprint/code_baseline, read
	// via IntentReader.GetByID). They are per-run facts, deliberately not the
	// wave carrier ("planned environment") fingerprint (R-G1, art.4.6): a
	// report must never claim fingerprint B while the run executed under
	// fingerprint A. The report read model populates them.
	EnvironmentFingerprint string
	CodeBaseline           string
	// EvidenceDroppedCount is the persisted count of evidence items this run
	// lost (execution CaseRunSummary.EvidenceDropped). Drops degrade the
	// conclusion, so the count is a conclusion input of the per-run evidence
	// digest (R-G2, art.4.5).
	EvidenceDroppedCount int
	// CaseDescription/CasePreconditions/SUTDescription are the case version's
	// natural-language context (v0.2.4-G SC-G2 C2): what the case verifies, what
	// must hold before it runs, and which system it names in prose. Steps carries
	// the design step sequence (action + target_description). Together they let a
	// human read "which system/page/step is doing what" without any ID. They are
	// display context derived from the frozen case version (a content change
	// mints a new CaseVersionID, which IS fingerprinted), not payload members.
	CaseDescription   string
	CasePreconditions string
	SUTDescription    string
	Steps             []CaseStepSnapshot
	// EvidenceSHA256s is the FULL evidence sha256 carrier of the run (every
	// evidence row, multiplicity preserved, read order) — the fingerprint digest
	// input (v0.2.4-G SC-G7 R-G2). It is deliberately separate from Evidence,
	// which is the bounded DISPLAY projection: the digest must stay causally
	// complete over ALL evidence even when the display is capped (art.4.6). A nil
	// carrier means the snapshot was built without one (synthetic/unit inputs);
	// the digest then derives from Evidence. The production read model always
	// populates it (non-nil even for a run without evidence).
	EvidenceSHA256s []string
}

// CaseStepSnapshot is one design case step's natural-language projection for
// the defect card (v0.2.4-G SC-G2 C2): the step sequence, what it does, what it
// acts on (prose target) and the step-level expectation.
type CaseStepSnapshot struct {
	Seq               int
	Action            string
	TargetDescription string
	Expected          string
}

// EvidenceTotalCount is the run's full evidence row count before the display
// cap: the carrier length when present, otherwise the display list length.
func (row CaseRunSnapshot) EvidenceTotalCount() int {
	if row.EvidenceSHA256s != nil {
		return len(row.EvidenceSHA256s)
	}
	return len(row.Evidence)
}

// EvidenceTruncated reports whether the run's evidence DISPLAY list was reduced
// below its full count (explicit truncation, never silent — art.7.6).
func (row CaseRunSnapshot) EvidenceTruncated() bool {
	return row.EvidenceTotalCount() > len(row.Evidence)
}

// evidenceFingerprintSHAs returns the full evidence sha multiset the fingerprint
// digest consumes: the authoritative carrier when present, otherwise derived
// from the evidence list (synthetic snapshots without a carrier).
func (row CaseRunSnapshot) evidenceFingerprintSHAs() []string {
	if row.EvidenceSHA256s != nil {
		return row.EvidenceSHA256s
	}
	shas := make([]string, 0, len(row.Evidence))
	for _, evidence := range row.Evidence {
		shas = append(shas, evidence.SHA256)
	}
	return shas
}

// EffectiveFailureClass is the failure class a report presents. It is the raw
// execution class unless the platform triage finished with classification=flaky
// (automation flakiness) or classification=expected_failure (expected failure,
// budget/no-judgment, needs a rerun). Neither triage classification is a
// product/application defect (Task 118, Task 122).
func (row CaseRunSnapshot) EffectiveFailureClass() string {
	if row.ExecutorCapabilityBlocked() {
		// An executor capability block is not a product defect and no triage
		// classification may turn it into one (X-02).
		return FailureClassBlocked
	}
	switch row.TriageClassification {
	case TriageClassificationFlaky:
		return FailureClassAutomation
	case TriageClassificationExpectedFailure:
		return FailureClassExpectedFailure
	}
	return row.FailureClass
}

// ExecutorCapabilityBlocked reports whether the run was blocked by an executor
// capability limitation rather than a SUT defect. Execution and the issue
// candidate gate share the same terminal cause vocabulary (v0.2.3 X-02).
func (row CaseRunSnapshot) ExecutorCapabilityBlocked() bool {
	return row.TerminalCause == TerminalCauseExecutorCapability
}

// ExpectedFailureTriage reports whether the latest finished issue triage
// classified this run as expected_failure. The report summary counts it
// separately from product defects.
func (row CaseRunSnapshot) ExpectedFailureTriage() bool {
	return row.TriageClassification == TriageClassificationExpectedFailure
}

// EvidenceSnapshot is one evidence metadata row of a report. Content bytes and
// storage paths are deliberately absent (spec §6.1); ContentPath is the
// human-viewable link to the existing content endpoint (v0.2.4-G SC-G2/G7 C3),
// not a storage path — the bytes stay behind the execution API.
type EvidenceSnapshot struct {
	ID           int64
	CaseRunID    int64
	SUTID        *int64
	StepSeq      *int
	AssertionSeq *int
	Kind         string
	SHA256       string
	SizeBytes    int64
	// ContentPath links the evidence content endpoint
	// GET /api/versions/{versionID}/evidence/{evidenceID}/content so a human can
	// open the actual screenshot/log/network capture instead of reading a hash.
	ContentPath string
}

// IssueSnapshot is one delivery issue projection of a report. SourceIssueIDs
// lists issues merged into this one; CaseRunIDs lists its case_run refs (spec
// §6.1).
type IssueSnapshot struct {
	ID             int64
	VersionID      int64
	Title          string
	Status         IssueStatus
	Category       string
	Severity       string
	Source         string
	MergedIntoID   *int64
	SourceIssueIDs []int64
	CaseRunIDs     []int64
	// TriageStatus/TriageClassification are the latest finished issue.triage
	// projection. They are display metadata (the issue row itself is never
	// rewritten by a triage proposal) and let the report honor a flaky
	// classification instead of presenting the issue as a product defect.
	TriageStatus         string
	TriageClassification string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	// DedupeKey is the issue's deterministic dedupe identity
	// (sha256(version|caseVersion|stepSeq|assertionSeq|failureClass)[:32]).
	// The design lists dedupe among the issue conclusion inputs (§7.4, P22),
	// so it participates in the canonical fingerprint. The report read model
	// populates it from the issue row.
	DedupeKey string
	// RepresentativeCaseRunID is the DEFINED representative run of the issue's
	// linked runs (v0.2.4-G SC-G2 I4): the newest failed run, else the newest
	// linked run (RepresentativeRunID). The five-element expected/observed below
	// come from THIS run only — never mixed across runs. 0 when no linked run is
	// present in the snapshot (the structured fields then fall back to the
	// persisted issue row, the creation-time representative).
	RepresentativeCaseRunID int64
	// FailedExpected/FailedActual/FailedReason are the five-element card's
	// structured failure detail taken from the representative run's failed
	// assertion (expected/observed/reason). They are display metadata derived
	// from immutable assertion facts and the fingerprinted run set, not payload
	// members.
	FailedExpected string
	FailedActual   string
	FailedReason   string
	// RepresentativeEvidence is the evidence bound to the representative run's
	// failed assertion/step, reverse-looked-up through the evidence_ref read
	// side (v0.2.4-G SC-G9, T4 EvidenceRefReader). Each row carries a viewable
	// ContentPath. Bounded by the report evidence display caps.
	RepresentativeEvidence []EvidenceSnapshot
	// RepresentativeEvidenceTruncated records that the card's representative
	// evidence DISPLAY list was reduced below its full reverse-lookup count by
	// the aggregate card budget (v0.2.4-G final fix wave #6, art.7.6). The card
	// then discloses 证据超出显示上限（已截断） instead of falsely claiming 无代表 run
	// 证据. Display metadata derived from the bounded projection, not a payload
	// member (the per-run evidence digest already fingerprints every row).
	RepresentativeEvidenceTruncated bool
}

// FlakyTriage reports whether the latest finished triage classified this issue
// as flaky. Only a done proposal is authoritative: a degraded/failed run or a
// non-flaky classification never downgrades a real application defect.
func (row IssueSnapshot) FlakyTriage() bool {
	return row.TriageStatus == string(AnalysisStatusDone) &&
		row.TriageClassification == TriageClassificationFlaky
}

// ExpectedFailureTriage reports whether the latest finished triage classified
// this issue as expected_failure. Like FlakyTriage, only a done proposal is
// authoritative; the stored issue row itself is never rewritten.
func (row IssueSnapshot) ExpectedFailureTriage() bool {
	return row.TriageStatus == string(AnalysisStatusDone) &&
		row.TriageClassification == TriageClassificationExpectedFailure
}

// OpenProductIssue reports whether this issue is an open issue that the platform
// triage did not classify as flaky or expected_failure, i.e. the issue counts as
// an open product defect in the report summary (Task 122). The stored status is
// never changed by a triage proposal; this is a report-visible projection.
func (row IssueSnapshot) OpenProductIssue() bool {
	return row.Status == IssueStatusOpen && !row.FlakyTriage() && !row.ExpectedFailureTriage()
}

// SUTSnapshot is one SUT used to label report rows
// (spec §6.1). Its ID equals SUTID: v0.2.4-B dropped the binding-row id.
type SUTSnapshot struct {
	ID              int64
	SUTID           int64
	SUTName         string
	EnvironmentID   int64
	EnvironmentName string
	// SUTDescription is the SUT's natural-language introduction from the
	// resource catalog (v0.2.4-G SC-G2 C2), so a human knows which system a row
	// concerns. Display context; the canonical payload keeps the SUT name only.
	SUTDescription string
}

// CoverageSnapshot is the P3 execution coverage projection of a version
// (spec §6.1). It counts the newest non-superseded case_run of every executable
// case version: a superseded process row is not a result.
type CoverageSnapshot struct {
	RequirementsTotal   int
	RequirementsCovered int
	CaseVersionsTotal   int
	CaseVersionsCovered int
	Gaps                []CoverageGapSnapshot
	// ByRequirement is the per-requirement projection of the same executable
	// case versions and latest runs (see RequirementCoverageSnapshot). It is
	// derived metadata: the canonical fingerprint already covers the case
	// versions, their latest runs and the gaps, so this field is deliberately
	// absent from the fingerprint payload. Report narrative validation uses it
	// to prove statements such as "no valid execution" wrong; the report writer
	// tool exposes its counts so the model quotes instead of inventing them.
	ByRequirement []RequirementCoverageSnapshot
	// ACVerification is the AC-level verification coverage matrix (v0.2.4-G
	// SC-G4 I1): per-AC verified/unverified/failed/uncovered + g/w/t, separating
	// link coverage from acceptance verification. Derived display metadata (AC
	// content via ACHash, linkage via case versions, verdict via case_run status
	// are all fingerprinted), deliberately absent from the canonical payload.
	ACVerification ACCoverageSnapshot
}

// RequirementCoverageSnapshot is the deterministic coverage of one requirement,
// derived strictly from its executable case versions and their latest case_run
// statuses. CaseVersionsTotal always equals the sum of the four outcome
// counters: covered, not run, product failed coverage and flaky coverage.
type RequirementCoverageSnapshot struct {
	RequirementID              int64
	CaseVersionsTotal          int
	CaseVersionsCovered        int
	CaseVersionsNotRun         int
	CaseVersionsFailedCoverage int
	// CaseVersionsFlakyCoverage counts failed latest runs whose issue triage is
	// flaky. They stay uncovered (a failure is never a pass) but are not a
	// product coverage failure.
	CaseVersionsFlakyCoverage int
}

// CoverageGapSnapshot is one uncovered case version.
type CoverageGapSnapshot struct {
	RequirementID int64
	CaseVersionID int64
	Reason        string
}

// AC verification states (v0.2.4-G SC-G4 I1). Link coverage (an AC has a
// covering confirmed case) is NOT acceptance verification: the per-AC state
// overlays the covering cases' latest terminal execution verdict. A human reading
// "100% coverage" must not misread link coverage as acceptance-passed — the matrix
// distinguishes verified (a covering run passed) from unverified (covering runs
// blocked/inconclusive/not-run), failed (a covering run failed) and uncovered (no
// covering case).
const (
	// ACVerificationVerified means at least one covering confirmed case's latest
	// run passed: the acceptance criterion has a passing execution demonstration.
	ACVerificationVerified = "verified"
	// ACVerificationUnverified means covering cases exist but none produced a
	// definitive pass/fail (blocked/inconclusive/not-run): the AC is linked but
	// not verified.
	ACVerificationUnverified = "unverified"
	// ACVerificationFailed means no covering run passed and at least one covering
	// run failed: the AC has a failing execution.
	ACVerificationFailed = "failed"
	// ACVerificationUncovered means no current confirmed case covers the AC.
	ACVerificationUncovered = "uncovered"
)

// MaxReportACs caps the AC verification matrix DISPLAY projection (v0.2.4-G
// SC-G4 M4): a large AC×case matrix is explicitly truncated with an annotation,
// never silently dropped (art.7.6). The cap bounds the total AC rows across
// requirements; the summary counts still reflect the full set.
const MaxReportACs = 500

// ACCriterionSnapshot is one acceptance criterion's natural-language text
// (given/when/then) with its revision-stable id, so the matrix shows the
// criterion itself, not just an id.
type ACCriterionSnapshot struct {
	ID    string
	Given string
	When  string
	Then  string
}

// ACVerificationSnapshot is one AC's verification row (v0.2.4-G SC-G4 I1): its
// g/w/t text, its verification state and the current confirmed case versions
// that cover it (traceability). It is derived display metadata — the AC content
// is fingerprinted via RequirementSnapshot.ACHash, the linkage via the case
// versions, and the verdict via the case_run status — so it is deliberately
// absent from the canonical fingerprint payload (like CoverageSnapshot.ByRequirement).
type ACVerificationSnapshot struct {
	RequirementID          int64
	ACID                   string
	Given                  string
	When                   string
	Then                   string
	State                  string
	CoveringCaseVersionIDs []int64
}

// RequirementACCoverageSnapshot is one requirement's AC verification matrix.
type RequirementACCoverageSnapshot struct {
	RequirementID int64
	ACs           []ACVerificationSnapshot
}

// ACCoverageSnapshot is the version-level AC verification coverage projection
// (v0.2.4-G SC-G4). It separates LINK coverage (LinkCovered: ACs with at least
// one covering confirmed case) from VERIFICATION coverage (Verified/Unverified/
// Failed/Uncovered counts), so a human knows which acceptance criteria passed,
// failed, are linked-but-unverified, or are uncovered. Truncated/TotalBeforeCap
// record the explicit M4 display cap; Displayed is the number of AC rows actually
// kept after the cap (final fix wave #2: the truncation note states the DISPLAYED
// count, not the full Total, so it never reads "显示 501 条" above a 500-row table).
// Derived display metadata, not a payload member.
type ACCoverageSnapshot struct {
	ByRequirement  []RequirementACCoverageSnapshot
	Total          int
	Verified       int
	Unverified     int
	Failed         int
	Uncovered      int
	LinkCovered    int
	Truncated      bool
	TotalBeforeCap int
	Displayed      int
}

// DeriveACVerificationState overlays the covering cases' latest terminal verdicts
// onto link coverage (v0.2.4-G SC-G4 I1). No covering case (empty verdicts) ->
// uncovered; any passed -> verified; else any failed -> failed; else (blocked/
// inconclusive/skipped/not-run) -> unverified. A not-run covering case contributes
// an empty verdict and never verifies. The verdict vocabulary is the execution
// domain's (single source of truth).
func DeriveACVerificationState(coveringVerdicts []string) string {
	if len(coveringVerdicts) == 0 {
		return ACVerificationUncovered
	}
	hasFailed := false
	for _, verdict := range coveringVerdicts {
		switch verdict {
		case string(execution.CaseRunPassed):
			return ACVerificationVerified
		case string(execution.CaseRunFailed):
			hasFailed = true
		}
	}
	if hasFailed {
		return ACVerificationFailed
	}
	return ACVerificationUnverified
}

// JudgmentFindingSnapshot is one design judge finding projected onto the report
// (v0.2.4-G SC-G4 I3): dimension/severity/message and the optional case ref.
type JudgmentFindingSnapshot struct {
	Dimension string
	Severity  string
	Message   string
	CaseRef   string
}

// JudgmentSnapshot is one current case's design judge fact projection
// (v0.2.4-G SC-G4 I3): the derived review_state, the latest verdict bound to the
// current case version (empty when unjudged or only a stale verdict exists) and
// its findings. The report presents it at the case/AC coverage area so a human
// sees the judge's assessment alongside execution coverage. It is consumed
// through the cross-context judge read port; derived display metadata, not a
// canonical payload member (review_state is an instant projection, art.V.11).
type JudgmentSnapshot struct {
	CaseID        int64
	CaseVersionID int64
	RequirementID int64
	ReviewState   string
	Verdict       string
	Findings      []JudgmentFindingSnapshot
	// ACCoverageMatrix is the judge's CANDIDATE AC×case coverage assessment
	// (design_judgment.ac_coverage_matrix, AC id -> covering case ids), consumed
	// for presentation (v0.2.4-G SC-G4). It is the judge's candidate content, NOT
	// the verification truth: the authoritative AC verification matrix uses the
	// confirmed covered_acs linkage + case_run terminal verdict (art.III — the
	// model only submits candidates). Display metadata, not a payload member.
	ACCoverageMatrix map[string][]string
	// ACCoverageMatrixDroppedCount counts the malformed judge matrix entries
	// discarded during projection (a non-array AC value, or a non-string/empty
	// case id). The discard is disclosed, never silent (art.7.6): the presentation
	// may be smaller than the judge submitted, and the count says so.
	ACCoverageMatrixDroppedCount int
}

// WaveRepoBaselineSnapshot is one frozen (repository, commit) pair of the wave's
// planned code baseline (v0.2.4-G SC-G6).
type WaveRepoBaselineSnapshot struct {
	RepoID    int64
	CommitSHA string
}

// WaveSUTEnvironmentSnapshot is one SUT's planned environment items frozen in the
// wave scope snapshot (v0.2.4-G SC-G6): base_url, the page/API/database asset id
// sets, account roles and the code baseline. It is the "planned environment"
// context a human reads next to the per-run ACTUAL fingerprint.
type WaveSUTEnvironmentSnapshot struct {
	SUTID               int64
	BaseURL             string
	PageIDs             []int64
	APIIDs              []int64
	DatabaseIDs         []int64
	AccountRoles        []string
	RepositoryBaselines []WaveRepoBaselineSnapshot
}

// WaveEnvironmentDivergenceSnapshot discloses a carrier-vs-per-run environment
// divergence (v0.2.4-G SC-G6 R-G1/M1): the wave carrier ("planned") fingerprint/
// baseline differs from the member run's actual intent fingerprint/baseline. The
// report shows planned vs actual plus a READABLE difference (which env dimensions
// differ — base_url/asset set/account roles/code baseline — not just two sha256
// strings). Divergence is disclosed, never blocks publication (art.4.6: the
// per-run actual fingerprint is the fingerprint member; the carrier is context).
type WaveEnvironmentDivergenceSnapshot struct {
	CaseRunID           int64
	CaseVersionID       int64
	PlannedFingerprint  string
	ActualFingerprint   string
	PlannedCodeBaseline string
	ActualCodeBaseline  string
	Differences         []string
}

// FailureExplanationSnapshot is the failure-reason projection of one terminal
// non-passed run that has no defect issue (v0.2.4-G SC-G2 C1). It answers "why
// did case X not pass" for blocked/inconclusive/environment/automation/
// expected_failure runs (and failed(application) runs that did not meet the
// issue-candidate evidence gate). Every field is derived from fingerprinted
// run facts; the projection itself is display metadata.
type FailureExplanationSnapshot struct {
	CaseRunID             int64
	CaseVersionID         int64
	RequirementID         int64
	CaseTitle             string
	Status                string
	FailureClass          string
	EffectiveFailureClass string
	TerminalCause         string
	Explanation           string
}

// NeedsFailureExplanation reports whether a run belongs in the non-issue
// failure section: a terminal non-passed verdict (failed/blocked/inconclusive)
// with no defect issue. passed/skipped/process states are not failures; a run
// with an issue is explained by its defect card instead.
func (row CaseRunSnapshot) NeedsFailureExplanation(hasIssue bool) bool {
	if hasIssue {
		return false
	}
	switch row.Status {
	case string(execution.CaseRunFailed), string(execution.CaseRunBlocked), string(execution.CaseRunInconclusive):
		return true
	default:
		return false
	}
}

// FailureExplanationText returns the deterministic human-readable explanation
// of a terminal non-passed run (v0.2.4-G SC-G2 C1/M3). It is keyed on the run
// status, the report-visible failure class and the stable terminal cause, so a
// human reads WHY the case did not pass without knowing any internal code. The
// text never names a product defect for a non-application class.
func FailureExplanationText(status, effectiveClass, terminalCause string) string {
	cause := terminalCauseDetail(terminalCause)
	switch effectiveClass {
	case FailureClassBlocked:
		if terminalCause == TerminalCauseExecutorCapability {
			return "执行器能力受限，无法完成用例要求的操作；这不是产品缺陷。" + cause
		}
		return "执行被阻塞，用例未能完成。" + cause
	case FailureClassAutomation:
		return "平台分诊判定为自动化偶发失败（flaky），不是产品缺陷；建议重跑确认。" + cause
	case FailureClassExpectedFailure:
		return "平台分诊判定为预期内失败（如预算耗尽或未观察到目标），需要重跑后才能得出结论。" + cause
	case string(execution.FailureClassEnvironment):
		return "执行环境问题导致失败，不是产品缺陷。" + cause
	case string(execution.FailureClassSession):
		return "会话/登录态问题导致失败，不是产品缺陷。" + cause
	case string(execution.FailureClassModel):
		return "模型执行异常导致失败，不是产品缺陷。" + cause
	case string(execution.FailureClassTimeout):
		return "执行超时导致失败。" + cause
	case string(execution.FailureClassExecutorLost):
		return "执行器失联导致失败，不是产品缺陷。" + cause
	case string(execution.FailureClassApplication):
		// The trigger is only "no issue references this run in the report", which
		// has several possible causes (did not pass the candidate gate, async
		// ingestion not yet run/failed, or deduped into a merged issue). The text
		// must NOT assert a single definite cause it cannot know (T8 fix round,
		// IMPORTANT #3 — descriptions accurate).
		return "该失败在报告中没有对应的缺陷卡片（未满足建单条件、尚未建单或已合并）；请复核执行证据。" + cause
	}
	if status == string(execution.CaseRunInconclusive) {
		return "证据不足，无法判定用例通过或失败。" + cause
	}
	return "失败原因未分类，请复核执行记录。" + cause
}

// terminalCauseDetail renders the stable terminal cause as a human-readable
// suffix (empty when there is no cause). The cause codes are the execution
// domain vocabulary; the text is the report's fixed localization.
func terminalCauseDetail(terminalCause string) string {
	switch terminalCause {
	case "":
		return ""
	case execution.TerminalCauseTurnBudget:
		return "（执行轮次预算耗尽）"
	case execution.TerminalCauseTokenBudget:
		return "（token 预算耗尽）"
	case execution.TerminalCauseTimeBudget:
		return "（执行时间预算耗尽）"
	case execution.TerminalCauseAgentStopped:
		return "（执行代理停止）"
	case TerminalCauseExecutorCapability:
		return "（执行器能力限制）"
	default:
		return "（终止原因：" + terminalCause + "）"
	}
}

// DescribeEnvironmentDivergence builds the READABLE carrier-vs-per-run
// environment difference (v0.2.4-G SC-G6 R-G1/M1). It names WHICH environment
// dimensions differ (base_url / asset set / account roles / code baseline / SUT
// set) and shows the wave's planned configuration, never just two sha256 strings.
// The per-run actual environment items are not all stored (the intent carries the
// fingerprint/baseline hashes plus the resolved SUT set), so the actual side is
// disclosed as the executed fingerprint/baseline and the resolved SUT set — no
// per-run item value is fabricated (art.5 facts over projections). The caller
// only invokes this when the carrier and the run actually diverge.
func DescribeEnvironmentDivergence(plannedFingerprint, actualFingerprint, plannedBaseline, actualBaseline string, plannedSUTs []WaveSUTEnvironmentSnapshot, actualSUTIDs []int64) []string {
	diffs := make([]string, 0)
	if plannedFingerprint != actualFingerprint {
		if len(plannedSUTs) == 0 {
			diffs = append(diffs, "环境配置不同（计划 vs 实际执行）：base_url/资产集/账号角色等维度可能已变化；实际执行环境指纹不同。")
		}
		for _, sut := range plannedSUTs {
			diffs = append(diffs, fmt.Sprintf(
				"环境配置不同（计划 vs 实际执行）：SUT %d 计划 base_url=%s，页面资产=%v，API资产=%v，数据库资产=%v，账号角色=%v；实际执行环境指纹不同。",
				sut.SUTID, sut.BaseURL, sut.PageIDs, sut.APIIDs, sut.DatabaseIDs, sut.AccountRoles))
		}
		// Concrete SUT-set diff: the resolved actual SUT set IS a per-run fact
		// (case_run_sut_context), so this dimension is compared item-level.
		plannedIDs := make([]int64, 0, len(plannedSUTs))
		plannedSet := make(map[int64]bool, len(plannedSUTs))
		for _, sut := range plannedSUTs {
			if !plannedSet[sut.SUTID] {
				plannedSet[sut.SUTID] = true
				plannedIDs = append(plannedIDs, sut.SUTID)
			}
		}
		sort.Slice(plannedIDs, func(i, j int) bool { return plannedIDs[i] < plannedIDs[j] })
		actualIDs := append([]int64(nil), actualSUTIDs...)
		sort.Slice(actualIDs, func(i, j int) bool { return actualIDs[i] < actualIDs[j] })
		if !equalInt64Sets(plannedIDs, actualIDs) {
			diffs = append(diffs, fmt.Sprintf("被测系统集不同：计划 SUT=%v，实际执行 SUT=%v。", plannedIDs, actualIDs))
		}
	}
	if plannedBaseline != actualBaseline {
		hasBaseline := false
		for _, sut := range plannedSUTs {
			for _, baseline := range sut.RepositoryBaselines {
				hasBaseline = true
				diffs = append(diffs, fmt.Sprintf(
					"代码基线不同：计划仓库 %d @ %s；实际执行代码基线指纹不同。", baseline.RepoID, baseline.CommitSHA))
			}
		}
		if !hasBaseline {
			diffs = append(diffs, "代码基线不同：计划 vs 实际执行代码基线指纹不同。")
		}
	}
	return diffs
}

// equalInt64Sets reports whether two sorted id slices carry the same set.
func equalInt64Sets(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
