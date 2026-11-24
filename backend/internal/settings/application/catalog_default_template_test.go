package settingsapp

// v0.2.4-G T10 fix round, R-G10b: the production default report template MUST be
// the human-useful v3 (five-element defect cards), not the legacy flat table.
// This test chains the REAL catalog default through the REAL renderer so a
// default flip regression (or a catalog/renderer drift) fails here.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/render"
)

func TestDefaultReportTemplateRendersV3(t *testing.T) {
	defs := Definitions("http://127.0.0.1:13336")
	def := defs[indexOf(t, defs, "report.template_version")]
	var version string
	if err := json.Unmarshal(def.Default, &version); err != nil {
		t.Fatalf("decode report.template_version default %s: %v", def.Default, err)
	}
	if version != "3" {
		t.Fatalf("production default report template = %q, want \"3\" (R-G10b)", version)
	}
	renderer, err := render.NewHTMLRenderer(version)
	if err != nil {
		t.Fatalf("NewHTMLRenderer(default %q): %v", version, err)
	}
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	snapshot := delivery.ReportSnapshot{
		Kind: delivery.ReportKindVersion, VersionID: 7, GeneratedAt: now,
		CaseRuns: []delivery.CaseRunSnapshot{
			{ID: 21, VersionID: 7, CaseVersionID: 201, RequirementID: 101, CaseTitle: "登录用例", Status: "failed", FailureClass: "application", CreatedAt: now},
		},
		Issues: []delivery.IssueSnapshot{
			{ID: 1, VersionID: 7, Title: "登录失败", Status: delivery.IssueStatusOpen, Category: "ui", Severity: "major", RepresentativeCaseRunID: 21, FailedExpected: "跳转主页", FailedActual: "停留登录页", CaseRunIDs: []int64{21}, CreatedAt: now, UpdatedAt: now},
		},
	}
	output, err := renderer.Render(context.Background(), snapshot, "", delivery.SectionOptions{})
	if err != nil {
		t.Fatalf("Render with default template: %v", err)
	}
	text := string(output)
	if !strings.Contains(text, `data-template-version="3"`) {
		t.Fatal("the default-rendered report is not template v3")
	}
	for _, marker := range []string{`data-element="requirement"`, `data-element="steps"`, `data-element="ac"`, `data-element="expected"`, `data-element="actual"`} {
		if !strings.Contains(text, marker) {
			t.Fatalf("the default-rendered report has no five-element card element %q", marker)
		}
	}
	// The legacy v1 flat issues table must NOT be the default user report.
	if strings.Contains(text, "<th>合并来源数</th>") {
		t.Fatal("the default-rendered report is the legacy flat issues table, not the v3 defect cards")
	}
}
