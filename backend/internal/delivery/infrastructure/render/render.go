// Package render renders deterministic, fully self-contained delivery report
// HTML (spec §6.2). It implements the delivery domain ReportRenderer port with
// Go's html/template, a fixed built-in template, inline CSS and no external
// assets. All user-controlled strings are escaped by html/template; narratives
// are plain text and keep their line breaks.
package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	// TemplateVersionV1 is the frozen P4 built-in report template: five
	// sections. Its rendered bytes must never change (spec P7-D-03).
	TemplateVersionV1 = "1"

	// TemplateVersionV2 is the P7 built-in template: v1 plus the optional
	// appendix section (report.section_appendix).
	TemplateVersionV2 = "2"

	// TemplateVersionV3 is the v0.2.4-G human-useful fixed template (SC-G1/G2):
	// overview + five-element defect cards + non-issue failure explanations +
	// traceability chain + evidence appendix + AC verification matrix + waived
	// content. Its core sections are non-omittable and its bytes are frozen
	// (TestTemplateV3Golden). v1/v2 stay byte-identical.
	TemplateVersionV3 = "3"

	// MaxHTMLBytes is the hard upper bound of one rendered report (spec
	// §6.1/§6.2). Rendering truncates issues, waves and case runs in that order
	// until the document fits; if it still does not fit, Render fails visibly.
	MaxHTMLBytes = 2 * 1024 * 1024
)

// HTMLRenderer implements delivery.ReportRenderer with the fixed built-in
// template of one template version. It is safe for concurrent use: the parsed
// template is read-only and every render builds its own view and buffer.
type HTMLRenderer struct {
	templateVersion string
	template        *template.Template
}

var _ delivery.ReportRenderer = (*HTMLRenderer)(nil)

// NewHTMLRenderer parses one built-in report template. templateVersion accepts
// "1" (the frozen P4 five-section layout), "2" (P7: v1 plus the optional appendix
// section) and "3" (v0.2.4-G: the human-useful fixed five-element-card layout);
// any other value is an error because EzTest only ships built-in templates.
func NewHTMLRenderer(templateVersion string) (*HTMLRenderer, error) {
	var source string
	switch templateVersion {
	case TemplateVersionV1:
		source = reportTemplateV1
	case TemplateVersionV2:
		source = reportTemplateV2
	case TemplateVersionV3:
		source = reportTemplateV3
	default:
		return nil, fmt.Errorf("render: unsupported report template version %q, only %q, %q and %q are built in", templateVersion, TemplateVersionV1, TemplateVersionV2, TemplateVersionV3)
	}
	parsed, err := template.New("report").Funcs(template.FuncMap{
		"int64ptr":           formatInt64Ptr,
		"intptr":             formatIntPtr,
		"timeptr":            formatTimePtr,
		"joinInt64s":         joinInt64s,
		"joinStrings":        joinStrings,
		"acStateLabel":       acStateLabel,
		"issueStatusLabel":   issueStatusLabel,
		"issueCategoryLabel": issueCategoryLabel,
		"severityLabel":      severityLabel,
		"closureKindLabel":   closureKindLabel,
		"runStatusLabel":     runStatusLabel,
		"failureClassLabel":  failureClassLabel,
		"reviewStateLabel":   reviewStateLabel,
		"verdictLabel":       verdictLabel,
	}).Parse(source)
	if err != nil {
		return nil, fmt.Errorf("render: parse report template: %w", err)
	}
	return &HTMLRenderer{templateVersion: templateVersion, template: parsed}, nil
}

// TemplateVersion reports the built-in template version of this renderer. The
// report service records it on every new revision so the revision metadata can
// never disagree with the rendered bytes (P7-D-03).
func (r *HTMLRenderer) TemplateVersion() string {
	if r == nil {
		return ""
	}
	return r.templateVersion
}

// Render renders one report revision from the deterministic snapshot and the
// optional plain-text narrative. Narrative text is escaped as plain text and
// split into preserved lines; it can never inject HTML. When the rendered HTML
// exceeds MaxHTMLBytes, issues, then waves, then case runs are halved in order
// and a visible truncation note is added; lists whose section is disabled are
// not rendered and are therefore skipped. If the document still exceeds the
// limit, Render returns an error instead of silently dropping content.
func (r *HTMLRenderer) Render(ctx context.Context, snapshot delivery.ReportSnapshot, narrative string, opts delivery.SectionOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.template == nil {
		return nil, errors.New("render: report renderer is not initialized")
	}
	// v3 is the fixed-format user report (SC-G1 I3): its core sections (概览/缺陷/
	// 覆盖/追溯/证据) are non-omittable. SectionOptions cannot drop them, so force
	// them on before building the view. The traceability section is always rendered
	// by the v3 template itself (it has no section switch).
	if r.templateVersion == TemplateVersionV3 {
		opts.Summary = true
		opts.Issues = true
		opts.Coverage = true
		opts.Evidence = true
	}
	data := newRenderData(snapshot, narrative, opts)
	data.TemplateVersion = r.templateVersion
	// v3-only stable projections (judge/failure/AC/waiver carriers, conclusion,
	// terms, AC rate) are built only for the v3 template. v1/v2 never reference
	// them, so gating here is output-neutral for the frozen v1/v2 bytes (T19b #24;
	// v1 golden hash + v2 tests pin the byte-identical guarantee).
	if r.templateVersion == TemplateVersionV3 {
		data.initV3Stable(snapshot)
	}
	output, err := r.render(data)
	if err != nil {
		return nil, err
	}
	if len(output) <= MaxHTMLBytes {
		return output, nil
	}

	// Kept counts start at the full list sizes: a list is only recorded as
	// truncated once it is actually sliced. Otherwise untouched lists would be
	// reported as reduced to zero.
	state := &truncationState{
		issuesTotal:       len(data.Issues),
		issuesKept:        len(data.Issues),
		wavesTotal:        len(data.Waves),
		wavesKept:         len(data.Waves),
		caseRunsTotal:     len(data.CaseRuns),
		caseRunsKept:      len(data.CaseRuns),
		explanationsTotal: len(data.FailureExplanations),
		explanationsKept:  len(data.FailureExplanations),
		judgmentsTotal:    len(data.Judgments),
		judgmentsKept:     len(data.Judgments),
	}
	// A list that its section does not render cannot shrink the document, so a
	// disabled section's list is neither halved nor reported.
	if opts.Issues {
		for len(data.Issues) > 0 {
			data.Issues = data.Issues[:len(data.Issues)/2]
			state.issuesKept = len(data.Issues)
			if output, err = r.renderTruncated(data, state); err != nil {
				return nil, err
			}
			if len(output) <= MaxHTMLBytes {
				return output, nil
			}
		}
	}
	if opts.Results {
		for len(data.Waves) > 0 {
			data.Waves = data.Waves[:len(data.Waves)/2]
			state.wavesKept = len(data.Waves)
			if output, err = r.renderTruncated(data, state); err != nil {
				return nil, err
			}
			if len(output) <= MaxHTMLBytes {
				return output, nil
			}
		}
	}
	// Case runs are visible both as results rows and, through their evidence
	// rows, in the evidence section, so they are rendered if either is enabled.
	if opts.Results || opts.Evidence {
		for len(data.CaseRuns) > 0 {
			data.CaseRuns = data.CaseRuns[:len(data.CaseRuns)/2]
			state.caseRunsKept = len(data.CaseRuns)
			if output, err = r.renderTruncated(data, state); err != nil {
				return nil, err
			}
			if len(output) <= MaxHTMLBytes {
				return output, nil
			}
		}
	}
	// v3-only carriers (fix minor): the failure-explanation and judge sections
	// derive from lists the loops above never shrink, so a large version could
	// otherwise fail to render outright. Halve them too, with the same explicit
	// no-silent-drop note. The AC matrix is bounded by its render-side display cap
	// (buildV3Display) and the waived scope by the AC cap, so they need no halving.
	if r.templateVersion == TemplateVersionV3 {
		for len(data.FailureExplanations) > 0 {
			data.FailureExplanations = data.FailureExplanations[:len(data.FailureExplanations)/2]
			state.explanationsKept = len(data.FailureExplanations)
			if output, err = r.renderTruncated(data, state); err != nil {
				return nil, err
			}
			if len(output) <= MaxHTMLBytes {
				return output, nil
			}
		}
		for len(data.Judgments) > 0 {
			data.Judgments = data.Judgments[:len(data.Judgments)/2]
			state.judgmentsKept = len(data.Judgments)
			if output, err = r.renderTruncated(data, state); err != nil {
				return nil, err
			}
			if len(output) <= MaxHTMLBytes {
				return output, nil
			}
		}
	}
	return nil, fmt.Errorf("render: report HTML is %d bytes, still above the %d-byte limit after truncating the rendered lists", len(output), MaxHTMLBytes)
}

func (r *HTMLRenderer) renderTruncated(data *renderData, state *truncationState) ([]byte, error) {
	data.Truncated = true
	data.TruncationNote = joinTruncationNotes(data.SnapshotTruncationNote, state.note())
	return r.render(data)
}

func (r *HTMLRenderer) render(data *renderData) ([]byte, error) {
	view := *data
	view.Issues = make([]delivery.IssueSnapshot, 0, len(data.Issues))
	for index := len(data.Issues) - 1; index >= 0; index-- {
		view.Issues = append(view.Issues, data.Issues[index])
	}
	view.SUTRows = buildSUTRows(data.Waves, data.SUTs)
	view.EvidenceRows = buildEvidenceRows(data.CaseRuns)
	if r.templateVersion == TemplateVersionV3 {
		view.buildV3Display()
	}

	var buffer bytes.Buffer
	if err := r.template.Execute(&buffer, &view); err != nil {
		return nil, fmt.Errorf("render: execute report template: %w", err)
	}
	return buffer.Bytes(), nil
}

// renderData is the template data of one render. Fields are exported because
// html/template can only reach exported fields. Slice order is already
// normalized (IDs ascending) before rendering; derived tables are rebuilt on
// every render so truncation also shrinks them.
type renderData struct {
	TemplateVersion string
	Title           string
	KindLabel       string
	VersionID       int64
	HasWave         bool
	WaveID          int64
	GeneratedAt     string
	// P7 template v2 appendix metadata. The fields are render-only; they never
	// participate in the report input fingerprint.
	ReportID         int64
	ReportRevision   int32
	InputFingerprint string
	SectionSwitches  []sectionSwitch
	Sections         delivery.SectionOptions
	Requirements     []delivery.RequirementSnapshot
	Waves            []delivery.WaveSnapshot
	CaseRuns         []delivery.CaseRunSnapshot
	Issues           []delivery.IssueSnapshot
	SUTs             []delivery.SUTSnapshot
	Coverage         delivery.CoverageSnapshot
	SUTRows          []sutRow
	EvidenceRows     []delivery.EvidenceSnapshot
	Summary          summaryView
	NarrativeLines   []string
	HasNarrative     bool
	Truncated        bool
	TruncationNote   string
	// SnapshotTruncationNote is the §6.1 cap note derived from the snapshot's
	// TruncatedSections. The 2 MiB truncation pass appends its own note to it.
	SnapshotTruncationNote string

	// v3 human-useful fixed-template projections (v0.2.4-G T10). Only the v3
	// template consumes them; v1/v2 never reference these fields so their bytes
	// stay frozen. The raw carriers and the stable views (conclusion/terms/AC
	// rate/waived scope) are set once in newRenderData; the display views (cards/
	// traceability/AC matrix/evidence links/judge rows) are rebuilt on every render
	// so they stay consistent with the truncatable lists.
	Judgments               []delivery.JudgmentSnapshot
	FailureExplanations     []delivery.FailureExplanationSnapshot
	ACVerification          delivery.ACCoverageSnapshot
	ClosureKind             string
	Unverified              bool
	WaiverReason            string
	WaiverOperator          string
	WaivedAt                string
	Conclusion              conclusionView
	Terms                   []termDef
	ACRate                  string
	EvidenceTotalCount      int
	HasWaived               bool
	VersionWaived           bool
	Waived                  waivedView
	DefectCards             []defectCard
	Traceability            []traceRow
	ACMatrix                []acMatrixRow
	ACMatrixRenderTruncated bool
	ACMatrixRenderTotal     int
	EvidenceLinks           []evidenceLink
	JudgeRows               []judgeRow
}

// sectionSwitch is one row of the v2 appendix section-switch list.
type sectionSwitch struct {
	Label   string
	State   string
	Enabled bool
}

// summaryView is the deterministic section summary. Numbers come from the
// snapshot, not from the narrative (spec §6.2).
type summaryView struct {
	Total              int
	Passed             int
	Failed             int
	Blocked            int
	Inconclusive       int
	Skipped            int
	Other              int
	Issues             int
	OpenIssues         int
	Evidence           int
	Waves              int
	Requirements       int
	RequirementsClosed int
}

// sutRow is one wave × SUT result row.
type sutRow struct {
	WaveID       int64
	SUTID        int64
	Label        string
	Total        int
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
}

func newRenderData(snapshot delivery.ReportSnapshot, narrative string, opts delivery.SectionOptions) *renderData {
	data := &renderData{
		TemplateVersion:  TemplateVersionV1,
		VersionID:        snapshot.VersionID,
		GeneratedAt:      formatTime(snapshot.GeneratedAt),
		ReportID:         snapshot.ReportID,
		ReportRevision:   snapshot.ReportRevision,
		InputFingerprint: snapshot.InputFingerprint,
		SectionSwitches:  buildSectionSwitches(opts),
		Sections:         opts,
	}
	if note := snapshotTruncationNote(snapshot.TruncatedSections); note != "" {
		data.Truncated = true
		data.SnapshotTruncationNote = note
		data.TruncationNote = note
	}
	if snapshot.Kind == delivery.ReportKindWave {
		data.KindLabel = "波次报告"
		if snapshot.WaveID != nil {
			data.HasWave = true
			data.WaveID = *snapshot.WaveID
			data.Title = fmt.Sprintf("波次报告 · 版本 #%d · Wave #%d", snapshot.VersionID, *snapshot.WaveID)
		} else {
			data.Title = fmt.Sprintf("波次报告 · 版本 #%d", snapshot.VersionID)
		}
	} else {
		data.KindLabel = "版本报告"
		data.Title = fmt.Sprintf("版本报告 · 版本 #%d", snapshot.VersionID)
	}

	data.Requirements = append([]delivery.RequirementSnapshot(nil), snapshot.Requirements...)
	sort.SliceStable(data.Requirements, func(i, j int) bool {
		return data.Requirements[i].ID < data.Requirements[j].ID
	})

	data.Waves = make([]delivery.WaveSnapshot, len(snapshot.Waves))
	copy(data.Waves, snapshot.Waves)
	for index := range data.Waves {
		bySUT := make([]delivery.SUTCountsSnapshot, len(data.Waves[index].BySUT))
		copy(bySUT, data.Waves[index].BySUT)
		sort.SliceStable(bySUT, func(i, j int) bool {
			return bySUT[i].SUTID < bySUT[j].SUTID
		})
		data.Waves[index].BySUT = bySUT
	}
	sort.SliceStable(data.Waves, func(i, j int) bool { return data.Waves[i].ID < data.Waves[j].ID })

	data.CaseRuns = make([]delivery.CaseRunSnapshot, len(snapshot.CaseRuns))
	copy(data.CaseRuns, snapshot.CaseRuns)
	for index := range data.CaseRuns {
		sutIDs := make([]int64, len(data.CaseRuns[index].SUTIDs))
		copy(sutIDs, data.CaseRuns[index].SUTIDs)
		sort.Slice(sutIDs, func(i, j int) bool { return sutIDs[i] < sutIDs[j] })
		data.CaseRuns[index].SUTIDs = sutIDs

		evidence := make([]delivery.EvidenceSnapshot, len(data.CaseRuns[index].Evidence))
		copy(evidence, data.CaseRuns[index].Evidence)
		sort.SliceStable(evidence, func(i, j int) bool { return evidence[i].ID < evidence[j].ID })
		data.CaseRuns[index].Evidence = evidence
		// Task 118: the frozen template keeps its columns; the render projection
		// shows the triage-honoring class so a flaky-triaged failure is never
		// rendered as an application product defect.
		data.CaseRuns[index].FailureClass = data.CaseRuns[index].EffectiveFailureClass()
	}
	sort.SliceStable(data.CaseRuns, func(i, j int) bool { return data.CaseRuns[i].ID < data.CaseRuns[j].ID })

	// Issues are kept in ascending ID order while truncating (so "first N by
	// id" is deterministic) and reversed only for display (spec §6.1). A
	// flaky- or expected_failure-triaged issue is displayed with the triage
	// classification instead of its stored product category; the issue row
	// itself is never rewritten (Task 118/122).
	data.Issues = append([]delivery.IssueSnapshot(nil), snapshot.Issues...)
	for index := range data.Issues {
		switch {
		case data.Issues[index].FlakyTriage():
			data.Issues[index].Category = delivery.TriageClassificationFlaky
		case data.Issues[index].ExpectedFailureTriage():
			data.Issues[index].Category = delivery.TriageClassificationExpectedFailure
		}
	}
	sort.SliceStable(data.Issues, func(i, j int) bool { return data.Issues[i].ID < data.Issues[j].ID })

	data.SUTs = append([]delivery.SUTSnapshot(nil), snapshot.SUTs...)
	sort.SliceStable(data.SUTs, func(i, j int) bool { return data.SUTs[i].ID < data.SUTs[j].ID })

	data.Coverage = snapshot.Coverage
	data.Coverage.Gaps = append([]delivery.CoverageGapSnapshot(nil), snapshot.Coverage.Gaps...)
	sort.SliceStable(data.Coverage.Gaps, func(i, j int) bool {
		left := data.Coverage.Gaps[i]
		right := data.Coverage.Gaps[j]
		if left.RequirementID != right.RequirementID {
			return left.RequirementID < right.RequirementID
		}
		if left.CaseVersionID != right.CaseVersionID {
			return left.CaseVersionID < right.CaseVersionID
		}
		return left.Reason < right.Reason
	})

	data.NarrativeLines = narrativeLines(narrative)
	data.HasNarrative = len(data.NarrativeLines) > 0
	data.Summary = summarize(snapshot)
	return data
}

// buildSectionSwitches renders the v2 appendix section-switch list in the
// frozen order (summary, results, issues, coverage, evidence, appendix).
func buildSectionSwitches(opts delivery.SectionOptions) []sectionSwitch {
	state := func(enabled bool) string {
		if enabled {
			return "开"
		}
		return "关"
	}
	return []sectionSwitch{
		{Label: "概览", State: state(opts.Summary), Enabled: opts.Summary},
		{Label: "结果", State: state(opts.Results), Enabled: opts.Results},
		{Label: "问题", State: state(opts.Issues), Enabled: opts.Issues},
		{Label: "覆盖", State: state(opts.Coverage), Enabled: opts.Coverage},
		{Label: "证据", State: state(opts.Evidence), Enabled: opts.Evidence},
		{Label: "附录", State: state(opts.Appendix), Enabled: opts.Appendix},
	}
}

func summarize(snapshot delivery.ReportSnapshot) summaryView {
	summary := summaryView{
		Issues:       len(snapshot.Issues),
		Waves:        len(snapshot.Waves),
		Requirements: len(snapshot.Requirements),
	}
	for _, run := range snapshot.CaseRuns {
		summary.Total++
		switch run.Status {
		case delivery.ReportRunStatusPassed:
			summary.Passed++
		case delivery.ReportRunStatusFailed:
			summary.Failed++
		case delivery.ReportRunStatusBlocked:
			summary.Blocked++
		case delivery.ReportRunStatusInconclusive:
			summary.Inconclusive++
		case delivery.ReportRunStatusSkipped:
			summary.Skipped++
		default:
			summary.Other++
		}
		summary.Evidence += len(run.Evidence)
	}
	for _, issue := range snapshot.Issues {
		if issue.Status == delivery.IssueStatusOpen {
			summary.OpenIssues++
		}
	}
	for _, requirement := range snapshot.Requirements {
		if requirement.Status == string(version.RequirementClosed) {
			summary.RequirementsClosed++
		}
	}
	return summary
}

func buildSUTRows(waves []delivery.WaveSnapshot, suts []delivery.SUTSnapshot) []sutRow {
	labels := make(map[int64]string, len(suts))
	for _, sut := range suts {
		labels[sut.ID] = labelSUT(sut)
	}
	var rows []sutRow
	for _, wave := range waves {
		for _, counts := range wave.BySUT {
			label := labels[counts.SUTID]
			if label == "" {
				label = fmt.Sprintf("版本 SUT #%d", counts.SUTID)
			}
			rows = append(rows, sutRow{
				WaveID:       wave.ID,
				SUTID:        counts.SUTID,
				Label:        label,
				Total:        counts.Total,
				Passed:       counts.Passed,
				Failed:       counts.Failed,
				Blocked:      counts.Blocked,
				Inconclusive: counts.Inconclusive,
			})
		}
	}
	return rows
}

func labelSUT(sut delivery.SUTSnapshot) string {
	switch {
	case sut.SUTName != "" && sut.EnvironmentName != "":
		return sut.SUTName + " @ " + sut.EnvironmentName
	case sut.SUTName != "":
		return sut.SUTName
	default:
		return sut.EnvironmentName
	}
}

func buildEvidenceRows(caseRuns []delivery.CaseRunSnapshot) []delivery.EvidenceSnapshot {
	var rows []delivery.EvidenceSnapshot
	for _, run := range caseRuns {
		rows = append(rows, run.Evidence...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func narrativeLines(narrative string) []string {
	if narrative == "" {
		return nil
	}
	narrative = strings.ReplaceAll(narrative, "\r\n", "\n")
	narrative = strings.ReplaceAll(narrative, "\r", "\n")
	return strings.Split(narrative, "\n")
}

// truncationState records the original and kept sizes of the truncation
// candidates so the report can state exactly what was reduced (spec §6.2: no
// silent drop). Kept starts at the full size and decreases only for a list that
// was actually sliced, so the note never mentions untouched or disabled lists.
type truncationState struct {
	issuesTotal       int
	issuesKept        int
	wavesTotal        int
	wavesKept         int
	caseRunsTotal     int
	caseRunsKept      int
	explanationsTotal int
	explanationsKept  int
	judgmentsTotal    int
	judgmentsKept     int
}

func (s *truncationState) note() string {
	parts := make([]string, 0, 5)
	if s.issuesKept < s.issuesTotal {
		parts = append(parts, fmt.Sprintf("问题 %d → %d", s.issuesTotal, s.issuesKept))
	}
	if s.wavesKept < s.wavesTotal {
		parts = append(parts, fmt.Sprintf("波次 %d → %d", s.wavesTotal, s.wavesKept))
	}
	if s.caseRunsKept < s.caseRunsTotal {
		parts = append(parts, fmt.Sprintf("执行结果 %d → %d", s.caseRunsTotal, s.caseRunsKept))
	}
	if s.explanationsKept < s.explanationsTotal {
		parts = append(parts, fmt.Sprintf("失败说明 %d → %d", s.explanationsTotal, s.explanationsKept))
	}
	if s.judgmentsKept < s.judgmentsTotal {
		parts = append(parts, fmt.Sprintf("裁判事实 %d → %d", s.judgmentsTotal, s.judgmentsKept))
	}
	if len(parts) == 0 {
		return ""
	}
	return "为满足 2 MiB 上限，已逐步减半并保留 ID 较小的条目。当前保留：" +
		strings.Join(parts, "；") + "。"
}

// snapshotTruncationNote renders the visible §6.1 input-cap note. Only the
// sections named by the snapshot are mentioned; unknown reader-provided markers
// are still surfaced verbatim so no truncation is silent.
func snapshotTruncationNote(sections []string) string {
	if len(sections) == 0 {
		return ""
	}
	labels := map[string]string{
		delivery.ReportTruncationIssues:   fmt.Sprintf("问题（≤%d）", delivery.MaxReportIssues),
		delivery.ReportTruncationWaves:    fmt.Sprintf("波次（≤%d）", delivery.MaxReportWaves),
		delivery.ReportTruncationCaseRuns: fmt.Sprintf("执行结果（≤%d）", delivery.MaxReportCaseRuns),
	}
	parts := make([]string, 0, len(sections))
	seen := make(map[string]bool, len(sections))
	for _, name := range []string{
		delivery.ReportTruncationIssues,
		delivery.ReportTruncationWaves,
		delivery.ReportTruncationCaseRuns,
	} {
		for _, candidate := range sections {
			if candidate == name && !seen[name] {
				seen[name] = true
				parts = append(parts, labels[name])
			}
		}
	}
	for _, name := range sections {
		if !seen[name] {
			seen[name] = true
			parts = append(parts, name)
		}
	}
	return "报告输入超过规模上限，已按 ID 升序仅保留前 N 条：" + strings.Join(parts, "；") + "。"
}

// joinTruncationNotes combines the input-cap note with the 2 MiB truncation
// note without repeating either part across the iterative halving passes.
func joinTruncationNotes(snapshotNote, sizeNote string) string {
	switch {
	case snapshotNote == "":
		return sizeNote
	case sizeNote == "":
		return snapshotNote
	default:
		return snapshotNote + " " + sizeNote
	}
}

func formatInt64Ptr(value *int64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatInt(*value, 10)
}

func formatIntPtr(value *int) string {
	if value == nil {
		return "—"
	}
	return strconv.Itoa(*value)
}

func formatTimePtr(value *time.Time) string {
	if value == nil {
		return "—"
	}
	return formatTime(*value)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.UTC().Format("2006-01-02 15:04:05 UTC")
}
