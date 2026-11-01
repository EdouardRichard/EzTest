package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

var renderTime = time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

func allSections() delivery.SectionOptions {
	return delivery.SectionOptions{Summary: true, Results: true, Issues: true, Coverage: true, Evidence: true, Appendix: true}
}

func newTestRenderer(t *testing.T) *HTMLRenderer {
	t.Helper()
	renderer, err := NewHTMLRenderer(TemplateVersionV1)
	if err != nil {
		t.Fatalf("NewHTMLRenderer(%q): %v", TemplateVersionV1, err)
	}
	if renderer == nil {
		t.Fatal("NewHTMLRenderer returned a nil renderer")
	}
	return renderer
}

// newVersionedRenderer builds one built-in template version under test.
func newVersionedRenderer(t *testing.T, version string) *HTMLRenderer {
	t.Helper()
	renderer, err := NewHTMLRenderer(version)
	if err != nil {
		t.Fatalf("NewHTMLRenderer(%q): %v", version, err)
	}
	if renderer == nil {
		t.Fatalf("NewHTMLRenderer(%q) returned a nil renderer", version)
	}
	return renderer
}

// sampleSnapshot exercises every report section with fixed, deterministic data.
func sampleSnapshot() delivery.ReportSnapshot {
	waveID := int64(12)
	versionSUTID := int64(31)
	requirementVersionID := int64(101)
	closureID := int64(55)
	reportID := int64(70)
	return delivery.ReportSnapshot{
		Kind:             delivery.ReportKindWave,
		VersionID:        7,
		WaveID:           &waveID,
		ReportID:         70,
		ReportRevision:   3,
		InputFingerprint: strings.Repeat("b", 64),
		Requirements: []delivery.RequirementSnapshot{
			{ID: 2, VersionID: 7, CurrentVersionID: &requirementVersionID, Title: "需求二", Status: "closed", ClosureID: &closureID, ClosedAt: &renderTime, CloseRound: 1, UpdatedAt: renderTime},
			{ID: 1, VersionID: 7, Title: "需求一", Status: "clarified", UpdatedAt: renderTime},
		},
		Waves: []delivery.WaveSnapshot{
			{
				ID: 12, VersionID: 7, Status: "completed", Trigger: "manual",
				Counts:       delivery.WaveCountsSnapshot{Total: 2, Passed: 1, Failed: 1, Evidence: 1},
				ByVersionSUT: []delivery.SUTCountsSnapshot{{VersionSUTID: versionSUTID, Total: 2, Passed: 1, Failed: 1}},
				ReportID:     &reportID, StartedAt: &renderTime, FinishedAt: &renderTime,
			},
			{ID: 11, VersionID: 7, Status: "completed", Trigger: "schedule", Counts: delivery.WaveCountsSnapshot{Total: 1, Passed: 1}, FinishedAt: &renderTime},
		},
		CaseRuns: []delivery.CaseRunSnapshot{
			{
				ID: 21, VersionID: 7, WaveID: 12, CaseVersionID: 201, CaseTitle: "用例二十一",
				VersionSUTID: &versionSUTID, VersionSUTIDs: []int64{32, 31},
				Status: "failed", FailureClass: "application", Summary: "断言失败",
				StepCount: 3, StepFailedCount: 1, AssertionCount: 2, AssertionFailedCount: 1,
				FailedStepSeq: 2, FailedAssertionSeq: 1, Category: "api", Severity: "major",
				Evidence: []delivery.EvidenceSnapshot{
					{ID: 41, CaseRunID: 21, VersionSUTID: &versionSUTID, StepSeq: intPtrValue(2), Kind: "screenshot", SHA256: strings.Repeat("a", 64), SizeBytes: 100},
				},
				StartedAt: &renderTime, FinishedAt: &renderTime, CreatedAt: renderTime,
			},
			{ID: 20, VersionID: 7, WaveID: 12, CaseVersionID: 200, CaseTitle: "用例二十", Status: "passed", CreatedAt: renderTime},
		},
		Issues: []delivery.IssueSnapshot{
			{ID: 1, VersionID: 7, Title: "问题一", Status: delivery.IssueStatusConfirmed, Category: "ui", Severity: "minor", Source: "analysis", CreatedAt: renderTime, UpdatedAt: renderTime},
			{ID: 3, VersionID: 7, Title: "问题三", Status: delivery.IssueStatusOpen, Category: "api", Severity: "major", Source: "analysis", SourceIssueIDs: []int64{4, 5}, CaseRunIDs: []int64{21}, CreatedAt: renderTime, UpdatedAt: renderTime},
		},
		VersionSUTs: []delivery.VersionSUTSnapshot{
			{ID: 31, SUTID: 300, SUTName: "API", EnvironmentID: 8, EnvironmentName: "staging"},
		},
		Coverage: delivery.CoverageSnapshot{
			RequirementsTotal: 2, RequirementsCovered: 1, CaseVersionsTotal: 3, CaseVersionsCovered: 2,
			Gaps: []delivery.CoverageGapSnapshot{{RequirementID: 2, CaseVersionID: 202, Reason: "no_run"}},
		},
		GeneratedAt: renderTime,
	}
}

func intPtrValue(value int) *int { return &value }

func mustRender(t *testing.T, snapshot delivery.ReportSnapshot, narrative string, opts delivery.SectionOptions) []byte {
	t.Helper()
	output, err := newTestRenderer(t).Render(context.Background(), snapshot, narrative, opts)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(output) == 0 {
		t.Fatal("Render returned no HTML")
	}
	return output
}

// truncationNote extracts the visible truncation note text so a test can assert
// exactly which lists the renderer reported as truncated.
func truncationNote(t *testing.T, output []byte) string {
	t.Helper()
	const marker = `<aside class="truncation" data-truncated="true"><strong>报告截断说明</strong><p>`
	text := string(output)
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatal("rendered HTML has no visible truncation note")
	}
	start += len(marker)
	end := strings.Index(text[start:], "</p></aside>")
	if end < 0 {
		t.Fatal("rendered truncation note is not closed")
	}
	return text[start : start+end]
}

func TestRenderRejectsUnknownTemplateVersion(t *testing.T) {
	for _, version := range []string{"", "3", "latest", "1 ", "2.0"} {
		renderer, err := NewHTMLRenderer(version)
		if err == nil {
			t.Fatalf("NewHTMLRenderer(%q) = %v, want an error", version, renderer)
		}
		if renderer != nil {
			t.Fatalf("NewHTMLRenderer(%q) returned a non-nil renderer on error", version)
		}
	}
}

// v1GoldenSHA256 pins the P4 template v1 output byte-for-byte. Version 2 must
// never change the v1 document (spec P7-D-03), so any difference here is a
// regression even if the visible content looks equivalent.
const v1GoldenSHA256 = "03348b0015f9e80b46d0308255762bdd99db41c83b0a927b80ed15bf0e56c033"

// TestRenderV1GoldenHash is the byte-identity guard of the frozen v1 template.
// Go's template parser normalizes the raw string's CRLF checkout form to LF; the
// hash is also taken over LF-normalized bytes so the guard is platform
// independent either way.
func TestRenderV1GoldenHash(t *testing.T) {
	renderer := newTestRenderer(t)
	output, err := renderer.Render(context.Background(), sampleSnapshot(), "固定叙述\n第二行", allSections())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	normalized := bytes.ReplaceAll(output, []byte("\r\n"), []byte("\n"))
	sum := sha256.Sum256(normalized)
	if got := hex.EncodeToString(sum[:]); got != v1GoldenSHA256 {
		t.Fatalf("v1 golden hash changed: got %s (%d bytes)", got, len(output))
	}
}

// TestRenderV1IgnoresAppendixOption proves v1 stays byte-identical even when
// the composition root enables the (v2-only) appendix section.
func TestRenderV1IgnoresAppendixOption(t *testing.T) {
	renderer := newTestRenderer(t)
	enabled := allSections()
	disabled := allSections()
	disabled.Appendix = false
	withAppendix, err := renderer.Render(context.Background(), sampleSnapshot(), "叙述", enabled)
	if err != nil {
		t.Fatalf("Render with Appendix: %v", err)
	}
	without, err := renderer.Render(context.Background(), sampleSnapshot(), "叙述", disabled)
	if err != nil {
		t.Fatalf("Render without Appendix: %v", err)
	}
	if !bytes.Equal(withAppendix, without) {
		t.Fatal("v1 output changed when SectionOptions.Appendix was enabled")
	}
	if strings.Contains(string(withAppendix), `id="appendix"`) {
		t.Fatal("v1 rendered a v2 appendix section")
	}
}

// TestRenderV2AppendsMetadataSection covers the v2 appendix: it records the
// report metadata and section switches and explicitly states that the appendix
// never changes deterministic statistics (spec P7-D-03).
func TestRenderV2AppendsMetadataSection(t *testing.T) {
	renderer := newVersionedRenderer(t, TemplateVersionV2)
	output, err := renderer.Render(context.Background(), sampleSnapshot(), "叙述", allSections())
	if err != nil {
		t.Fatalf("Render v2: %v", err)
	}
	text := string(output)
	for _, want := range []string{
		`data-template-version="2"`,
		`<section id="appendix" data-section="appendix">`,
		"附录",
		"报告 ID",
		"#70",
		"报告版本",
		"r3",
		"版本",
		"#7",
		"波次",
		"#12",
		"as_of",
		"输入指纹",
		strings.Repeat("b", 64),
		"模板版本",
		"生成时间",
		"章节开关",
		"本附录由系统生成，不改变确定性统计。",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("v2 appendix is missing %q", want)
		}
	}
	if index := strings.Index(text, `id="appendix"`); index < 0 || index > strings.Index(text, "<footer>") {
		t.Fatal("v2 appendix is not rendered before the footer/main end")
	}
}

// TestRenderV2AppendixCanBeDisabled covers report.section_appendix=false: the
// template version stays 2 but no appendix section is emitted.
func TestRenderV2AppendixCanBeDisabled(t *testing.T) {
	renderer := newVersionedRenderer(t, TemplateVersionV2)
	opts := allSections()
	opts.Appendix = false
	output, err := renderer.Render(context.Background(), sampleSnapshot(), "叙述", opts)
	if err != nil {
		t.Fatalf("Render v2: %v", err)
	}
	text := string(output)
	if strings.Contains(text, `id="appendix"`) || strings.Contains(text, `data-section="appendix"`) {
		t.Fatal("v2 rendered the appendix while report.section_appendix was disabled")
	}
	if !strings.Contains(text, `data-template-version="2"`) {
		t.Fatal("disabled appendix removed the v2 template marker")
	}
	// The deterministic sections are untouched by the toggle.
	if !strings.Contains(text, `data-section="summary"`) || !strings.Contains(text, `data-section="issues"`) {
		t.Fatal("disabled appendix changed the deterministic sections")
	}
}

// TestRenderV2EmptySnapshotIsValid covers a v2 render before any report
// metadata is available: the appendix still renders with placeholders.
func TestRenderV2EmptySnapshotIsValid(t *testing.T) {
	renderer := newVersionedRenderer(t, TemplateVersionV2)
	output, err := renderer.Render(context.Background(), delivery.ReportSnapshot{
		Kind: delivery.ReportKindVersion, VersionID: 9, GeneratedAt: renderTime,
	}, "", allSections())
	if err != nil {
		t.Fatalf("Render v2 empty snapshot: %v", err)
	}
	if !strings.Contains(string(output), `id="appendix"`) {
		t.Fatal("v2 empty snapshot is missing its appendix")
	}
}

func TestRenderProducesSelfContainedDocument(t *testing.T) {
	output := mustRender(t, sampleSnapshot(), "", allSections())
	if !bytes.HasPrefix(output, []byte("<!doctype html>")) {
		t.Fatalf("output does not start with <!doctype html>: %.40q", output)
	}
	text := string(output)
	if !strings.Contains(text, "<style>") {
		t.Fatal("output has no inline style block")
	}
	if !strings.Contains(text, `data-template-version="1"`) {
		t.Fatal("output does not record its template version")
	}
	for _, external := range []string{"http://", "https://", "src=", "@import", "<link"} {
		if strings.Contains(text, external) {
			t.Fatalf("output references an external asset (%q)", external)
		}
	}
}

func TestRenderEscapesUserControlledText(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.Requirements[0].Title = `<script>alert("requirement")</script>`
	snapshot.Waves[0].Trigger = `<script>alert("wave")</script>`
	snapshot.CaseRuns[0].CaseTitle = `<script>alert("case")</script>`
	snapshot.CaseRuns[0].Summary = `a & b <b>bold</b>`
	snapshot.Issues[1].Title = `<script>alert("issue")</script>`
	snapshot.Coverage.Gaps[0].Reason = `<script>alert("gap")</script>`
	narrative := `<script>alert("narrative")</script>` + "\n第二行 & <b>bold</b>"
	output := mustRender(t, snapshot, narrative, allSections())
	text := string(output)

	if strings.Contains(text, "<script>") {
		t.Fatal("rendered HTML contains an unescaped <script> tag")
	}
	if strings.Contains(text, "<b>bold</b>") {
		t.Fatal("rendered HTML contains an unescaped <b> tag")
	}
	for _, want := range []string{
		"&lt;script&gt;", "alert(&#34;requirement&#34;)", "alert(&#34;wave&#34;)",
		"alert(&#34;case&#34;)", "alert(&#34;issue&#34;)", "alert(&#34;narrative&#34;)",
		"&lt;b&gt;bold&lt;/b&gt;", "&amp;",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered HTML is missing escaped text %q", want)
		}
	}
	if !strings.Contains(text, `data-narrative="candidate"`) {
		t.Fatal("narrative is not rendered as the candidate AI narrative")
	}
	if !strings.Contains(text, "第二行") {
		t.Fatal("narrative second line is missing")
	}
	if got := strings.Count(text, `class="narrative-line"`); got != 2 {
		t.Fatalf("narrative lines = %d, want 2", got)
	}
	if strings.Index(text, `alert(&#34;narrative&#34;)`) > strings.Index(text, "第二行") {
		t.Fatal("narrative line order is not preserved")
	}
}

func TestRenderSectionToggles(t *testing.T) {
	snapshot := sampleSnapshot()
	all := mustRender(t, snapshot, "叙述内容", allSections())
	for _, marker := range []string{
		`data-section="summary"`, `data-section="results"`, `data-section="issues"`,
		`data-section="coverage"`, `data-section="evidence"`,
	} {
		if !strings.Contains(string(all), marker) {
			t.Fatalf("all-sections output is missing %s", marker)
		}
	}

	disabled := mustRender(t, snapshot, "叙述内容", delivery.SectionOptions{Summary: true, Results: true})
	for _, marker := range []string{`data-section="issues"`, `data-section="coverage"`, `data-section="evidence"`} {
		if strings.Contains(string(disabled), marker) {
			t.Fatalf("disabled-section output still contains %s", marker)
		}
	}
	for _, marker := range []string{`data-section="summary"`, `data-section="results"`} {
		if !strings.Contains(string(disabled), marker) {
			t.Fatalf("enabled-section output is missing %s", marker)
		}
	}

	noSummary := mustRender(t, snapshot, "不应出现的叙述", delivery.SectionOptions{Results: true, Issues: true, Coverage: true, Evidence: true})
	if strings.Contains(string(noSummary), `data-section="summary"`) {
		t.Fatal("summary section rendered while disabled")
	}
	if strings.Contains(string(noSummary), "不应出现的叙述") {
		t.Fatal("narrative rendered while the summary section is disabled")
	}
	if !strings.Contains(string(noSummary), `data-section="issues"`) {
		t.Fatal("issues section missing while enabled")
	}
}

func TestRenderEmptySnapshotIsValid(t *testing.T) {
	waveID := int64(3)
	for _, snapshot := range []delivery.ReportSnapshot{
		{Kind: delivery.ReportKindVersion, VersionID: 9, GeneratedAt: renderTime},
		{Kind: delivery.ReportKindWave, VersionID: 9, WaveID: &waveID, GeneratedAt: renderTime},
	} {
		output := mustRender(t, snapshot, "", allSections())
		text := string(output)
		if !strings.HasPrefix(text, "<!doctype html>") {
			t.Fatalf("empty snapshot output does not start with <!doctype html>")
		}
		if !strings.Contains(text, "无执行结果") {
			t.Fatalf("empty snapshot output has no empty-results marker")
		}
		if !strings.Contains(text, `data-section="issues"`) || !strings.Contains(text, "无问题") {
			t.Fatalf("empty snapshot output has no empty-issues marker")
		}
		if len(output) > 64*1024 {
			t.Fatalf("empty snapshot rendered %d bytes", len(output))
		}
	}
}

func TestRenderTruncatesOversizedReport(t *testing.T) {
	base := delivery.ReportSnapshot{Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: renderTime}
	renderer := newTestRenderer(t)

	t.Run("issues", func(t *testing.T) {
		snapshot := base
		const total = 4000
		for id := int64(1); id <= total; id++ {
			snapshot.Issues = append(snapshot.Issues, delivery.IssueSnapshot{
				ID: id, VersionID: 7, Title: strings.Repeat("x", 900),
				Status: delivery.IssueStatusOpen, CreatedAt: renderTime,
			})
		}
		output, err := renderer.Render(context.Background(), snapshot, "", allSections())
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(output) > MaxHTMLBytes {
			t.Fatalf("truncated output = %d bytes, want <= %d", len(output), MaxHTMLBytes)
		}
		text := string(output)
		if !strings.Contains(text, `data-truncated="true"`) || !strings.Contains(text, "报告截断说明") {
			t.Fatal("truncated output has no visible truncation note")
		}
		if !strings.Contains(text, "问题 4000 → ") {
			t.Fatal("truncation note does not report the truncated issues counts")
		}
		kept := strings.Count(text, `<tr data-issue-id="`)
		if kept == 0 || kept >= total {
			t.Fatalf("issues kept after truncation = %d, want between 1 and %d", kept, total-1)
		}
	})

	t.Run("waves", func(t *testing.T) {
		snapshot := base
		const total = 2000
		for id := int64(1); id <= total; id++ {
			snapshot.Waves = append(snapshot.Waves, delivery.WaveSnapshot{
				ID: id, VersionID: 7, Status: "completed", Trigger: strings.Repeat("w", 1200),
			})
		}
		output, err := renderer.Render(context.Background(), snapshot, "", allSections())
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(output) > MaxHTMLBytes {
			t.Fatalf("truncated output = %d bytes, want <= %d", len(output), MaxHTMLBytes)
		}
		text := string(output)
		if !strings.Contains(text, `data-truncated="true"`) || !strings.Contains(text, "报告截断说明") {
			t.Fatal("truncated output has no visible truncation note")
		}
		if !strings.Contains(text, "波次 2000 → ") {
			t.Fatal("truncation note does not report the truncated waves counts")
		}
		kept := strings.Count(text, `<tr data-wave-id="`)
		if kept == 0 || kept >= total {
			t.Fatalf("waves kept after truncation = %d, want between 1 and %d", kept, total-1)
		}
	})

	t.Run("case_runs", func(t *testing.T) {
		snapshot := base
		const total = 8000
		for id := int64(1); id <= total; id++ {
			snapshot.CaseRuns = append(snapshot.CaseRuns, delivery.CaseRunSnapshot{
				ID: id, VersionID: 7, WaveID: 12, CaseVersionID: id, CaseTitle: strings.Repeat("c", 250),
				Status: "failed", FailureClass: "application", CreatedAt: renderTime,
			})
		}
		output, err := renderer.Render(context.Background(), snapshot, "", allSections())
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(output) > MaxHTMLBytes {
			t.Fatalf("truncated output = %d bytes, want <= %d", len(output), MaxHTMLBytes)
		}
		text := string(output)
		if !strings.Contains(text, `data-truncated="true"`) || !strings.Contains(text, "报告截断说明") {
			t.Fatal("truncated output has no visible truncation note")
		}
		if !strings.Contains(text, "执行结果 8000 → ") {
			t.Fatal("truncation note does not report the truncated case-runs counts")
		}
		kept := strings.Count(text, `<tr data-case-run-id="`)
		if kept == 0 || kept >= total {
			t.Fatalf("case runs kept after truncation = %d, want between 1 and %d", kept, total-1)
		}
	})
}

// oversizedIssues returns a single list that is large enough to cross the 2 MiB
// limit on its own, so tests can force truncation through one known list.
func oversizedIssues(total int) []delivery.IssueSnapshot {
	issues := make([]delivery.IssueSnapshot, 0, total)
	for id := int64(1); id <= int64(total); id++ {
		issues = append(issues, delivery.IssueSnapshot{
			ID: id, VersionID: 7, Title: strings.Repeat("x", 900),
			Status: delivery.IssueStatusOpen, CreatedAt: renderTime,
		})
	}
	return issues
}

// oversizedWaves mirrors oversizedIssues for the waves list.
func oversizedWaves(total int) []delivery.WaveSnapshot {
	waves := make([]delivery.WaveSnapshot, 0, total)
	for id := int64(1); id <= int64(total); id++ {
		waves = append(waves, delivery.WaveSnapshot{
			ID: id, VersionID: 7, Status: "completed", Trigger: strings.Repeat("w", 1200),
		})
	}
	return waves
}

// TestRenderTruncationNoteOnlyReportsSlicedLists is the R1 regression test:
// when only the issues list is sliced, untouched waves and case runs keep their
// full counts, their rows all survive, and the note mentions only issues.
func TestRenderTruncationNoteOnlyReportsSlicedLists(t *testing.T) {
	snapshot := delivery.ReportSnapshot{
		Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: renderTime,
		Issues: oversizedIssues(4000),
		Waves: []delivery.WaveSnapshot{
			{ID: 1, VersionID: 7, Status: "completed", Trigger: "manual"},
			{ID: 2, VersionID: 7, Status: "completed", Trigger: "schedule"},
		},
		CaseRuns: []delivery.CaseRunSnapshot{
			{ID: 1, VersionID: 7, WaveID: 1, CaseVersionID: 11, CaseTitle: "用例一", Status: "passed", CreatedAt: renderTime},
			{ID: 2, VersionID: 7, WaveID: 2, CaseVersionID: 12, CaseTitle: "用例二", Status: "failed", CreatedAt: renderTime},
		},
	}
	output := mustRender(t, snapshot, "", allSections())
	note := truncationNote(t, output)
	if !strings.Contains(note, "问题 4000 → ") {
		t.Fatalf("truncation note does not report the sliced issues: %q", note)
	}
	if strings.Contains(note, "波次") {
		t.Fatalf("truncation note reports untouched waves: %q", note)
	}
	if strings.Contains(note, "执行结果") {
		t.Fatalf("truncation note reports untouched case runs: %q", note)
	}
	text := string(output)
	if got, want := strings.Count(text, `<tr data-case-run-id="`), len(snapshot.CaseRuns); got != want {
		t.Fatalf("case-run rows after truncation = %d, want all %d to survive", got, want)
	}
	if got, want := strings.Count(text, `<tr data-wave-id="`), len(snapshot.Waves); got != want {
		t.Fatalf("wave rows after truncation = %d, want all %d to survive", got, want)
	}
}

// TestRenderTruncationIgnoresDisabledSections is the second R1 regression test:
// data of a disabled section is not rendered, so the renderer must neither
// halve it nor report it in the truncation note.
func TestRenderTruncationIgnoresDisabledSections(t *testing.T) {
	renderer := newTestRenderer(t)
	base := delivery.ReportSnapshot{Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: renderTime}

	t.Run("issues section disabled", func(t *testing.T) {
		snapshot := base
		snapshot.Issues = oversizedIssues(4000)
		snapshot.Waves = oversizedWaves(2000)
		opts := allSections()
		opts.Issues = false
		output, err := renderer.Render(context.Background(), snapshot, "", opts)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(output) > MaxHTMLBytes {
			t.Fatalf("truncated output = %d bytes, want <= %d", len(output), MaxHTMLBytes)
		}
		note := truncationNote(t, output)
		if strings.Contains(note, "问题") {
			t.Fatalf("truncation note reports the disabled issues section: %q", note)
		}
		if !strings.Contains(note, "波次 2000 → ") {
			t.Fatalf("truncation note does not report the sliced waves: %q", note)
		}
		if strings.Contains(string(output), `data-issue-id="`) {
			t.Fatal("disabled issues section rendered issue rows")
		}
	})

	t.Run("results section disabled", func(t *testing.T) {
		snapshot := base
		snapshot.Issues = oversizedIssues(4000)
		snapshot.Waves = oversizedWaves(2000)
		opts := allSections()
		opts.Results = false
		output, err := renderer.Render(context.Background(), snapshot, "", opts)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(output) > MaxHTMLBytes {
			t.Fatalf("truncated output = %d bytes, want <= %d", len(output), MaxHTMLBytes)
		}
		note := truncationNote(t, output)
		if !strings.Contains(note, "问题 4000 → ") {
			t.Fatalf("truncation note does not report the sliced issues: %q", note)
		}
		if strings.Contains(note, "波次") {
			t.Fatalf("truncation note reports the disabled results section: %q", note)
		}
		if strings.Contains(string(output), `data-section="results"`) {
			t.Fatal("disabled results section was rendered")
		}
	})
}

// TestRenderSnapshotTruncationNoteIsVisible is the O1 test: when the snapshot
// service capped an oversized input list, the rendered report must state which
// lists were capped and the cap sizes, without mentioning untouched lists.
func TestRenderSnapshotTruncationNoteIsVisible(t *testing.T) {
	snapshot := sampleSnapshot()
	snapshot.TruncatedSections = []string{
		delivery.ReportTruncationIssues,
		delivery.ReportTruncationCaseRuns,
	}
	output := mustRender(t, snapshot, "", allSections())
	text := string(output)
	if !strings.Contains(text, `data-truncated="true"`) {
		t.Fatal("snapshot truncation produced no visible truncation aside")
	}
	note := truncationNote(t, output)
	for _, want := range []string{"问题", "500", "执行结果", "2000"} {
		if !strings.Contains(note, want) {
			t.Fatalf("truncation note is missing %q: %q", want, note)
		}
	}
	if strings.Contains(note, "波次") {
		t.Fatalf("truncation note mentions an untouched list: %q", note)
	}
}

func TestRenderFailsWhenNarrativeAloneExceedsLimit(t *testing.T) {
	renderer := newTestRenderer(t)
	narrative := strings.Repeat("n", MaxHTMLBytes+1024)
	output, err := renderer.Render(context.Background(), delivery.ReportSnapshot{Kind: delivery.ReportKindVersion, VersionID: 7}, narrative, allSections())
	if err == nil {
		t.Fatalf("Render returned %d bytes for an over-limit narrative, want an error", len(output))
	}
	if output != nil {
		t.Fatalf("Render returned output alongside the limit error: %d bytes", len(output))
	}
}

func TestRenderIsDeterministicAcrossInputOrder(t *testing.T) {
	first := mustRender(t, sampleSnapshot(), "叙述", allSections())
	reordered := sampleSnapshot()
	reverseSnapshotSlices(&reordered)
	second := mustRender(t, reordered, "叙述", allSections())
	if !bytes.Equal(first, second) {
		t.Fatal("rendered HTML depends on input slice order")
	}
	text := string(first)
	issueThree := strings.Index(text, `data-issue-id="3"`)
	issueOne := strings.Index(text, `data-issue-id="1"`)
	if issueThree < 0 || issueOne < 0 {
		t.Fatal("rendered issues table is missing issue rows")
	}
	if issueThree > issueOne {
		t.Fatal("issues are not displayed by id descending")
	}
}

func TestRenderHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := newTestRenderer(t).Render(ctx, sampleSnapshot(), "", allSections())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Render error = %v, want context.Canceled", err)
	}
	if output != nil {
		t.Fatalf("Render returned output for a canceled context: %d bytes", len(output))
	}
}

func reverseSnapshotSlices(snapshot *delivery.ReportSnapshot) {
	reverseRequirements(snapshot.Requirements)
	reverseWaves(snapshot.Waves)
	reverseCaseRuns(snapshot.CaseRuns)
	reverseIssues(snapshot.Issues)
	reverseVersionSUTs(snapshot.VersionSUTs)
	reverseCoverageGaps(snapshot.Coverage.Gaps)
	for index := range snapshot.CaseRuns {
		reverseInt64s(snapshot.CaseRuns[index].VersionSUTIDs)
	}
	for index := range snapshot.Issues {
		reverseInt64s(snapshot.Issues[index].SourceIssueIDs)
		reverseInt64s(snapshot.Issues[index].CaseRunIDs)
	}
}

func reverseRequirements(rows []delivery.RequirementSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseWaves(rows []delivery.WaveSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	for index := range rows {
		sutCounts := rows[index].ByVersionSUT
		for i, j := 0, len(sutCounts)-1; i < j; i, j = i+1, j-1 {
			sutCounts[i], sutCounts[j] = sutCounts[j], sutCounts[i]
		}
	}
}

func reverseCaseRuns(rows []delivery.CaseRunSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseIssues(rows []delivery.IssueSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseVersionSUTs(rows []delivery.VersionSUTSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseCoverageGaps(rows []delivery.CoverageGapSnapshot) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func reverseInt64s(values []int64) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}
