package main

// Task 37 fix round 1: the analysis bundle must persist its augmented case
// drafts through the design service. The writer adapter is deliberately narrow:
// its only design-side capability is CreateAugmentedDraftCase, so it cannot
// confirm a case, execute one or mutate a requirement.

import (
	"context"
	"errors"
	"sync"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func versionViewNoBindings() versionapp.VersionView {
	return versionapp.VersionView{ID: 1001}
}

type fakeP9DraftDesigner struct {
	mu       sync.Mutex
	version  int64
	drafts   []designapp.AugmentedCaseDraft
	err      error
	nextCase int64
	commits  int
}

func (f *fakeP9DraftDesigner) CreateAugmentedDraftCase(_ context.Context, versionID int64, in designapp.AugmentedCaseDraft) (designapp.AugmentedCaseDraftResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version = versionID
	f.drafts = append(f.drafts, in)
	if f.err != nil {
		return designapp.AugmentedCaseDraftResult{}, f.err
	}
	f.nextCase++
	return designapp.AugmentedCaseDraftResult{
		CaseID:        7000 + f.nextCase,
		CaseVersionID: 8000 + f.nextCase,
		Status:        "draft",
	}, nil
}

func p9TestCaseDraft() exploreapp.CaseDraft {
	return exploreapp.CaseDraft{
		DedupeKey:            "dedupe-key",
		RequirementID:        101,
		RequirementVersionID: 10010,
		Dimension:            exploreapp.GapDimensionAcceptance,
		Title:                "[补用例] 下单 · 验收",
		Description:          "缺少验收标准",
		Preconditions:        "需求当前版本已确认",
		Priority:             "P2",
		SourceFactIDs:        []int64{501, 502},
		SourceFactKeys:       []string{"gap:req:101:acceptance", "gap:req:101:acceptance:2"},
		Steps: []exploreapp.CaseDraftStep{
			{Seq: 1, Action: "打开下单页", Expected: "出现下单表单"},
			{Seq: 2, Action: "逐条核对验收标准", Expected: "每条标准均可观察"},
		},
		Assertions: []exploreapp.CaseDraftAssertion{
			{StepSeq: 2, Kind: "ui", Target: "验收标准", Expected: "全部验收标准逐条满足"},
		},
	}
}

// TestP9DraftCaseWriter proves the explore.CaseDraft projection reaches the
// design draft service with the version SUT resolved and only draft semantics.
func TestP9DraftCaseWriter(t *testing.T) {
	designer := &fakeP9DraftDesigner{}
	writer := newP9DraftCaseWriter(designer, &fakeP9Versions{view: p9TestVersionView()})
	created, err := writer.CreateDraftCase(context.Background(), 1001, p9TestCaseDraft())
	if err != nil {
		t.Fatalf("CreateDraftCase: %v", err)
	}
	if created.CaseID != 7001 || created.CaseVersionID != 8001 || created.Status != "draft" {
		t.Fatalf("created = %+v", created)
	}
	if designer.version != 1001 || len(designer.drafts) != 1 {
		t.Fatalf("designer calls = version %d drafts %d", designer.version, len(designer.drafts))
	}
	draft := designer.drafts[0]
	if draft.RequirementID != 101 || draft.RequirementVersionID != 10010 {
		t.Fatalf("requirement binding = %+v", draft)
	}
	// The version SUT is resolved from the version binding; the draft itself has
	// no resource ids to choose from.
	if draft.VersionSUTID != 77 {
		t.Fatalf("version SUT = %d, want the first bound SUT 77", draft.VersionSUTID)
	}
	if len(draft.Steps) != 2 || len(draft.Assertions) != 1 {
		t.Fatalf("steps/assertions = %d/%d", len(draft.Steps), len(draft.Assertions))
	}
	if len(draft.SourceFactIDs) != 2 || draft.SourceFactIDs[0] != 501 {
		t.Fatalf("source fact ids = %v", draft.SourceFactIDs)
	}

	// A writer failure and a missing version SUT binding fail closed.
	designer.err = errors.New("requirement is not clarified")
	if _, err := writer.CreateDraftCase(context.Background(), 1001, p9TestCaseDraft()); err == nil {
		t.Fatal("CreateDraftCase must propagate the design error")
	}
	empty := newP9DraftCaseWriter(designer, &fakeP9Versions{view: versionViewNoBindings()})
	if _, err := empty.CreateDraftCase(context.Background(), 1001, p9TestCaseDraft()); err == nil {
		t.Fatal("CreateDraftCase must reject a version without a bound SUT")
	}
	if _, err := (*p9DraftCaseWriter)(nil).CreateDraftCase(context.Background(), 1001, p9TestCaseDraft()); err == nil {
		t.Fatal("nil writer must fail closed")
	}
}

// TestP9AnalysisPersistsDrafts proves the analysis bundle actually passes the
// writer to CaseAugment and reports the persisted drafts.
func TestP9AnalysisPersistsDrafts(t *testing.T) {
	facts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{
		exploreapp.KindGap: {p9GapFact(2)},
	}}
	designer := &fakeP9DraftDesigner{}
	writer := newP9DraftCaseWriter(designer, &fakeP9Versions{view: p9TestVersionView()})
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:  &fakeP9Runtime{},
		Pages:    &fakeP9Closer{},
		Facts:    facts,
		Recorder: facts,
		Requirements: &fakeP9Requirements{snapshots: []exploreapp.RequirementSnapshot{{
			ID: 101, VersionID: 10010, Title: "下单", Status: "clarified", Description: "用户下单",
		}}},
		Cases:       &fakeP9Cases{},
		DraftWriter: writer,
		Versions:    &fakeP9Versions{view: p9TestVersionView()},
	})
	if err != nil {
		t.Fatalf("newP9Analysis: %v", err)
	}
	result, err := analysis.Refresh(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(designer.drafts) == 0 {
		t.Fatal("analysis did not persist any augmented draft")
	}
	if result.Drafts != len(designer.drafts) {
		t.Fatalf("result drafts = %d, persisted = %d", result.Drafts, len(designer.drafts))
	}
	for _, draft := range designer.drafts {
		if draft.VersionSUTID != 77 {
			t.Fatalf("persisted draft version SUT = %d", draft.VersionSUTID)
		}
	}
}
