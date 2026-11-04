package designapp

// Task 37 fix round 1: the exploration analysis augments cases by creating
// draft cases through the existing design service. This test pins the design
// side of that writer: a draft is persisted as a draft case version (never
// confirmed), references its source facts, is visible in the design graph, and
// reuses the single augmentation scenario instead of bloating the requirement.

import (
	"context"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

func augmentedDraftCase() AugmentedCaseDraft {
	return AugmentedCaseDraft{
		RequirementID:        3001,
		RequirementVersionID: 3002,
		VersionSUTID:         9001,
		Title:                "[补用例] 登录 · 验收",
		Description:          "登录缺少可验证的验收标准",
		Preconditions:        "需求当前版本已确认",
		Priority:             "P2",
		Steps: []AugmentedCaseDraftStep{
			{Seq: 1, Action: "打开登录页", Expected: "出现登录表单"},
			{Seq: 2, Action: "逐条核对验收标准", Expected: "每条标准均可观察"},
		},
		Assertions: []AugmentedCaseDraftAssertion{
			{StepSeq: 2, Kind: "ui", Target: "验收标准", Expected: "全部验收标准逐条满足"},
		},
		SourceFactIDs: []int64{501, 502},
	}
}

func TestCaseAugmentDraftWriterPersistsDraft(t *testing.T) {
	service, repo, _, _ := newGraphFixture(t)
	input := augmentedDraftCase()

	created, err := service.CreateAugmentedDraftCase(context.Background(), 1001, input)
	if err != nil {
		t.Fatalf("CreateAugmentedDraftCase: %v", err)
	}
	if created.CaseID <= 0 || created.CaseVersionID <= 0 {
		t.Fatalf("created ids = %+v", created)
	}
	if created.Status != string(design.CaseStatusDraft) {
		t.Fatalf("created status = %q, want draft", created.Status)
	}

	item := repo.cases[created.CaseID]
	if item == nil || item.current == nil {
		t.Fatalf("draft case was not persisted: %+v", item)
	}
	if item.current.Status != design.CaseStatusDraft {
		t.Fatalf("persisted status = %q, want draft", item.current.Status)
	}
	if item.current.Title != input.Title {
		t.Fatalf("persisted title = %q", item.current.Title)
	}
	if len(item.current.Steps) != 2 || len(item.current.Assertions) != 1 {
		t.Fatalf("persisted steps/assertions = %d/%d", len(item.current.Steps), len(item.current.Assertions))
	}
	if len(item.current.Sources) != 2 {
		t.Fatalf("persisted sources = %+v, want one per gap fact", item.current.Sources)
	}
	for _, source := range item.current.Sources {
		if source.Kind != design.SourceKnowledge {
			t.Fatalf("source kind = %q, want knowledge", source.Kind)
		}
	}
	// The requirement projection must be unchanged: augmentation never mutates a
	// requirement (and it never confirms the case it created).
	if repo.requirement.Status != "clarified" || repo.requirement.Revision != 1 {
		t.Fatalf("requirement mutated by augmentation: %+v", repo.requirement)
	}

	graph, err := service.ReadGraph(context.Background(), 1001)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	visible := false
	for _, requirement := range graph.Requirements {
		for _, scenario := range requirement.Scenarios {
			for _, card := range scenario.Cases {
				if card.CaseID == created.CaseID && card.Status == design.CaseStatusDraft {
					visible = true
				}
			}
		}
	}
	if !visible {
		t.Fatalf("augmented draft is not visible in the design graph: %+v", graph)
	}

	// A second augmentation for the same requirement reuses the scenario and the
	// identical draft instead of creating another case.
	again, err := service.CreateAugmentedDraftCase(context.Background(), 1001, input)
	if err != nil {
		t.Fatalf("second CreateAugmentedDraftCase: %v", err)
	}
	if again.CaseID != created.CaseID {
		t.Fatalf("second draft case id = %d, want the existing %d", again.CaseID, created.CaseID)
	}
	if len(repo.cases) != 1 {
		t.Fatalf("cases = %d, want 1", len(repo.cases))
	}
	if len(repo.scenarios) != 1 {
		t.Fatalf("scenarios = %d, want the single augmentation scenario", len(repo.scenarios))
	}

	// A different dimension reuses the same scenario but creates a new draft.
	second := input
	second.Title = "[补用例] 登录 · 边界"
	other, err := service.CreateAugmentedDraftCase(context.Background(), 1001, second)
	if err != nil {
		t.Fatalf("third CreateAugmentedDraftCase: %v", err)
	}
	if other.CaseID == created.CaseID || len(repo.cases) != 2 || len(repo.scenarios) != 1 {
		t.Fatalf("second dimension: case=%d cases=%d scenarios=%d", other.CaseID, len(repo.cases), len(repo.scenarios))
	}
	if status := repo.cases[other.CaseID].current.Status; status != design.CaseStatusDraft {
		t.Fatalf("second dimension status = %q", status)
	}
}
