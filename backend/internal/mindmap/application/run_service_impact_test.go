package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application/agent_tool"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// fakeP4ImpactReader is the in-memory P4ImpactReader of the incremental-design
// run-start tests; it records the version it was called for.
type fakeP4ImpactReader struct {
	summary   application.P4ImpactSummary
	err       error
	calls     int
	versionID int64
}

// LatestImpactSummary implements application.P4ImpactReader.
func (f *fakeP4ImpactReader) LatestImpactSummary(_ context.Context, versionID int64) (application.P4ImpactSummary, error) {
	f.calls++
	f.versionID = versionID
	if f.err != nil {
		return application.P4ImpactSummary{}, f.err
	}
	return f.summary, nil
}

// startedIncrementalDesign starts one incremental_design run of the continuing
// fixture and returns the stored run.
func startedIncrementalDesign(t *testing.T, fixture *runFixture) domain.CaseDesignRun {
	t.Helper()
	fixture.version.Status = "continuing"
	run, err := fixture.service().Start(context.Background(), runTestVersionID, 42,
		application.StartRunCommand{Kind: application.RunKindIncrementalDesign, BatchID: 77})
	if err != nil {
		t.Fatalf("start incremental_design: %v", err)
	}
	stored := fixture.runs[run.ID]
	if string(stored.Result) != string(run.Result) {
		t.Fatalf("returned result = %s, stored result = %s", run.Result, stored.Result)
	}
	return stored
}

// resultWarningCodes returns the run-result warning codes in document order.
func resultWarningCodes(t *testing.T, run domain.CaseDesignRun) []string {
	t.Helper()
	document := runResultDocument(t, run)
	raw, ok := document["warnings"]
	if !ok {
		return nil
	}
	entries, ok := raw.([]any)
	if !ok {
		t.Fatalf("run result warnings = %#v, want an array", raw)
	}
	codes := make([]string, 0, len(entries))
	for _, entry := range entries {
		record, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("run result warning = %#v, want an object", entry)
		}
		code, _ := record["code"].(string)
		codes = append(codes, code)
	}
	return codes
}

// countResultWarningCode counts one warning code in the run result.
func countResultWarningCode(t *testing.T, run domain.CaseDesignRun, code string) int {
	t.Helper()
	count := 0
	for _, got := range resultWarningCodes(t, run) {
		if got == code {
			count++
		}
	}
	return count
}

// TestStartIncrementalDesignSeedsP4Impact pins spec §6.1 step 3b/§7.6: the
// starter preseed of run.result is the only writer of the P4 impact signal. An
// available analysis records impactAvailable=true and the unmapped changes (an
// explicit empty array when P4 ran with no changes); no usable P4 result records
// impactAvailable=false plus the impact_unavailable warning and never fabricates
// a change list. Only incremental_design runs are seeded.
func TestStartIncrementalDesignSeedsP4Impact(t *testing.T) {
	ctx := context.Background()

	t.Run("available with unmapped changes", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		reader := &fakeP4ImpactReader{summary: application.P4ImpactSummary{
			Available:       true,
			UnmappedChanges: []application.P4UnmappedChange{{Path: "src/x.ts", Summary: "signature changed"}},
		}}
		fixture.impact = reader

		run := startedIncrementalDesign(t, fixture)
		if reader.calls != 1 || reader.versionID != runTestVersionID {
			t.Fatalf("impact reads = %d for version %d, want 1 for %d", reader.calls, reader.versionID, runTestVersionID)
		}
		document := runResultDocument(t, run)
		if document["impactAvailable"] != true {
			t.Fatalf("impactAvailable = %#v, want true", document["impactAvailable"])
		}
		changes, ok := document["unmappedChanges"].([]any)
		if !ok || len(changes) != 1 {
			t.Fatalf("unmappedChanges = %#v, want one change", document["unmappedChanges"])
		}
		change, _ := changes[0].(map[string]any)
		if change["path"] != "src/x.ts" || change["summary"] != "signature changed" {
			t.Fatalf("change = %#v, want the P4 path and summary", change)
		}
		if codes := resultWarningCodes(t, run); len(codes) != 0 {
			t.Fatalf("warnings = %v, want none for an available P4 result", codes)
		}
		if _, exists := document["unmappedChanges"]; !exists {
			t.Fatal("unmappedChanges is absent, want the explicit P4 list")
		}
	})

	t.Run("available with no unmapped changes keeps an explicit empty array", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.impact = &fakeP4ImpactReader{summary: application.P4ImpactSummary{Available: true}}

		run := startedIncrementalDesign(t, fixture)
		document := runResultDocument(t, run)
		if document["impactAvailable"] != true {
			t.Fatalf("impactAvailable = %#v, want true", document["impactAvailable"])
		}
		changes, ok := document["unmappedChanges"].([]any)
		if !ok || len(changes) != 0 {
			t.Fatalf("unmappedChanges = %#v, want the explicit empty array", document["unmappedChanges"])
		}
		if codes := resultWarningCodes(t, run); len(codes) != 0 {
			t.Fatalf("warnings = %v, want none", codes)
		}
	})

	t.Run("available carries the P4 warnings", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.impact = &fakeP4ImpactReader{summary: application.P4ImpactSummary{
			Available: true,
			Warnings: []application.IncrementalPlanWarning{{
				Code:    "unmapped_truncated",
				Message: "the unmapped change list was truncated",
			}},
		}}

		run := startedIncrementalDesign(t, fixture)
		if countResultWarningCode(t, run, "unmapped_truncated") != 1 {
			t.Fatalf("warnings = %v, want the P4 diagnostic", resultWarningCodes(t, run))
		}
		if countResultWarningCode(t, run, "impact_unavailable") != 0 {
			t.Fatalf("warnings = %v, impact was available", resultWarningCodes(t, run))
		}
	})

	t.Run("no P4 result records impact_unavailable", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.impact = &fakeP4ImpactReader{}

		run := startedIncrementalDesign(t, fixture)
		document := runResultDocument(t, run)
		if document["impactAvailable"] != false {
			t.Fatalf("impactAvailable = %#v, want false", document["impactAvailable"])
		}
		if _, exists := document["unmappedChanges"]; exists {
			t.Fatalf("unmappedChanges was fabricated: %#v", document["unmappedChanges"])
		}
		if countResultWarningCode(t, run, "impact_unavailable") != 1 {
			t.Fatalf("warnings = %v, want one impact_unavailable", resultWarningCodes(t, run))
		}
	})

	t.Run("degraded P4 result records impact_unavailable and the degraded diagnostic", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.impact = &fakeP4ImpactReader{summary: application.P4ImpactSummary{
			Available:       true,
			Degraded:        true,
			UnmappedChanges: []application.P4UnmappedChange{{Path: "src/x.ts", Summary: "changed"}},
			Warnings: []application.IncrementalPlanWarning{{
				Code:    "impact_degraded",
				Message: "the latest P4 impact analysis finished degraded",
			}},
		}}

		run := startedIncrementalDesign(t, fixture)
		document := runResultDocument(t, run)
		if document["impactAvailable"] != false {
			t.Fatalf("impactAvailable = %#v, want false for a degraded P4 result", document["impactAvailable"])
		}
		if document["degraded"] != true {
			t.Fatalf("degraded = %#v, want the P4 degraded state carried into the seed", document["degraded"])
		}
		if _, exists := document["unmappedChanges"]; exists {
			t.Fatalf("unmappedChanges must not be seeded from a degraded P4 result: %#v", document["unmappedChanges"])
		}
		if countResultWarningCode(t, run, "impact_degraded") != 1 {
			t.Fatalf("warnings = %v, want the adapter's degraded diagnostic", resultWarningCodes(t, run))
		}
		if countResultWarningCode(t, run, "impact_unavailable") != 1 {
			t.Fatalf("warnings = %v, want one impact_unavailable", resultWarningCodes(t, run))
		}
	})

	t.Run("unavailable status carries the P4 diagnostics", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.impact = &fakeP4ImpactReader{summary: application.P4ImpactSummary{
			Available: false,
			Warnings: []application.IncrementalPlanWarning{{
				Code:    "impact_analysis_pending",
				Message: "the latest P4 impact analysis is still running",
			}},
		}}

		run := startedIncrementalDesign(t, fixture)
		document := runResultDocument(t, run)
		if document["impactAvailable"] != false {
			t.Fatalf("impactAvailable = %#v, want false", document["impactAvailable"])
		}
		if _, exists := document["unmappedChanges"]; exists {
			t.Fatalf("unmappedChanges was fabricated: %#v", document["unmappedChanges"])
		}
		if countResultWarningCode(t, run, "impact_analysis_pending") != 1 {
			t.Fatalf("warnings = %v, want the adapter's status diagnostic", resultWarningCodes(t, run))
		}
		if countResultWarningCode(t, run, "impact_unavailable") != 1 {
			t.Fatalf("warnings = %v, want one impact_unavailable", resultWarningCodes(t, run))
		}
	})

	t.Run("impact read failure degrades to impact_unavailable", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.impact = &fakeP4ImpactReader{err: errors.New("analysis read failed")}

		run := startedIncrementalDesign(t, fixture)
		document := runResultDocument(t, run)
		if document["impactAvailable"] != false {
			t.Fatalf("impactAvailable = %#v, want false", document["impactAvailable"])
		}
		if countResultWarningCode(t, run, "impact_unavailable") != 1 {
			t.Fatalf("warnings = %v, want one impact_unavailable", resultWarningCodes(t, run))
		}
	})

	t.Run("unwired impact port degrades to impact_unavailable", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		run := startedIncrementalDesign(t, fixture)
		document := runResultDocument(t, run)
		if document["impactAvailable"] != false {
			t.Fatalf("impactAvailable = %#v, want false", document["impactAvailable"])
		}
		if countResultWarningCode(t, run, "impact_unavailable") != 1 {
			t.Fatalf("warnings = %v, want one impact_unavailable", resultWarningCodes(t, run))
		}
	})

	t.Run("supplement runs are never seeded", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		reader := &fakeP4ImpactReader{summary: application.P4ImpactSummary{
			Available:       true,
			UnmappedChanges: []application.P4UnmappedChange{{Path: "src/x.ts", Summary: "changed"}},
		}}
		fixture.impact = reader

		run, err := fixture.service().Start(ctx, runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		if err != nil {
			t.Fatalf("start supplement: %v", err)
		}
		document := runResultDocument(t, fixture.runs[run.ID])
		for _, key := range []string{"impactAvailable", "unmappedChanges", "degraded", "warnings"} {
			if _, exists := document[key]; exists {
				t.Fatalf("supplement run result carries %q: %#v", key, document)
			}
		}
		if reader.calls != 0 {
			t.Fatalf("impact reads = %d, a supplement run must not read P4 impact", reader.calls)
		}
	})
}

// TestIncrementalPlanConsumesSeededP4Impact pins the plan side of the starter
// preseed: a seeded available result keeps impact_unavailable out of the plan and
// exposes the unmapped changes, while an unavailable seed still warns exactly
// once even before materialization.
func TestIncrementalPlanConsumesSeededP4Impact(t *testing.T) {
	ctx := context.Background()

	t.Run("available seed survives materialization and drops the warning", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.impact = &fakeP4ImpactReader{summary: application.P4ImpactSummary{
			Available:       true,
			UnmappedChanges: []application.P4UnmappedChange{{Path: "src/refund.ts", Summary: "refund endpoint changed"}},
		}}
		run := startedIncrementalDesign(t, fixture)
		if err := fixture.service().Materialize(ctx, run.ID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		materialized := fixture.runs[run.ID]
		document := runResultDocument(t, materialized)
		if document["impactAvailable"] != true {
			t.Fatalf("materialized impactAvailable = %#v, want true", document["impactAvailable"])
		}

		view, err := planService(fixture).IncrementalPlan(ctx, runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if containsPlanWarning(view.Warnings, "impact_unavailable") {
			t.Fatalf("plan warnings = %+v, seeded P4 impact is available", view.Warnings)
		}
		if len(view.UnmappedChanges) != 1 || view.UnmappedChanges[0].Path != "src/refund.ts" {
			t.Fatalf("plan unmappedChanges = %+v, want the seeded change", view.UnmappedChanges)
		}
	})

	t.Run("unavailable seed warns once before materialization", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		run := startedIncrementalDesign(t, fixture)
		if document := runResultDocument(t, run); document["impactAvailable"] != false {
			t.Fatalf("impactAvailable = %#v, want the seeded false", document["impactAvailable"])
		}
		view, err := planService(fixture).IncrementalPlan(ctx, runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		count := 0
		for _, warning := range view.Warnings {
			if warning.Code == "impact_unavailable" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("plan warnings = %+v, want exactly one impact_unavailable", view.Warnings)
		}
	})
}

// TestSeededP4WarningSurvivesToolAndMaterialization pins the warning owner chain
// of T16b/T14/T9: the starter seeds a P4 diagnostic (truncated unmapped list),
// the supplement tools carry every starter-owned key through their rewrite, and
// the materializer must keep the seeded warning so the plan still reports it
// after the run is done.
func TestSeededP4WarningSurvivesToolAndMaterialization(t *testing.T) {
	ctx := context.Background()
	fixture := newSupplementMaterializeFixture()
	fixture.impact = &fakeP4ImpactReader{summary: application.P4ImpactSummary{
		Available:       true,
		UnmappedChanges: []application.P4UnmappedChange{{Path: "src/refund.ts", Summary: "refund endpoint changed"}},
		Warnings: []application.IncrementalPlanWarning{{
			Code:    "unmapped_truncated",
			Message: "the P4 unmapped change list was truncated",
		}},
	}}
	run := startedIncrementalDesign(t, fixture)
	if countResultWarningCode(t, run, "unmapped_truncated") != 1 {
		t.Fatalf("seed warnings = %v, want the P4 diagnostic", resultWarningCodes(t, run))
	}

	// Run the real T14 tool: it merges the proposal into the seed document and
	// keeps every starter-owned key (impactAvailable, unmappedChanges, warnings).
	tools := agent_tool.NewTools(agent_tool.Deps{
		Runs:         runRunRepo{fixture},
		Graphs:       planService(fixture),
		Requirements: runRequirementReader{fixture},
	})
	if len(tools) == 0 {
		t.Fatal("supplement tools are not wired")
	}
	toolCtx := agentapp.WithToolInvocation(ctx, agentapp.Invocation{
		VersionID:       runTestVersionID,
		SupplementRunID: run.ID,
	})
	proposed := false
	for _, tool := range tools {
		if tool.Name() != "supplement.case_propose" {
			continue
		}
		if _, err := tool.Execute(toolCtx, json.RawMessage(
			`{"cases":[`+validSupplementCaseJSON("Refund after truncation")+`]}`)); err != nil {
			t.Fatalf("supplement.case_propose: %v", err)
		}
		proposed = true
		break
	}
	if !proposed {
		t.Fatal("supplement.case_propose is not registered")
	}
	if countResultWarningCode(t, fixture.runs[run.ID], "unmapped_truncated") != 1 {
		t.Fatalf("after the tool write warnings = %v, want the seeded diagnostic preserved",
			resultWarningCodes(t, fixture.runs[run.ID]))
	}

	if err := fixture.service().Materialize(ctx, run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	materialized := fixture.runs[run.ID]
	materializedDocument := runResultDocument(t, materialized)
	if materializedDocument["impactAvailable"] != true {
		t.Fatalf("materialized impactAvailable = %#v, want true", materializedDocument["impactAvailable"])
	}
	if countResultWarningCode(t, materialized, "unmapped_truncated") != 1 {
		t.Fatalf("materialized warnings = %v, want the seeded P4 diagnostic preserved",
			resultWarningCodes(t, materialized))
	}
	if countResultWarningCode(t, materialized, "impact_unavailable") != 0 {
		t.Fatalf("materialized warnings = %v, impact was available", resultWarningCodes(t, materialized))
	}

	view, err := planService(fixture).IncrementalPlan(ctx, runTestVersionID)
	if err != nil {
		t.Fatalf("incremental plan: %v", err)
	}
	count := 0
	for _, warning := range view.Warnings {
		switch warning.Code {
		case "unmapped_truncated":
			count++
		case "impact_unavailable":
			t.Fatalf("plan warnings = %+v, impact was available", view.Warnings)
		}
	}
	if count != 1 {
		t.Fatalf("plan warnings = %+v, want exactly one unmapped_truncated", view.Warnings)
	}
	if len(view.UnmappedChanges) != 1 || view.UnmappedChanges[0].Path != "src/refund.ts" {
		t.Fatalf("plan unmappedChanges = %+v, want the seeded change", view.UnmappedChanges)
	}
	if len(view.NewCases) != 1 {
		t.Fatalf("plan newCases = %+v, want the materialized tool proposal", view.NewCases)
	}
}

// TestIncrementalDesignSeedIsJSONObject pins the persisted shape: the seed is a
// JSON object the T14 tools can merge into (never a JSON array/scalar).
func TestIncrementalDesignSeedIsJSONObject(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	run := startedIncrementalDesign(t, fixture)
	var document map[string]json.RawMessage
	if err := json.Unmarshal(run.Result, &document); err != nil {
		t.Fatalf("seed result is not a JSON object: %v (%s)", err, run.Result)
	}
}
