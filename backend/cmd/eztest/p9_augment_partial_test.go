package main

// Task 37 fix round 2: one ineligible requirement must not abort the whole
// augmentation/refresh batch. The design writer rejects a gap whose requirement
// is no longer clarified or whose requirement version went stale, while gap
// analysis deliberately keeps every non-closed requirement. The analysis must
// persist the remaining drafts and report the partial result instead of failing
// the refresh, and a hard augmentation contract error must still leave the
// refresh alive (gaps/coverage were already computed).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// p9PartialDraftWriter persists every augmentation draft except one requirement,
// which the design side rejects the way case.graph_propose rejects a stale
// requirement version. contractStatus additionally simulates a writer that
// violates the draft-only contract.
type p9PartialDraftWriter struct {
	mu                sync.Mutex
	attempts          []int64
	created           []int64
	rejectRequirement int64
	contractStatus    string
}

func (w *p9PartialDraftWriter) CreateDraftCase(_ context.Context, _ int64, draft exploreapp.CaseDraft) (exploreapp.CaseDraftResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.attempts = append(w.attempts, draft.RequirementID)
	if draft.RequirementID == w.rejectRequirement {
		return exploreapp.CaseDraftResult{}, errors.New("requirement version is stale; re-read the graph and retry")
	}
	if w.contractStatus != "" {
		return exploreapp.CaseDraftResult{CaseID: 9000, CaseVersionID: 9001, Status: w.contractStatus}, nil
	}
	w.created = append(w.created, draft.RequirementID)
	return exploreapp.CaseDraftResult{
		CaseID:        int64(7000 + len(w.created)),
		CaseVersionID: int64(8000 + len(w.created)),
		Status:        "draft",
	}, nil
}

func (w *p9PartialDraftWriter) snapshot() (attempts, created []int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int64(nil), w.attempts...), append([]int64(nil), w.created...)
}

// p9AugmentGapFact builds a requirement-scoped acceptance gap without a
// requirement version, so the writer (not the fact) is the authority on
// eligibility unless the test sets a version explicitly.
func p9AugmentGapFact(id, requirementID int64, requirementTitle string) versiondomain.Fact {
	return versiondomain.Fact{
		ID:   id,
		Kind: exploreapp.KindGap,
		Key:  fmt.Sprintf("gap:req:%d:acceptance", requirementID),
		Payload: map[string]any{
			"dimension":         "acceptance",
			"requirement_id":    requirementID,
			"requirement_title": requirementTitle,
			"target":            "/checkout",
			"reason":            "缺少验收标准",
			"question":          "请补充验收标准",
		},
	}
}

// p9AugmentRequirement is a gap-free requirement: rich text covers the
// free-text dimensions and the bound page target is verified by the page fact,
// so gap analysis records no extra gap facts during Refresh.
func p9AugmentRequirement(id, versionID int64, title string) exploreapp.RequirementSnapshot {
	return exploreapp.RequirementSnapshot{
		ID:                 id,
		VersionID:          versionID,
		Title:              title,
		Status:             "clarified",
		Description:        "管理员操作；异常超时重试；边界上限 100；使用预置测试数据账号。",
		AcceptanceCriteria: "操作成功后状态可观察",
		TargetPages:        []string{"http://sut.test/checkout"},
	}
}

func p9LogContains(logs []string, fragment string) bool {
	return strings.Contains(strings.Join(logs, "\n"), fragment)
}

// TestP9AnalysisAugmentPartial proves the refresh survives a partial writer
// failure: the rejected (ineligible) draft is isolated, the eligible draft is
// persisted and reported, and the refresh returns no error. It also proves a
// hard augmentation error (a writer returning a confirmed status, which violates
// the draft-only contract) is logged instead of aborting the refresh.
func TestP9AnalysisAugmentPartial(t *testing.T) {
	facts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{
		exploreapp.KindPage: {p9PageFact(1, "http://sut.test/checkout")},
		exploreapp.KindGap: {
			// The rejected draft comes first: with per-draft isolation the
			// draft after it must still be persisted.
			p9AugmentGapFact(2, 102, "退款"),
			p9AugmentGapFact(3, 101, "下单"),
		},
	}}
	requirements := &fakeP9Requirements{snapshots: []exploreapp.RequirementSnapshot{
		p9AugmentRequirement(101, 10010, "下单"),
		p9AugmentRequirement(102, 10020, "退款"),
	}}
	writer := &p9PartialDraftWriter{rejectRequirement: 102}
	analysis, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      &fakeP9Runtime{},
		Pages:        &fakeP9Closer{},
		Facts:        facts,
		Recorder:     facts,
		Requirements: requirements,
		Cases:        &fakeP9Cases{},
		DraftWriter:  writer,
	})
	if err != nil {
		t.Fatalf("newP9Analysis: %v", err)
	}
	var logs []string
	analysis.logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	result, err := analysis.Refresh(context.Background(), 1001)
	if err != nil {
		t.Fatalf("one rejected draft must not fail the refresh: %v", err)
	}
	if result.Drafts != 1 {
		t.Fatalf("persisted drafts = %d, want the eligible draft only", result.Drafts)
	}
	attempts, created := writer.snapshot()
	if len(attempts) != 2 || attempts[0] != 102 || attempts[1] != 101 {
		t.Fatalf("writer attempts = %v, want both drafts in gap-fact order", attempts)
	}
	if len(created) != 1 || created[0] != 101 {
		t.Fatalf("persisted requirements = %v, want the eligible 101", created)
	}
	if !p9LogContains(logs, "case augmentation partial") {
		t.Fatalf("partial augmentation was not logged: %v", logs)
	}

	// A writer that claims a confirmed case violates the draft-only contract.
	// The augmentation run must stop, but the refresh must not fail: gaps and
	// coverage were already computed and the next refresh can retry.
	violatingFacts := &fakeP9Facts{byKind: map[string][]versiondomain.Fact{
		exploreapp.KindPage: {p9PageFact(1, "http://sut.test/checkout")},
		exploreapp.KindGap:  {p9AugmentGapFact(2, 101, "下单")},
	}}
	violatingWriter := &p9PartialDraftWriter{contractStatus: "confirmed"}
	violating, err := newP9Analysis(p9AnalysisDeps{
		Runtime:      &fakeP9Runtime{},
		Pages:        &fakeP9Closer{},
		Facts:        violatingFacts,
		Recorder:     violatingFacts,
		Requirements: &fakeP9Requirements{snapshots: []exploreapp.RequirementSnapshot{p9AugmentRequirement(101, 10010, "下单")}},
		Cases:        &fakeP9Cases{},
		DraftWriter:  violatingWriter,
	})
	if err != nil {
		t.Fatalf("newP9Analysis violating writer: %v", err)
	}
	var violatingLogs []string
	violating.logf = func(format string, args ...any) {
		violatingLogs = append(violatingLogs, fmt.Sprintf(format, args...))
	}
	violatingResult, err := violating.Refresh(context.Background(), 1001)
	if err != nil {
		t.Fatalf("an augmentation contract error must not fail the refresh: %v", err)
	}
	if violatingResult.Drafts != 0 {
		t.Fatalf("contract violation persisted drafts = %d, want 0", violatingResult.Drafts)
	}
	if !p9LogContains(violatingLogs, "case augmentation failed") {
		t.Fatalf("augmentation failure was not logged: %v", violatingLogs)
	}
}
