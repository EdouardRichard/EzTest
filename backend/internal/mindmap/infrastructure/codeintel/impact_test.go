package codeintel_test

import (
	"context"
	"errors"
	"testing"

	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapcodeintel "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/codeintel"
)

// TestImpactSummaryAdapterMapsAvailableAnalysis pins the incremental-design
// starter projection of spec §6.1 step 3b/§7.6: a completed, non-degraded P4
// analysis exposes its unmapped changes as {path, summary} and reports
// impactAvailable=true.
func TestImpactSummaryAdapterMapsAvailableAnalysis(t *testing.T) {
	reader := &fakeAnalysisReader{analysis: codeinteldomain.CodeAnalysis{
		ID:         33,
		VersionID:  7,
		BaseCommit: "aaaa",
		HeadCommit: "bbbb",
		Status:     codeinteldomain.RunDone,
		Unmapped: []codeinteldomain.UnmappedChange{
			{Path: "src/a.ts", Status: "M", Reason: codeinteldomain.UnmappedNoFact, Suggestion: "人工确认是否影响需求"},
			{Path: "src/b.ts", Status: "D", Reason: codeinteldomain.UnmappedDeleted},
		},
	}}
	adapter := mindmapcodeintel.NewImpactSummaryAdapter(reader)
	var _ mindmapapp.P4ImpactReader = adapter

	summary, err := adapter.LatestImpactSummary(context.Background(), 7)
	if err != nil {
		t.Fatalf("LatestImpactSummary: %v", err)
	}
	if reader.versionID != 7 {
		t.Fatalf("analysis reader received version %d, want 7", reader.versionID)
	}
	if !summary.Available || summary.Degraded {
		t.Fatalf("available/degraded = %v/%v, want true/false", summary.Available, summary.Degraded)
	}
	if len(summary.UnmappedChanges) != 2 {
		t.Fatalf("unmappedChanges = %+v, want 2", summary.UnmappedChanges)
	}
	if summary.UnmappedChanges[0].Path != "src/a.ts" ||
		summary.UnmappedChanges[0].Summary != "人工确认是否影响需求" {
		t.Fatalf("first change = %+v, want the path and P4 suggestion", summary.UnmappedChanges[0])
	}
	if summary.UnmappedChanges[1].Path != "src/b.ts" ||
		summary.UnmappedChanges[1].Summary != codeinteldomain.UnmappedDeleted {
		t.Fatalf("second change = %+v, want the reason fallback", summary.UnmappedChanges[1])
	}
	if len(summary.Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none", summary.Warnings)
	}
}

// TestImpactSummaryAdapterUnavailable covers the degraded/not-started cases: a
// degraded analysis is not consumable (spec §6.1 step 3a records
// impact_unavailable), a running analysis is not started, and a version without
// an analysis is the documented zero summary.
func TestImpactSummaryAdapterUnavailable(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		analysis codeinteldomain.CodeAnalysis
	}{
		{
			name: "degraded analysis",
			analysis: codeinteldomain.CodeAnalysis{
				ID:       34,
				Status:   codeinteldomain.RunDone,
				Degraded: true,
				Unmapped: []codeinteldomain.UnmappedChange{{Path: "src/a.ts", Reason: codeinteldomain.UnmappedAIUnavailable}},
			},
		},
		{name: "running analysis", analysis: codeinteldomain.CodeAnalysis{ID: 35, Status: codeinteldomain.RunRunning}},
		{name: "failed analysis", analysis: codeinteldomain.CodeAnalysis{ID: 36, Status: codeinteldomain.RunFailed}},
		{name: "no analysis", analysis: codeinteldomain.CodeAnalysis{}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			adapter := mindmapcodeintel.NewImpactSummaryAdapter(&fakeAnalysisReader{analysis: testCase.analysis})
			summary, err := adapter.LatestImpactSummary(ctx, 7)
			if err != nil {
				t.Fatalf("LatestImpactSummary: %v", err)
			}
			if summary.Available {
				t.Fatalf("available = true for %s, want false", testCase.name)
			}
			if testCase.analysis.Degraded != summary.Degraded {
				t.Fatalf("degraded = %v, want %v", summary.Degraded, testCase.analysis.Degraded)
			}
		})
	}
}

// TestImpactSummaryAdapterNoAnalysisNotFound covers the documented zero summary:
// a version without any analysis is a nil-error zero summary, not a lookup
// failure.
func TestImpactSummaryAdapterNoAnalysisNotFound(t *testing.T) {
	reader := &fakeAnalysisReader{err: codeinteldomain.NewNotFound("analysis_not_found", "codeintel: version 7 has no analysis")}
	adapter := mindmapcodeintel.NewImpactSummaryAdapter(reader)

	summary, err := adapter.LatestImpactSummary(context.Background(), 7)
	if err != nil {
		t.Fatalf("LatestImpactSummary: %v", err)
	}
	if summary.Available || summary.Degraded || len(summary.UnmappedChanges) != 0 || len(summary.Warnings) != 0 {
		t.Fatalf("summary = %+v, want the zero summary", summary)
	}
}

// TestImpactSummaryAdapterPropagatesOperationalError keeps a real read failure
// visible: the starter turns it into impact_unavailable, but the adapter must not
// silently swallow it.
func TestImpactSummaryAdapterPropagatesOperationalError(t *testing.T) {
	adapter := mindmapcodeintel.NewImpactSummaryAdapter(&fakeAnalysisReader{err: errors.New("database unavailable")})
	if _, err := adapter.LatestImpactSummary(context.Background(), 7); err == nil {
		t.Fatal("error = nil, want the operational failure")
	}
}

// TestImpactSummaryAdapterSurfacesUnmappedTruncated pins the P4
// stats.unmappedTruncated diagnostic (spec §6.6 step 7, P4 design §6.6): a
// completed analysis whose unmapped list hit the 200-entry cap stays available,
// but its summary must carry the truncation warning so the plan never presents
// a silently truncated list as complete.
func TestImpactSummaryAdapterSurfacesUnmappedTruncated(t *testing.T) {
	reader := &fakeAnalysisReader{analysis: codeinteldomain.CodeAnalysis{
		ID:       37,
		Status:   codeinteldomain.RunDone,
		Unmapped: []codeinteldomain.UnmappedChange{{Path: "src/a.ts", Reason: codeinteldomain.UnmappedNoFact}},
		Stats:    []byte(`{"unmapped":200,"unmappedTruncated":true}`),
	}}
	summary, err := mindmapcodeintel.NewImpactSummaryAdapter(reader).LatestImpactSummary(context.Background(), 7)
	if err != nil {
		t.Fatalf("LatestImpactSummary: %v", err)
	}
	if !summary.Available || summary.Degraded {
		t.Fatalf("available/degraded = %v/%v, want true/false", summary.Available, summary.Degraded)
	}
	if len(summary.UnmappedChanges) != 1 || summary.UnmappedChanges[0].Path != "src/a.ts" {
		t.Fatalf("unmappedChanges = %+v, want the mapped change", summary.UnmappedChanges)
	}
	if countImpactWarning(summary, "unmapped_truncated") != 1 {
		t.Fatalf("warnings = %+v, want exactly one unmapped_truncated", summary.Warnings)
	}
	for _, warning := range summary.Warnings {
		if warning.Code == "unmapped_truncated" && warning.Message == "" {
			t.Fatal("the truncation warning has no message")
		}
	}
}

// TestImpactSummaryAdapterSurfacesDegradedDiagnostic pins the done+degraded
// branch: the P4 result is not consumable, and the adapter must expose both the
// degraded flag and a degraded diagnostic so the starter can distinguish it from
// a version without any analysis (spec §6.1 step 3a).
func TestImpactSummaryAdapterSurfacesDegradedDiagnostic(t *testing.T) {
	reader := &fakeAnalysisReader{analysis: codeinteldomain.CodeAnalysis{
		ID:       38,
		Status:   codeinteldomain.RunDone,
		Degraded: true,
		Unmapped: []codeinteldomain.UnmappedChange{{Path: "src/a.ts", Reason: codeinteldomain.UnmappedAIUnavailable}},
		Stats:    []byte(`{"aiUnavailable":true}`),
	}}
	summary, err := mindmapcodeintel.NewImpactSummaryAdapter(reader).LatestImpactSummary(context.Background(), 7)
	if err != nil {
		t.Fatalf("LatestImpactSummary: %v", err)
	}
	if summary.Available {
		t.Fatal("available = true for a degraded analysis, want false")
	}
	if !summary.Degraded {
		t.Fatal("degraded = false, want true")
	}
	if countImpactWarning(summary, "impact_degraded") != 1 {
		t.Fatalf("warnings = %+v, want exactly one impact_degraded", summary.Warnings)
	}
	if len(summary.UnmappedChanges) != 1 {
		t.Fatalf("unmappedChanges = %+v, want the diagnostic data kept", summary.UnmappedChanges)
	}
	// The port never synthesizes the plan's own impact_unavailable code; the
	// starter still records that warning exactly once for unavailable results.
	if countImpactWarning(summary, "impact_unavailable") != 0 {
		t.Fatalf("warnings = %+v, the adapter must not synthesize impact_unavailable", summary.Warnings)
	}
}

// TestImpactSummaryAdapterStatusFallbackDiagnostics pins the status fallback of
// an analysis that exists but is not consumable: the summary carries a
// status-derived diagnostic with a non-empty message, and an unknown/empty status
// still gets the generic incomplete diagnostic.
func TestImpactSummaryAdapterStatusFallbackDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		status string
		code   string
	}{
		{name: "queued", status: codeinteldomain.RunQueued, code: "impact_analysis_pending"},
		{name: "running", status: codeinteldomain.RunRunning, code: "impact_analysis_pending"},
		{name: "failed", status: codeinteldomain.RunFailed, code: "impact_analysis_failed"},
		{name: "unknown status", status: "", code: "impact_analysis_incomplete"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			reader := &fakeAnalysisReader{analysis: codeinteldomain.CodeAnalysis{ID: 39, Status: testCase.status}}
			summary, err := mindmapcodeintel.NewImpactSummaryAdapter(reader).LatestImpactSummary(context.Background(), 7)
			if err != nil {
				t.Fatalf("LatestImpactSummary: %v", err)
			}
			if summary.Available {
				t.Fatalf("available = true for status %q, want false", testCase.status)
			}
			if countImpactWarning(summary, testCase.code) != 1 {
				t.Fatalf("warnings = %+v, want exactly one %s", summary.Warnings, testCase.code)
			}
			for _, warning := range summary.Warnings {
				if warning.Code == testCase.code && warning.Message == "" {
					t.Fatalf("%s warning has no message", testCase.code)
				}
			}
		})
	}
}

// TestImpactSummaryAdapterUnreadableStatsFallsBack pins the stats read fallback:
// a malformed stored stats document must not fail the starter nor silently drop
// the truncation/degradation signal; the analysis stays available and a
// diagnostic warns that the diagnostics are incomplete.
func TestImpactSummaryAdapterUnreadableStatsFallsBack(t *testing.T) {
	reader := &fakeAnalysisReader{analysis: codeinteldomain.CodeAnalysis{
		ID:     40,
		Status: codeinteldomain.RunDone,
		Stats:  []byte(`{not json`),
	}}
	summary, err := mindmapcodeintel.NewImpactSummaryAdapter(reader).LatestImpactSummary(context.Background(), 7)
	if err != nil {
		t.Fatalf("LatestImpactSummary: %v", err)
	}
	if !summary.Available {
		t.Fatal("available = false for a done analysis with an unreadable stats document, want true")
	}
	if countImpactWarning(summary, "impact_stats_unreadable") != 1 {
		t.Fatalf("warnings = %+v, want exactly one impact_stats_unreadable", summary.Warnings)
	}
	if countImpactWarning(summary, "unmapped_truncated") != 0 {
		t.Fatalf("warnings = %+v, truncation is unknown and must not be asserted", summary.Warnings)
	}
}

// TestImpactSummaryAdapterNilGuard pins the fail-closed wiring guards: a nil
// adapter and an adapter without a reader both report a configuration error
// instead of panicking.
func TestImpactSummaryAdapterNilGuard(t *testing.T) {
	var adapter *mindmapcodeintel.ImpactSummaryAdapter
	if _, err := adapter.LatestImpactSummary(context.Background(), 7); err == nil {
		t.Fatal("a nil adapter must report a configuration error")
	}
	if _, err := mindmapcodeintel.NewImpactSummaryAdapter(nil).LatestImpactSummary(context.Background(), 7); err == nil {
		t.Fatal("an adapter without an analysis reader must report a configuration error")
	}
}

// countImpactWarning counts one warning code in a summary.
func countImpactWarning(summary mindmapapp.P4ImpactSummary, code string) int {
	count := 0
	for _, warning := range summary.Warnings {
		if warning.Code == code {
			count++
		}
	}
	return count
}
