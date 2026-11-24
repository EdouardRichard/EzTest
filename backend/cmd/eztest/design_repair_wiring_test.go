package main

import (
	"encoding/json"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// TestDesignConvergesAfterCoverageRepairGeneration pins the composition-root
// half of Task 70's generation-gap repair: a terminal design.requirement task
// must not block the next authoring generation, an in-flight or retry-backed
// generation must not be duplicated, and the repair budget is bounded.
func TestDesignConvergesAfterCoverageRepairGeneration(t *testing.T) {
	const baseKey = "design:3005:3006"
	workItem := func(reqVersion string, status agent.WorkItemStatus, attempts, maxAttempts int) agent.WorkItem {
		payload, _ := json.Marshal(map[string]string{
			"requirement_id": "3005", "requirement_version_id": reqVersion,
		})
		return agent.WorkItem{
			Type:        "design.requirement",
			Status:      status,
			Attempts:    attempts,
			MaxAttempts: maxAttempts,
			Payload:     payload,
		}
	}

	tests := []struct {
		name           string
		items          []agent.WorkItem
		want           string
		wantExhausted  bool
		wantGeneration int
	}{
		{
			name: "no authoring generation yet keeps the base key",
			want: baseKey,
		},
		{
			name:           "terminal generation asks for the first repair generation",
			items:          []agent.WorkItem{workItem("3006", agent.WorkItemDone, 1, 3)},
			want:           baseKey + ":repair:1",
			wantGeneration: 1,
		},
		{
			name:  "open generation is not stampeded",
			items: []agent.WorkItem{workItem("3006", agent.WorkItemOpen, 0, 3)},
			want:  "",
		},
		{
			name:  "claimed generation is not stampeded",
			items: []agent.WorkItem{workItem("3006", agent.WorkItemClaimed, 1, 3)},
			want:  "",
		},
		{
			name:  "running generation is not stampeded",
			items: []agent.WorkItem{workItem("3006", agent.WorkItemRunning, 1, 3)},
			want:  "",
		},
		{
			name:  "retry-backed failure is not stamped by a repair generation",
			items: []agent.WorkItem{workItem("3006", agent.WorkItemFailed, 1, 3)},
			want:  "",
		},
		{
			name:           "exhausted failure counts as a terminal generation",
			items:          []agent.WorkItem{workItem("3006", agent.WorkItemFailed, 3, 3)},
			want:           baseKey + ":repair:1",
			wantGeneration: 1,
		},
		{
			name: "terminal generations advance the bounded repair budget",
			items: []agent.WorkItem{
				workItem("3006", agent.WorkItemDone, 1, 3),
				workItem("3006", agent.WorkItemDone, 1, 3),
			},
			want:           baseKey + ":repair:2",
			wantGeneration: 2,
		},
		{
			name: "exhausted repair budget escalates instead of stranding (H-F5b)",
			items: []agent.WorkItem{
				workItem("3006", agent.WorkItemDone, 1, 3),
				workItem("3006", agent.WorkItemDone, 1, 3),
				workItem("3006", agent.WorkItemDone, 1, 3),
			},
			want:           "",
			wantExhausted:  true,
			wantGeneration: 3,
		},
		{
			name: "spent budget with an in-flight generation is a normal wait, not an escalation",
			items: []agent.WorkItem{
				workItem("3006", agent.WorkItemDone, 1, 3),
				workItem("3006", agent.WorkItemDone, 1, 3),
				workItem("3006", agent.WorkItemDone, 1, 3),
				workItem("3006", agent.WorkItemOpen, 0, 3),
			},
			want:           "",
			wantGeneration: 3,
		},
		{
			name: "spent budget with a retry-backed failure still owes work",
			items: []agent.WorkItem{
				workItem("3006", agent.WorkItemDone, 1, 3),
				workItem("3006", agent.WorkItemDone, 1, 3),
				workItem("3006", agent.WorkItemFailed, 1, 3),
			},
			want:           "",
			wantGeneration: 2,
		},
		{
			name:  "another requirement version is ignored",
			items: []agent.WorkItem{workItem("9999", agent.WorkItemDone, 1, 3)},
			want:  baseKey,
		},
		{
			name: "another work type is ignored",
			items: []agent.WorkItem{{
				Type:   "design.critic",
				Status: agent.WorkItemDone,
				Payload: mustJSON(map[string]string{
					"requirement_id": "3005", "requirement_version_id": "3006",
				}),
			}},
			want: baseKey,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, exhausted, generation := coverageRepairDedupeKey(test.items, baseKey, 3006)
			if got != test.want || exhausted != test.wantExhausted || generation != test.wantGeneration {
				t.Fatalf("coverageRepairDedupeKey = (%q, %v, %d), want (%q, %v, %d)",
					got, exhausted, generation, test.want, test.wantExhausted, test.wantGeneration)
			}
		})
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
