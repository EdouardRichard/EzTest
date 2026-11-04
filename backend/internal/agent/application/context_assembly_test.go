package agentapp

import (
	"fmt"
	"strings"
	"testing"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// testConfidence returns the pointer shape of a fact confidence.
func testConfidence(value float64) *float64 { return &value }

// testState is the pinned material of the post-compaction reinjection tests: the
// task context of work_item 42, the requirement/case ids, one open plan item,
// one decision fact, pinned facts and the derived briefing. RecentMessages is
// deliberately present as the material that compaction folds: the reinjection
// must not replay it.
func testState() compactionState {
	return compactionState{
		Input: ContextInput{
			Task: TaskContext{
				Type:               "design.requirement",
				Goal:               "完成登录接口",
				Constraints:        []string{"不得修改已确认需求"},
				AcceptanceCriteria: []string{"登录成功返回 200", "错误密码返回 401"},
				Scope:              []string{"work_item_id=42", "requirement_id=501"},
			},
			Conversation: ConversationContext{
				PinnedFacts: []string{"JWT 有效期 15 分钟"},
				RecentMessages: []Message{
					{ID: 9001, Role: "user", Content: "这段历史必须被折叠，不能出现在重注入里"},
				},
			},
			VersionPack: VersionPack{VersionID: 10, Title: "订单系统"},
		},
		WorkItemID:     42,
		RequirementIDs: []int64{501},
		CaseIDs:        []int64{7001},
		Plan: []agentcontext.PlanItemSnapshot{
			{ID: 7, Title: "实现登录接口", Status: "in_progress", DependsOn: []string{"6"}},
		},
		Facts: []agentcontext.FactSnapshot{
			{ID: 9, Kind: "decision", Key: "auth.method", Summary: "使用 JWT", Confidence: testConfidence(0.9)},
		},
		Briefing: "## 计划\n当前：[in_progress] 实现登录接口",
	}
}

// TestCompactionReinjectsTaskContext is the plan's required assertion (R-A5-9):
// after a compaction the pinned task context is rebuilt from state.input and
// still carries the work item id and the acceptance criteria.
func TestCompactionReinjectsTaskContext(t *testing.T) {
	out := assembleAfterCompaction(testState())
	if !strings.Contains(out, "work_item: 42") || !strings.Contains(out, "acceptance") {
		t.Fatalf("task context lost: %s", out)
	}
	for _, want := range []string{
		"goal: 完成登录接口",
		"task_type: design.requirement",
		"不得修改已确认需求",
		"登录成功返回 200",
		"requirement_id=501",
		"case_id=7001",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("reinjected task context misses %q:\n%s", want, out)
		}
	}
}

// TestCompactionReinjectionKeepsPlanAndFacts pins R-A5-9's second half: the plan
// items and facts are pinned briefing material, are never folded into the
// summary and survive the compaction rebuild with their ids/keys.
func TestCompactionReinjectionKeepsPlanAndFacts(t *testing.T) {
	out := assembleAfterCompaction(testState())
	for _, want := range []string{
		"JWT 有效期 15 分钟",
		"plan_item id=7 status=in_progress",
		"fact kind=decision key=auth.method",
		"## 计划",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("reinjection lost pinned material %q:\n%s", want, out)
		}
	}
}

// TestCompactionReinjectionDropsFoldedHistory pins that the rebuild renders
// pinned segments only: the folded transcript must not leak into the
// reinjected block (it belongs to the summary).
func TestCompactionReinjectionDropsFoldedHistory(t *testing.T) {
	out := assembleAfterCompaction(testState())
	for _, folded := range []string{"这段历史必须被折叠", "RecentMessages"} {
		if strings.Contains(out, folded) {
			t.Fatalf("folded history %q leaked into the reinjection:\n%s", folded, out)
		}
	}
}

// TestCompactionReinjectionCapsPinnedFacts pins the R-A5-4 pinned_facts ≤20
// cap: the newest 20 survive and the older five are dropped.
func TestCompactionReinjectionCapsPinnedFacts(t *testing.T) {
	state := testState()
	state.PinnedFacts = make([]string, 0, 25)
	for index := 0; index < 25; index++ {
		state.PinnedFacts = append(state.PinnedFacts, fmt.Sprintf("钉住事实%02d", index))
	}
	out := assembleAfterCompaction(state)
	if !strings.Contains(out, "pinned_facts (20/20)") {
		t.Fatalf("pinned facts header missing:\n%s", out)
	}
	for index := 5; index < 25; index++ {
		if want := fmt.Sprintf("钉住事实%02d", index); !strings.Contains(out, want) {
			t.Fatalf("newest pinned fact %q dropped:\n%s", want, out)
		}
	}
	for index := 0; index < 5; index++ {
		if dropped := fmt.Sprintf("钉住事实%02d", index); strings.Contains(out, dropped) {
			t.Fatalf("oldest pinned fact %q kept:\n%s", dropped, out)
		}
	}
}

// TestCompactionReinjectionFromRunState pins the production handoff: the runner
// state (work item + parsed payload) is enough to rebuild the pinned task
// context, so the reinjection never depends on the rolling summary.
func TestCompactionReinjectionFromRunState(t *testing.T) {
	state := &runState{
		item:    agent.WorkItem{ID: 42},
		payload: WorkItemPayload{RequirementID: 501, CaseID: 7001},
		input: ContextInput{Task: TaskContext{
			Goal:               "完成登录接口",
			AcceptanceCriteria: []string{"登录成功返回 200"},
		}},
	}
	out := assembleAfterCompaction(newCompactionState(state))
	for _, want := range []string{
		"work_item: 42", "acceptance", "登录成功返回 200",
		"requirement_id=501", "case_id=7001",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("run-state reinjection misses %q:\n%s", want, out)
		}
	}
}
