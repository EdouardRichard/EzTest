package versionadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
)

// fakeBriefingSource is the in-memory read source behind the briefing reader.
// It lets the reader's version validation, empty handling and deterministic
// ordering/limits be tested without a database.
type fakeBriefingSource struct {
	source    agentcontext.Source
	err       error
	calls     int
	versionID int64
}

func (f *fakeBriefingSource) Load(_ context.Context, versionID int64) (agentcontext.Source, error) {
	f.calls++
	f.versionID = versionID
	return f.source, f.err
}

func TestBriefingReaderEmptyVersion(t *testing.T) {
	t.Run("empty source returns non-nil empty sections", func(t *testing.T) {
		source, err := newBriefingReader(&fakeBriefingSource{}).Load(context.Background(), 7)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if source.Version.Goal != "" || source.Version.Stage != "" || source.Version.Blocker != "" {
			t.Fatalf("version snapshot = %#v, want zero value", source.Version)
		}
		for name, length := range map[string]int{
			"requirements.confirmed":   len(source.Requirements.Confirmed),
			"requirements.gaps":        len(source.Requirements.Gaps),
			"cases.coverage_gaps":      len(source.Cases.CoverageGaps),
			"plan":                     len(source.Plan),
			"findings":                 len(source.Findings),
			"knowledge":                len(source.Knowledge),
			"open_questions.questions": len(source.OpenQuestions.Questions),
			"open_questions.human":     len(source.OpenQuestions.HumanRequests),
		} {
			if length != 0 {
				t.Fatalf("%s length = %d, want 0", name, length)
			}
		}
		if source.Requirements.Confirmed == nil ||
			source.Requirements.Gaps == nil ||
			source.Cases.CoverageGaps == nil ||
			source.Plan == nil ||
			source.Findings == nil ||
			source.Knowledge == nil ||
			source.OpenQuestions.Questions == nil ||
			source.OpenQuestions.HumanRequests == nil {
			t.Fatalf("empty source has nil slices: %#v", source)
		}
		if source.Cases.Draft != 0 || source.Cases.Confirmed != 0 || source.Cases.Stale != 0 {
			t.Fatalf("case counts = %#v, want zero", source.Cases)
		}
		if source.Execution != (agentcontext.ExecutionSnapshot{}) {
			t.Fatalf("execution = %#v, want zero", source.Execution)
		}
	})

	t.Run("invalid version ID is rejected", func(t *testing.T) {
		fake := &fakeBriefingSource{}
		for _, versionID := range []int64{0, -1} {
			if _, err := newBriefingReader(fake).Load(context.Background(), versionID); err == nil {
				t.Fatalf("Load(%d) = nil error, want invalid version error", versionID)
			}
		}
		if fake.calls != 0 {
			t.Fatalf("source called %d times for an invalid version", fake.calls)
		}
	})

	t.Run("nil reader and nil source fail without panic", func(t *testing.T) {
		var reader *BriefingReader
		if _, err := reader.Load(context.Background(), 7); err == nil {
			t.Fatal("nil reader Load = nil error")
		}
		if _, err := newBriefingReader(nil).Load(context.Background(), 7); err == nil {
			t.Fatal("nil source Load = nil error")
		}
	})

	t.Run("source errors propagate", func(t *testing.T) {
		boom := errors.New("boom")
		if _, err := newBriefingReader(&fakeBriefingSource{err: boom}).Load(context.Background(), 7); !errors.Is(err, boom) {
			t.Fatalf("Load error = %v, want %v", err, boom)
		}
	})

	t.Run("version ID reaches the source", func(t *testing.T) {
		fake := &fakeBriefingSource{}
		if _, err := newBriefingReader(fake).Load(context.Background(), 42); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if fake.versionID != 42 || fake.calls != 1 {
			t.Fatalf("source calls/version = %d/%d, want 1/42", fake.calls, fake.versionID)
		}
	})
}

func TestBriefingReaderBuildsBriefing(t *testing.T) {
	source := agentcontext.Source{
		Version: agentcontext.VersionSnapshot{Goal: "验证结算", Stage: "executing"},
		Requirements: agentcontext.RequirementsSnapshot{
			Confirmed: []agentcontext.RequirementSnapshot{{Title: "优惠券", Acceptance: "折扣生效"}},
			Gaps:      []string{"退款规则未定"},
		},
		Cases:         agentcontext.CasesSnapshot{Draft: 1, Confirmed: 2, Stale: 0, CoverageGaps: []string{"退款"}},
		OpenQuestions: agentcontext.OpenQuestionsSnapshot{HumanRequests: []string{"登录接管"}},
	}
	reader := newBriefingReader(&fakeBriefingSource{source: source})
	briefing, err := agentcontext.NewBriefingBuilder(reader).BuildBriefing(context.Background(), 77)
	if err != nil {
		t.Fatalf("BuildBriefing: %v", err)
	}
	rendered := briefing.Render()
	for _, want := range []string{"验证结算", "executing", "优惠券", "折扣生效", "退款规则未定", "登录接管"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered briefing is missing %q:\n%s", want, rendered)
		}
	}
}

func TestBriefingReaderOrdersAndLimits(t *testing.T) {
	const limit = 20
	source := agentcontext.Source{}
	for index := 0; index < limit+10; index++ {
		source.Requirements.Confirmed = append(source.Requirements.Confirmed, agentcontext.RequirementSnapshot{
			Title:      fmt.Sprintf("requirement-%02d", index),
			Acceptance: fmt.Sprintf("acceptance-%02d", index),
		})
		source.Requirements.Gaps = append(source.Requirements.Gaps, fmt.Sprintf("gap-%02d", index))
		source.Cases.CoverageGaps = append(source.Cases.CoverageGaps, fmt.Sprintf("coverage-%02d", index))
		source.Findings = append(source.Findings, agentcontext.FactSnapshot{
			ID:      int64(index + 1),
			Kind:    "page",
			Key:     fmt.Sprintf("key-%02d", index),
			Summary: fmt.Sprintf("fact-%02d", index),
		})
		source.Knowledge = append(source.Knowledge, agentcontext.KnowledgeSnapshot{
			ID:      int64(index + 1),
			Title:   fmt.Sprintf("knowledge-%02d", index),
			Summary: fmt.Sprintf("summary-%02d", index),
		})
		source.OpenQuestions.Questions = append(source.OpenQuestions.Questions, fmt.Sprintf("question-%02d", index))
		source.OpenQuestions.HumanRequests = append(source.OpenQuestions.HumanRequests, fmt.Sprintf("human-%02d", index))
	}
	// The plan arrives in (sort_order, id) order but with a dependency after its
	// dependant. The reader must return dependency-first order and substitute
	// the dependency title in the rendered reference.
	for index := 0; index < limit+10; index++ {
		item := agentcontext.PlanItemSnapshot{
			ID:     int64(100 + index),
			Title:  fmt.Sprintf("plan-%03d", 100+index),
			Status: "pending",
		}
		if index == 1 {
			item.DependsOn = []string{"102"}
		}
		source.Plan = append(source.Plan, item)
	}

	got, err := newBriefingReader(&fakeBriefingSource{source: source}).Load(context.Background(), 9)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	assertLen := func(name string, length int) {
		t.Helper()
		if length != limit {
			t.Fatalf("%s length = %d, want %d", name, length, limit)
		}
	}
	assertLen("requirements.confirmed", len(got.Requirements.Confirmed))
	assertLen("requirements.gaps", len(got.Requirements.Gaps))
	assertLen("cases.coverage_gaps", len(got.Cases.CoverageGaps))
	assertLen("plan", len(got.Plan))
	assertLen("findings", len(got.Findings))
	assertLen("knowledge", len(got.Knowledge))
	assertLen("open_questions.questions", len(got.OpenQuestions.Questions))
	assertLen("open_questions.human", len(got.OpenQuestions.HumanRequests))

	// The reader preserves the semantic input order where it is meaningful.
	if got.Requirements.Confirmed[0].Title != "requirement-00" ||
		got.Requirements.Confirmed[0].Acceptance != "acceptance-00" {
		t.Fatalf("confirmed requirements order changed: %#v", got.Requirements.Confirmed[0])
	}
	if got.Requirements.Gaps[0] != "gap-00" {
		t.Fatalf("requirement gaps order changed: %#v", got.Requirements.Gaps[0])
	}
	if got.Cases.CoverageGaps[0] != "coverage-00" {
		t.Fatalf("coverage gaps order changed: %#v", got.Cases.CoverageGaps[0])
	}
	if got.Findings[0].ID != 1 || got.Knowledge[0].ID != 1 {
		t.Fatalf("fact/knowledge order changed: %#v %#v", got.Findings[0], got.Knowledge[0])
	}
	if got.OpenQuestions.Questions[0] != "question-00" || got.OpenQuestions.HumanRequests[0] != "human-00" {
		t.Fatalf("open question order changed: %#v", got.OpenQuestions)
	}

	// plan-101 depends on plan-102, so 102 must be emitted first and the
	// dependency reference must be rendered by title.
	positions := make(map[int64]int, len(got.Plan))
	for index, item := range got.Plan {
		positions[item.ID] = index
	}
	position102, ok := positions[102]
	if !ok {
		t.Fatalf("plan item 102 was dropped by the limit")
	}
	position101, ok := positions[101]
	if !ok {
		t.Fatalf("plan item 101 was dropped by the limit")
	}
	if position102 > position101 {
		t.Fatalf("plan dependency order = %#v, want 102 before 101", positions)
	}
	var dependent agentcontext.PlanItemSnapshot
	for _, item := range got.Plan {
		if item.ID == 101 {
			dependent = item
		}
	}
	if len(dependent.DependsOn) != 1 || dependent.DependsOn[0] != "plan-102" {
		t.Fatalf("plan dependency text = %#v, want [plan-102]", dependent.DependsOn)
	}
}
