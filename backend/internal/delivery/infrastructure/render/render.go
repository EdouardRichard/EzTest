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
)

const (
	// TemplateVersionV1 is the only built-in report template version (spec
	// §6.2). The setting report.template_version is fixed to this value in P4.
	TemplateVersionV1 = "1"

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

// NewHTMLRenderer parses the built-in report template. templateVersion only
// accepts "1" (spec §6.2); any other value is an error because P4 does not
// support template versioning or user templates.
func NewHTMLRenderer(templateVersion string) (*HTMLRenderer, error) {
	if templateVersion != TemplateVersionV1 {
		return nil, fmt.Errorf("render: unsupported report template version %q, only %q is built in", templateVersion, TemplateVersionV1)
	}
	parsed, err := template.New("report").Funcs(template.FuncMap{
		"int64ptr": formatInt64Ptr,
		"intptr":   formatIntPtr,
		"timeptr":  formatTimePtr,
	}).Parse(reportTemplate)
	if err != nil {
		return nil, fmt.Errorf("render: parse report template: %w", err)
	}
	return &HTMLRenderer{templateVersion: templateVersion, template: parsed}, nil
}

// Render renders one report revision from the deterministic snapshot and the
// optional plain-text narrative. Narrative text is escaped as plain text and
// split into preserved lines; it can never inject HTML. When the rendered HTML
// exceeds MaxHTMLBytes, issues, then waves, then case runs are halved in order
// and a visible truncation note is added; if the document still exceeds the
// limit, Render returns an error instead of silently dropping content.
func (r *HTMLRenderer) Render(ctx context.Context, snapshot delivery.ReportSnapshot, narrative string, opts delivery.SectionOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.template == nil {
		return nil, errors.New("render: report renderer is not initialized")
	}
	data := newRenderData(snapshot, narrative, opts)
	data.TemplateVersion = r.templateVersion
	output, err := r.render(data)
	if err != nil {
		return nil, err
	}
	if len(output) <= MaxHTMLBytes {
		return output, nil
	}

	state := &truncationState{
		issuesTotal:   len(data.Issues),
		wavesTotal:    len(data.Waves),
		caseRunsTotal: len(data.CaseRuns),
	}
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
	return nil, fmt.Errorf("render: report HTML is %d bytes, still above the %d-byte limit after truncating issues, waves and case runs", len(output), MaxHTMLBytes)
}

func (r *HTMLRenderer) renderTruncated(data *renderData, state *truncationState) ([]byte, error) {
	data.Truncated = true
	data.TruncationNote = state.note()
	return r.render(data)
}

func (r *HTMLRenderer) render(data *renderData) ([]byte, error) {
	view := *data
	view.Issues = make([]delivery.IssueSnapshot, 0, len(data.Issues))
	for index := len(data.Issues) - 1; index >= 0; index-- {
		view.Issues = append(view.Issues, data.Issues[index])
	}
	view.SUTRows = buildSUTRows(data.Waves, data.VersionSUTs)
	view.EvidenceRows = buildEvidenceRows(data.CaseRuns)

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
	Sections        delivery.SectionOptions
	Requirements    []delivery.RequirementSnapshot
	Waves           []delivery.WaveSnapshot
	CaseRuns        []delivery.CaseRunSnapshot
	Issues          []delivery.IssueSnapshot
	VersionSUTs     []delivery.VersionSUTSnapshot
	Coverage        delivery.CoverageSnapshot
	SUTRows         []sutRow
	EvidenceRows    []delivery.EvidenceSnapshot
	Summary         summaryView
	NarrativeLines  []string
	HasNarrative    bool
	Truncated       bool
	TruncationNote  string
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

// sutRow is one wave × version SUT result row.
type sutRow struct {
	WaveID       int64
	VersionSUTID int64
	Label        string
	Total        int
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
}

func newRenderData(snapshot delivery.ReportSnapshot, narrative string, opts delivery.SectionOptions) *renderData {
	data := &renderData{
		TemplateVersion: TemplateVersionV1,
		VersionID:       snapshot.VersionID,
		GeneratedAt:     formatTime(snapshot.GeneratedAt),
		Sections:        opts,
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
		byVersionSUT := make([]delivery.SUTCountsSnapshot, len(data.Waves[index].ByVersionSUT))
		copy(byVersionSUT, data.Waves[index].ByVersionSUT)
		sort.SliceStable(byVersionSUT, func(i, j int) bool {
			return byVersionSUT[i].VersionSUTID < byVersionSUT[j].VersionSUTID
		})
		data.Waves[index].ByVersionSUT = byVersionSUT
	}
	sort.SliceStable(data.Waves, func(i, j int) bool { return data.Waves[i].ID < data.Waves[j].ID })

	data.CaseRuns = make([]delivery.CaseRunSnapshot, len(snapshot.CaseRuns))
	copy(data.CaseRuns, snapshot.CaseRuns)
	for index := range data.CaseRuns {
		versionSUTIDs := make([]int64, len(data.CaseRuns[index].VersionSUTIDs))
		copy(versionSUTIDs, data.CaseRuns[index].VersionSUTIDs)
		sort.Slice(versionSUTIDs, func(i, j int) bool { return versionSUTIDs[i] < versionSUTIDs[j] })
		data.CaseRuns[index].VersionSUTIDs = versionSUTIDs

		evidence := make([]delivery.EvidenceSnapshot, len(data.CaseRuns[index].Evidence))
		copy(evidence, data.CaseRuns[index].Evidence)
		sort.SliceStable(evidence, func(i, j int) bool { return evidence[i].ID < evidence[j].ID })
		data.CaseRuns[index].Evidence = evidence
	}
	sort.SliceStable(data.CaseRuns, func(i, j int) bool { return data.CaseRuns[i].ID < data.CaseRuns[j].ID })

	// Issues are kept in ascending ID order while truncating (so "first N by
	// id" is deterministic) and reversed only for display (spec §6.1).
	data.Issues = append([]delivery.IssueSnapshot(nil), snapshot.Issues...)
	sort.SliceStable(data.Issues, func(i, j int) bool { return data.Issues[i].ID < data.Issues[j].ID })

	data.VersionSUTs = append([]delivery.VersionSUTSnapshot(nil), snapshot.VersionSUTs...)
	sort.SliceStable(data.VersionSUTs, func(i, j int) bool { return data.VersionSUTs[i].ID < data.VersionSUTs[j].ID })

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

func summarize(snapshot delivery.ReportSnapshot) summaryView {
	summary := summaryView{
		Issues:       len(snapshot.Issues),
		Waves:        len(snapshot.Waves),
		Requirements: len(snapshot.Requirements),
	}
	for _, run := range snapshot.CaseRuns {
		summary.Total++
		switch run.Status {
		case "passed":
			summary.Passed++
		case "failed":
			summary.Failed++
		case "blocked":
			summary.Blocked++
		case "inconclusive":
			summary.Inconclusive++
		case "skipped":
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
		if requirement.Status == "closed" {
			summary.RequirementsClosed++
		}
	}
	return summary
}

func buildSUTRows(waves []delivery.WaveSnapshot, versionSUTs []delivery.VersionSUTSnapshot) []sutRow {
	labels := make(map[int64]string, len(versionSUTs))
	for _, sut := range versionSUTs {
		labels[sut.ID] = labelVersionSUT(sut)
	}
	var rows []sutRow
	for _, wave := range waves {
		for _, counts := range wave.ByVersionSUT {
			label := labels[counts.VersionSUTID]
			if label == "" {
				label = fmt.Sprintf("版本 SUT #%d", counts.VersionSUTID)
			}
			rows = append(rows, sutRow{
				WaveID:       wave.ID,
				VersionSUTID: counts.VersionSUTID,
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

func labelVersionSUT(sut delivery.VersionSUTSnapshot) string {
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

// truncationState records the original and kept sizes of the truncated lists so
// the report can state exactly what was reduced (spec §6.2: no silent drop).
type truncationState struct {
	issuesTotal   int
	issuesKept    int
	wavesTotal    int
	wavesKept     int
	caseRunsTotal int
	caseRunsKept  int
}

func (s *truncationState) note() string {
	parts := make([]string, 0, 3)
	if s.issuesKept < s.issuesTotal {
		parts = append(parts, fmt.Sprintf("问题 %d → %d", s.issuesTotal, s.issuesKept))
	}
	if s.wavesKept < s.wavesTotal {
		parts = append(parts, fmt.Sprintf("波次 %d → %d", s.wavesTotal, s.wavesKept))
	}
	if s.caseRunsKept < s.caseRunsTotal {
		parts = append(parts, fmt.Sprintf("执行结果 %d → %d", s.caseRunsTotal, s.caseRunsKept))
	}
	if len(parts) == 0 {
		return ""
	}
	return "为满足 2 MiB 上限，已按 issues → waves → case_runs 顺序逐步减半并保留 ID 较小的条目。当前保留：" +
		strings.Join(parts, "；") + "。"
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
