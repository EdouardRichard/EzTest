package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// reportToolsFixture seeds one wave report and one version report in the report
// service fakes used by the report service tests.
type reportToolsFixture struct {
	*reportFixture
	waveReport    delivery.Report
	waveRevision  delivery.ReportRevision
	versionReport delivery.Report
	versionRev    delivery.ReportRevision
	tools         []agenttools.Tool
}

func newReportToolsFixture(t *testing.T) *reportToolsFixture {
	t.Helper()
	base := newReportFixture(t)
	waveID := int64(12)
	waveReport := base.repo.seedReport(delivery.Report{
		ID: 910, VersionID: 7, WaveID: &waveID, Kind: delivery.ReportKindWave, Status: delivery.ReportStatusDraft,
	})
	waveRevision := base.repo.seedRevision(delivery.ReportRevision{
		ReportID: waveReport.ID, Revision: 1, Status: delivery.ReportStatusDraft, RenderStatus: "pending",
	})
	versionReport := base.repo.seedReport(delivery.Report{
		ID: 920, VersionID: 7, Kind: delivery.ReportKindVersion, Status: delivery.ReportStatusDraft,
	})
	versionRevision := base.repo.seedRevision(delivery.ReportRevision{
		ReportID: versionReport.ID, Revision: 1, Status: delivery.ReportStatusDraft, RenderStatus: "pending",
	})

	base.data.wave[reportDataWaveKey(7, waveID)] = reportToolWaveSnapshot(waveID)
	base.data.version[7] = reportToolVersionSnapshot()

	return &reportToolsFixture{
		reportFixture: base,
		waveReport:    waveReport,
		waveRevision:  waveRevision,
		versionReport: versionReport,
		versionRev:    versionRevision,
		tools:         ReportTools(base.svc),
	}
}

func reportToolWaveSnapshot(waveID int64) delivery.ReportSnapshot {
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindWave,
		VersionID: 7,
		WaveID:    &waveID,
		Requirements: []delivery.RequirementSnapshot{
			{ID: 1, VersionID: 7, Title: "Checkout", Status: "closed"},
			{ID: 2, VersionID: 7, Title: "Refunds", Status: "open"},
		},
		Waves: []delivery.WaveSnapshot{
			{ID: waveID, VersionID: 7, Status: "done", Counts: delivery.WaveCountsSnapshot{Total: 5, Passed: 3, Failed: 2}},
		},
		CaseRuns: []delivery.CaseRunSnapshot{
			{
				ID: 3001, VersionID: 7, WaveID: waveID, CaseVersionID: 21, CaseTitle: "Checkout fails",
				Status: "failed", FailureClass: "application", Summary: "order confirmation missing",
				StepCount: 4, StepFailedCount: 1, AssertionCount: 6, AssertionFailedCount: 1,
				Category: "ui", Severity: "major",
				Evidence: []delivery.EvidenceSnapshot{{ID: 8001, CaseRunID: 3001, Kind: "screenshot", SHA256: "deadbeef", SizeBytes: 2048}},
			},
			{
				ID: 3002, VersionID: 7, WaveID: waveID, CaseVersionID: 22, CaseTitle: "Refund passes",
				Status: "passed", StepCount: 3, AssertionCount: 4, Category: "api", Severity: "minor",
			},
		},
		Issues: []delivery.IssueSnapshot{
			{ID: 900, VersionID: 7, Title: "Checkout issue", Status: delivery.IssueStatusOpen, Category: "ui", Severity: "major", Source: "failure_candidate"},
		},
		VersionSUTs: []delivery.VersionSUTSnapshot{
			{ID: 9001, SUTID: 51, SUTName: "Shop", EnvironmentID: 61, EnvironmentName: "staging"},
		},
		Coverage: delivery.CoverageSnapshot{
			RequirementsTotal: 2, RequirementsCovered: 1, CaseVersionsTotal: 3, CaseVersionsCovered: 2,
			Gaps: []delivery.CoverageGapSnapshot{{RequirementID: 2, CaseVersionID: 31, Reason: "execution_not_run"}},
		},
	}
}

func reportToolVersionSnapshot() delivery.ReportSnapshot {
	return delivery.ReportSnapshot{
		Kind:      delivery.ReportKindVersion,
		VersionID: 7,
		Requirements: []delivery.RequirementSnapshot{
			{ID: 1, VersionID: 7, Title: "Checkout", Status: "closed"},
		},
		Waves: []delivery.WaveSnapshot{
			{ID: 12, VersionID: 7, Status: "done", Counts: delivery.WaveCountsSnapshot{Total: 5, Passed: 3, Failed: 2}},
		},
		CaseRuns: []delivery.CaseRunSnapshot{{ID: 3001, VersionID: 7, WaveID: 12, CaseVersionID: 21, Status: "failed", FailureClass: "application"}},
		Coverage: delivery.CoverageSnapshot{RequirementsTotal: 1, RequirementsCovered: 1, CaseVersionsTotal: 3, CaseVersionsCovered: 3},
	}
}

func reportTool(t *testing.T, toolList []agenttools.Tool, name string) agenttools.Tool {
	t.Helper()
	for _, tool := range toolList {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}

func requireReportToolError(t *testing.T, err error, code string) *agenttools.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s tool error, got nil", code)
	}
	var toolErr *agenttools.Error
	if !errors.As(err, &toolErr) || toolErr == nil {
		t.Fatalf("error %v is not a *tools.Error", err)
	}
	if toolErr.Code != code {
		t.Fatalf("tool error code = %q, want %q (%v)", toolErr.Code, code, err)
	}
	return toolErr
}

func TestReportToolsExposeFrozenToolNames(t *testing.T) {
	fixture := newReportToolsFixture(t)
	if len(fixture.tools) != 2 {
		t.Fatalf("ReportTools = %v, want two tools", fixture.tools)
	}
	names := map[string]bool{}
	for _, tool := range fixture.tools {
		names[tool.Name()] = true
		if tool.Description() == "" {
			t.Fatalf("tool %q has an empty description", tool.Name())
		}
		schema := string(tool.Parameters())
		if strings.Contains(schema, "version_id") || strings.Contains(schema, "versionId") {
			t.Fatalf("tool %q schema exposes the injected version scope: %s", tool.Name(), schema)
		}
		var decoded map[string]any
		if err := json.Unmarshal(tool.Parameters(), &decoded); err != nil {
			t.Fatalf("tool %q schema is not a JSON object: %v", tool.Name(), err)
		}
		if decoded["additionalProperties"] != false {
			t.Fatalf("tool %q schema must reject unknown parameters: %s", tool.Name(), schema)
		}
	}
	if !names[agenttools.ToolReportDataRead] || !names[agenttools.ToolReportNarrativePropose] {
		t.Fatalf("ReportTools names = %v", names)
	}
}

func TestReportToolDataReadReturnsOnlyCountsAndIDs(t *testing.T) {
	fixture := newReportToolsFixture(t)
	read := reportTool(t, fixture.tools, agenttools.ToolReportDataRead)

	out, err := read.Execute(context.Background(), agenttools.Scope{
		VersionID: 7, ReportID: 910, ReportRevision: 1, WaveID: 12,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("report.data_read: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("report.data_read result = %#v, want a JSON-ready map", out)
	}
	if payload["report_id"] != "910" || payload["version_id"] != "7" || payload["kind"] != "wave" || payload["wave_id"] != "12" {
		t.Fatalf("report.data_read identity = %#v", payload)
	}
	if payload["revision"] != int32(1) {
		t.Fatalf("report.data_read revision = %#v, want 1", payload["revision"])
	}

	requirements := payload["requirements"].(map[string]any)
	if requirements["total"] != 2 {
		t.Fatalf("requirements = %#v", requirements)
	}
	if byStatus := requirements["by_status"].(map[string]int); byStatus["closed"] != 1 || byStatus["open"] != 1 {
		t.Fatalf("requirement status counts = %#v", byStatus)
	}
	waves := payload["waves"].(map[string]any)
	if waves["total"] != 1 || waves["by_status"].(map[string]int)["done"] != 1 {
		t.Fatalf("waves = %#v", waves)
	}
	caseRuns := payload["case_runs"].(map[string]any)
	if caseRuns["total"] != 2 || caseRuns["by_status"].(map[string]int)["failed"] != 1 ||
		caseRuns["by_failure_class"].(map[string]int)["application"] != 1 {
		t.Fatalf("case_runs = %#v", caseRuns)
	}
	issues := payload["issues"].(map[string]any)
	if issues["total"] != 1 || issues["by_status"].(map[string]int)["open"] != 1 ||
		issues["by_severity"].(map[string]int)["major"] != 1 || issues["by_category"].(map[string]int)["ui"] != 1 {
		t.Fatalf("issues = %#v", issues)
	}
	coverage := payload["coverage"].(map[string]any)
	if coverage["requirements_total"] != 2 || coverage["requirements_covered"] != 1 ||
		coverage["case_versions_total"] != 3 || coverage["case_versions_covered"] != 2 {
		t.Fatalf("coverage = %#v", coverage)
	}
	gaps, ok := coverage["gaps"].([]map[string]any)
	if !ok || len(gaps) != 1 || gaps[0]["requirement_id"] != "2" || gaps[0]["reason"] != "execution_not_run" {
		t.Fatalf("coverage gaps = %#v", coverage["gaps"])
	}
	if ids := payload["version_sut_ids"].([]string); len(ids) != 1 || ids[0] != "9001" {
		t.Fatalf("version_sut_ids = %#v", payload["version_sut_ids"])
	}
	if evidence := payload["evidence"].(map[string]any); evidence["count"] != 1 {
		t.Fatalf("evidence = %#v", evidence)
	}

	// The summary never carries titles, case summaries, evidence hashes/bytes
	// or environment names.
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal report.data_read: %v", err)
	}
	for _, leaked := range []string{"Checkout issue", "order confirmation missing", "deadbeef", "staging", "Shop"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("report.data_read leaked %q: %s", leaked, encoded)
		}
	}
}

func TestReportToolDataReadVersionReport(t *testing.T) {
	fixture := newReportToolsFixture(t)
	read := reportTool(t, fixture.tools, agenttools.ToolReportDataRead)

	out, err := read.Execute(context.Background(), agenttools.Scope{
		VersionID: 7, ReportID: 920, ReportRevision: 1,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("report.data_read(version): %v", err)
	}
	payload := out.(map[string]any)
	if payload["kind"] != "version" {
		t.Fatalf("report.data_read kind = %#v", payload["kind"])
	}
	if _, exists := payload["wave_id"]; exists {
		t.Fatalf("version report summary exposed a wave_id: %#v", payload)
	}
	if payload["coverage"].(map[string]any)["requirements_covered"] != 1 {
		t.Fatalf("version coverage = %#v", payload["coverage"])
	}
}

func TestReportToolDataReadRejectsScopeViolations(t *testing.T) {
	fixture := newReportToolsFixture(t)
	read := reportTool(t, fixture.tools, agenttools.ToolReportDataRead)

	_, err := read.Execute(context.Background(), agenttools.Scope{VersionID: 7, ReportID: 910, ReportRevision: 1}, json.RawMessage(`{"report_id":"920"}`))
	requireReportToolError(t, err, agenttools.CodeInvalidArgument)

	_, err = read.Execute(context.Background(), agenttools.Scope{VersionID: 7, ReportID: 999, ReportRevision: 1}, json.RawMessage(`{}`))
	requireReportToolError(t, err, agenttools.CodeNotFound)

	_, err = read.Execute(context.Background(), agenttools.Scope{VersionID: 8, ReportID: 910, ReportRevision: 1}, json.RawMessage(`{}`))
	requireReportToolError(t, err, agenttools.CodeNotFound)

	_, err = read.Execute(context.Background(), agenttools.Scope{VersionID: 7}, json.RawMessage(`{}`))
	requireReportToolError(t, err, agenttools.CodeInvalidArgument)

	_, err = read.Execute(context.Background(), agenttools.Scope{VersionID: 7, ReportID: 910, ReportRevision: 5}, json.RawMessage(`{}`))
	requireReportToolError(t, err, agenttools.CodeNotFound)

	_, err = read.Execute(context.Background(), agenttools.Scope{VersionID: 7, ReportID: 910, ReportRevision: 1, WaveID: 99}, json.RawMessage(`{}`))
	requireReportToolError(t, err, agenttools.CodeNotFound)
}

func TestReportToolNarrativeProposeStagesWithoutPublishing(t *testing.T) {
	fixture := newReportToolsFixture(t)
	propose := reportTool(t, fixture.tools, agenttools.ToolReportNarrativePropose)

	out, err := propose.Execute(context.Background(), agenttools.Scope{
		VersionID: 7, ReportID: 910, ReportRevision: 1,
	}, json.RawMessage(`{
		"summary_markdown":"<b>本轮通过 3 条</b>，失败 2 条。\n请关注结算页。",
		"highlights":["结算页确认缺失","重试后通过"]
	}`))
	if err != nil {
		t.Fatalf("report.narrative_propose: %v", err)
	}
	payload := out.(map[string]any)
	if payload["report_id"] != "910" || payload["revision"] != int32(1) || payload["accepted"] != true {
		t.Fatalf("report.narrative_propose result = %#v", payload)
	}

	staged, ok := TakeReportNarrative(910, 1)
	if !ok {
		t.Fatal("narrative was not staged for publish")
	}
	if !strings.Contains(staged.SummaryMarkdown, "<b>本轮通过 3 条</b>") || len(staged.Highlights) != 2 {
		t.Fatalf("staged narrative = %+v", staged)
	}

	// The proposal must not publish: the revision stays draft, no render and no
	// content write happens.
	stored, err := fixture.repo.GetRevision(context.Background(), 910, 1)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if stored.Status != delivery.ReportStatusDraft || stored.RenderStatus != "pending" {
		t.Fatalf("revision after narrative proposal = %+v", stored)
	}
	if fixture.renderer.calls != 0 || fixture.content.putCalls != 0 {
		t.Fatalf("narrative proposal published: render=%d put=%d", fixture.renderer.calls, fixture.content.putCalls)
	}
}

func TestReportToolNarrativeProposeValidatesInput(t *testing.T) {
	fixture := newReportToolsFixture(t)
	propose := reportTool(t, fixture.tools, agenttools.ToolReportNarrativePropose)
	scope := agenttools.Scope{VersionID: 7, ReportID: 910, ReportRevision: 1}

	tooManyHighlights := `{"summary_markdown":"ok","highlights":[` + strings.Repeat(`"h",`, 20) + `"h"]}`
	cases := map[string]string{
		"empty":               `{"summary_markdown":""}`,
		"whitespace":          `{"summary_markdown":" \n\t "}`,
		"too long":            `{"summary_markdown":"` + strings.Repeat("a", 4001) + `"}`,
		"too many highlights": tooManyHighlights,
		"scope injection":     `{"summary_markdown":"ok","report_id":"920"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := propose.Execute(context.Background(), scope, json.RawMessage(raw))
			requireReportToolError(t, err, agenttools.CodeInvalidArgument)
		})
	}

	_, err := propose.Execute(context.Background(), scope, json.RawMessage(`{"summary_markdown":"ok"}`))
	if err != nil {
		t.Fatalf("valid narrative rejected: %v", err)
	}
	if _, ok := TakeReportNarrative(910, 1); !ok {
		t.Fatal("valid narrative did not stage a draft")
	}

	_, err = propose.Execute(context.Background(), agenttools.Scope{VersionID: 7, ReportID: 910, ReportRevision: 9}, json.RawMessage(`{"summary_markdown":"ok"}`))
	requireReportToolError(t, err, agenttools.CodeNotFound)

	_, err = propose.Execute(context.Background(), agenttools.Scope{VersionID: 7, ReportID: 910}, json.RawMessage(`{"summary_markdown":"ok"}`))
	requireReportToolError(t, err, agenttools.CodeInvalidArgument)
}

func TestReportServiceRequirePublished(t *testing.T) {
	fixture := newReportToolsFixture(t)
	ctx := context.Background()

	err := fixture.svc.RequirePublished(ctx, 910, 1)
	if !errors.Is(err, ErrReportNotPublished) {
		t.Fatalf("RequirePublished(draft) error = %v, want ErrReportNotPublished", err)
	}
	if err := fixture.svc.RequirePublished(ctx, 999, 1); !errors.Is(err, ErrReportNotFound) {
		t.Fatalf("RequirePublished(unknown report) error = %v, want ErrReportNotFound", err)
	}
	if err := fixture.svc.RequirePublished(ctx, 910, 9); !errors.Is(err, ErrReportNotFound) {
		t.Fatalf("RequirePublished(unknown revision) error = %v, want ErrReportNotFound", err)
	}

	htmlRef := int64(6001)
	published, revision := fixture.repo.seedPublished(delivery.Report{
		ID: 930, VersionID: 7, Kind: delivery.ReportKindVersion,
	}, delivery.ReportRevision{
		ReportID: 930, Revision: 1, Status: delivery.ReportStatusPublished, RenderStatus: "done",
	}, htmlRef, strings.Repeat("a", 64))
	if err := fixture.svc.RequirePublished(ctx, published.ID, revision.Revision); err != nil {
		t.Fatalf("RequirePublished(published) error = %v", err)
	}

	// Only the report's *current* revision counts: an older published revision
	// must not satisfy a newer report task.
	olderReport := fixture.repo.seedReport(delivery.Report{
		ID: 940, VersionID: 7, Kind: delivery.ReportKindVersion, Status: delivery.ReportStatusPublished,
	})
	older := fixture.repo.seedRevision(delivery.ReportRevision{
		ReportID: 940, Revision: 1, Status: delivery.ReportStatusPublished, RenderStatus: "done",
	})
	newer := fixture.repo.seedRevision(delivery.ReportRevision{
		ReportID: 940, Revision: 2, Status: delivery.ReportStatusPublished, RenderStatus: "done",
	})
	olderReport.CurrentRevisionID = &newer.ID
	fixture.repo.reports[940] = olderReport
	if err := fixture.svc.RequirePublished(ctx, 940, older.Revision); !errors.Is(err, ErrReportNotPublished) {
		t.Fatalf("RequirePublished(old published revision) error = %v, want ErrReportNotPublished", err)
	}
	if err := fixture.svc.RequirePublished(ctx, 940, newer.Revision); err != nil {
		t.Fatalf("RequirePublished(current published revision) error = %v", err)
	}

	finalizer := NewReportFinalizer(fixture.svc)
	if err := finalizer.Finalize(ctx, 910, 1); !errors.Is(err, ErrReportNotPublished) {
		t.Fatalf("ReportFinalizer draft error = %v, want ErrReportNotPublished", err)
	}
	if err := finalizer.Finalize(ctx, published.ID, revision.Revision); err != nil {
		t.Fatalf("ReportFinalizer published error = %v", err)
	}
}
