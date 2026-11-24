package main

import (
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
)

// TestJudgeGenerationDedupeKey pins the composition-root half of T8's judge
// generation revival (review fix round 1): the dedupe SEQUENCE is monotonic per
// requirement version (any in-flight judge run serializes; a reset budget never
// reuses an occupied key), while the generation BUDGET is keyed on the SET of
// current draft case versions (any set change resets it). The boolean reports
// budget exhaustion (the caller writes the judge_exhausted facts) versus an
// in-flight skip.
func TestJudgeGenerationDedupeKey(t *testing.T) {
	const baseKey = "judge:10:100"
	workItem := func(reqVersion, anchor string, status agent.WorkItemStatus, attempts, maxAttempts int) agent.WorkItem {
		return agent.WorkItem{
			Type:        "design.judge",
			Status:      status,
			Attempts:    attempts,
			MaxAttempts: maxAttempts,
			Payload: mustJSON(map[string]string{
				"requirement_id": "10", "requirement_version_id": reqVersion,
				"draft_case_version_ids": anchor,
			}),
		}
	}
	terminal := func(anchor string) agent.WorkItem {
		return workItem("100", anchor, agent.WorkItemDone, 1, 3)
	}
	refs := func(ids ...int64) []designapp.JudgeCaseRef {
		out := make([]designapp.JudgeCaseRef, 0, len(ids))
		for _, id := range ids {
			out = append(out, designapp.JudgeCaseRef{CaseID: 7001, CaseVersionID: id})
		}
		return out
	}

	tests := []struct {
		name          string
		items         []agent.WorkItem
		drafts        []designapp.JudgeCaseRef
		wantKey       string
		wantExhausted bool
	}{
		{name: "no judge generation yet keeps the base key", drafts: refs(5), wantKey: baseKey},
		{
			name:   "terminal generation asks for the next judge generation",
			items:  []agent.WorkItem{terminal("5")},
			drafts: refs(5), wantKey: baseKey + ":judge:1",
		},
		{
			name:   "open generation is not stampeded",
			items:  []agent.WorkItem{workItem("100", "5", agent.WorkItemOpen, 0, 3)},
			drafts: refs(5), wantKey: "",
		},
		{
			name:   "running generation is not stampeded",
			items:  []agent.WorkItem{workItem("100", "5", agent.WorkItemRunning, 1, 3)},
			drafts: refs(5), wantKey: "",
		},
		{
			name:   "retry-backed failure is not stampeded",
			items:  []agent.WorkItem{workItem("100", "5", agent.WorkItemFailed, 1, 3)},
			drafts: refs(5), wantKey: "",
		},
		{
			name:   "exhausted failure counts as a terminal generation",
			items:  []agent.WorkItem{workItem("100", "5", agent.WorkItemFailed, 3, 3)},
			drafts: refs(5), wantKey: baseKey + ":judge:1",
		},
		{
			name:   "exhausted budget reports exhaustion",
			items:  []agent.WorkItem{terminal("5"), terminal("5"), terminal("5")},
			drafts: refs(5), wantKey: "", wantExhausted: true,
		},
		{
			name:   "multi-case set counts its own generations",
			items:  []agent.WorkItem{terminal("5,6"), terminal("5,6"), terminal("5,6")},
			drafts: refs(6, 5), wantKey: "", wantExhausted: true,
		},
		{
			name:   "a revised case version resets the budget but not the sequence",
			items:  []agent.WorkItem{terminal("5"), terminal("5"), terminal("5")},
			drafts: refs(6), wantKey: baseKey + ":judge:3",
		},
		{
			name:   "an added draft resets the budget but not the sequence",
			items:  []agent.WorkItem{terminal("5"), terminal("5"), terminal("5")},
			drafts: refs(5, 7), wantKey: baseKey + ":judge:3",
		},
		{
			name:   "an in-flight run of an older set serializes the new set",
			items:  []agent.WorkItem{workItem("100", "5", agent.WorkItemRunning, 1, 3)},
			drafts: refs(5, 7), wantKey: "",
		},
		{
			name:   "another requirement version is ignored",
			items:  []agent.WorkItem{workItem("999", "5", agent.WorkItemDone, 1, 3)},
			drafts: refs(5), wantKey: baseKey,
		},
		{
			name: "another work type is ignored",
			items: []agent.WorkItem{{
				Type:   "design.requirement",
				Status: agent.WorkItemDone,
				Payload: mustJSON(map[string]string{
					"requirement_version_id": "100", "draft_case_version_ids": "5",
				}),
			}},
			drafts: refs(5), wantKey: baseKey,
		},
		{
			name:   "an empty draft set never schedules",
			drafts: nil, wantKey: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, exhausted := judgeGenerationDedupeKey(test.items, baseKey, 100, test.drafts)
			if key != test.wantKey || exhausted != test.wantExhausted {
				t.Fatalf("judgeGenerationDedupeKey = (%q,%v), want (%q,%v)",
					key, exhausted, test.wantKey, test.wantExhausted)
			}
		})
	}
}
