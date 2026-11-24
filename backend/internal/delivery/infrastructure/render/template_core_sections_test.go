package render

import (
	"context"
	"fmt"
	"strings"
	"testing"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// TestCoreSectionsNonOmittable pins SC-G1 I3: the user-facing v3 report's core
// sections (概览/缺陷/覆盖/追溯) are always present. SectionOptions cannot omit
// them — a fixed-format report must never silently drop a core section.
func TestCoreSectionsNonOmittable(t *testing.T) {
	// Every core section switch disabled.
	opts := delivery.SectionOptions{Summary: false, Results: false, Issues: false, Coverage: false, Evidence: false, Appendix: false}
	text := string(renderV3(t, v3SampleSnapshot(), "", opts))
	for _, marker := range []string{
		`data-section="summary"`,
		`data-section="issues"`,
		`data-section="coverage"`,
		`data-section="traceability"`,
		`data-section="evidence"`,
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("v3 omitted core section %q while SectionOptions disabled it", marker)
		}
	}
}

// TestCoreSectionsPresentWithAllOptions is the positive control: with every
// option enabled the same core sections render (the freeze is not conditional).
func TestCoreSectionsPresentWithAllOptions(t *testing.T) {
	text := string(renderV3(t, v3SampleSnapshot(), "", allSections()))
	for _, marker := range []string{`data-section="summary"`, `data-section="issues"`, `data-section="coverage"`, `data-section="traceability"`} {
		if !strings.Contains(text, marker) {
			t.Fatalf("v3 all-options output is missing core section %q", marker)
		}
	}
}

// TestSectionOptionsOmitSectionContent is the section-switch discrimination test
// (T19b #23①). The v3 template has no section switch, so disabling options there
// cannot prove the switch works — the v3 property is "core sections are
// non-omittable by construction" (TestCoreSectionsNonOmittable). The v2 template
// does own the per-section switch, so disabling a section MUST make its marker
// and rows disappear, and the enabled render MUST contain them. A regression that
// stops honoring SectionOptions turns this test red.
func TestSectionOptionsOmitSectionContent(t *testing.T) {
	snapshot := delivery.ReportSnapshot{
		Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: v3RenderTime,
		Issues: []delivery.IssueSnapshot{{
			ID: 1, VersionID: 7, Title: "登录失败", Status: delivery.IssueStatusOpen,
			Category: "ui", Severity: "major", Source: "analysis", CreatedAt: v3RenderTime,
		}},
	}
	renderer := newVersionedRenderer(t, TemplateVersionV2)

	enabled, err := renderer.Render(context.Background(), snapshot, "", allSections())
	if err != nil {
		t.Fatalf("Render v2 with issues enabled: %v", err)
	}
	if !strings.Contains(string(enabled), `data-section="issues"`) || !strings.Contains(string(enabled), `data-issue-id="1"`) {
		t.Fatal("enabled issues section did not render its marker and row")
	}

	disabledOpts := allSections()
	disabledOpts.Issues = false
	disabled, err := renderer.Render(context.Background(), snapshot, "", disabledOpts)
	if err != nil {
		t.Fatalf("Render v2 with issues disabled: %v", err)
	}
	if strings.Contains(string(disabled), `data-section="issues"`) || strings.Contains(string(disabled), `data-issue-id="1"`) {
		t.Fatal("disabled issues section still rendered its marker and row")
	}
}

// TestV3MaxHTMLBytesTruncationIsObservable is the v3 MaxHTMLBytes discrimination
// test (T19b #23②): an oversized v3 report is truncated (issue cards halved),
// stays under the hard bound, and discloses the truncation explicitly with the
// pre/post counts. The frozen v1 truncation tests do not exercise the v3 card
// projection, so this pins the v3 path itself.
func TestV3MaxHTMLBytesTruncationIsObservable(t *testing.T) {
	const total = 4000
	snapshot := delivery.ReportSnapshot{Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: v3RenderTime}
	for id := int64(1); id <= total; id++ {
		snapshot.Issues = append(snapshot.Issues, delivery.IssueSnapshot{
			ID: id, VersionID: 7, Title: strings.Repeat("x", 900),
			Status: delivery.IssueStatusOpen, CreatedAt: v3RenderTime,
		})
	}
	output := renderV3(t, snapshot, "", allSections())
	if len(output) > MaxHTMLBytes {
		t.Fatalf("truncated v3 output = %d bytes, want <= %d", len(output), MaxHTMLBytes)
	}
	text := string(output)
	if !strings.Contains(text, `data-truncated="true"`) || !strings.Contains(text, "报告截断说明") {
		t.Fatal("v3 oversized report rendered no visible truncation note")
	}
	if !strings.Contains(text, fmt.Sprintf("问题 %d → ", total)) {
		t.Fatal("v3 truncation note does not report the truncated issue counts")
	}
	kept := strings.Count(text, `data-issue-id="`)
	if kept == 0 || kept >= total {
		t.Fatalf("v3 defect cards kept after truncation = %d, want between 1 and %d", kept, total-1)
	}
}

// TestV3ForcedCoreSectionsDriveTruncation proves the v3 option-forcing block is
// load-bearing, not cosmetic: with EVERY SectionOptions field false the caller
// asks for no sections, but the renderer forces the core sections on, so the
// oversized issue list can still be halved and the report renders under the hard
// bound with an explicit truncation note. Removing the forcing block makes the
// truncation pass skip the list and Render fail above MaxHTMLBytes — this test
// then fails through renderV3's error check.
func TestV3ForcedCoreSectionsDriveTruncation(t *testing.T) {
	const total = 4000
	snapshot := delivery.ReportSnapshot{Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: v3RenderTime}
	for id := int64(1); id <= total; id++ {
		snapshot.Issues = append(snapshot.Issues, delivery.IssueSnapshot{
			ID: id, VersionID: 7, Title: strings.Repeat("x", 900),
			Status: delivery.IssueStatusOpen, CreatedAt: v3RenderTime,
		})
	}
	allDisabled := delivery.SectionOptions{}
	output := renderV3(t, snapshot, "", allDisabled)
	if len(output) > MaxHTMLBytes {
		t.Fatalf("forced-core v3 output = %d bytes, want <= %d", len(output), MaxHTMLBytes)
	}
	text := string(output)
	if !strings.Contains(text, `data-truncated="true"`) {
		t.Fatal("forced-core v3 report rendered no truncation disclosure")
	}
	kept := strings.Count(text, `data-issue-id="`)
	if kept == 0 || kept >= total {
		t.Fatalf("forced-core v3 defect cards kept = %d, want between 1 and %d", kept, total-1)
	}
}
