package agentapp_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// TestRunnerSettlementCarriesConcreteCause pins QA Q2's missing link: a run that
// enters the tool-less settlement turn because of a budget must report which
// budget actually hit. Before the fix the run ended "done" with no terminal
// reason and the case run was finalized inconclusive(incomplete) with an empty
// last_error; the settlement prompt also always blamed "运行时间" even when the
// turn budget was the trigger. v0.22 keeps only the turn/time causes (tokens are
// telemetry only, R-A2-1).
func TestRunnerSettlementCarriesConcreteCause(t *testing.T) {
	tests := []struct {
		name        string
		itemBudget  string
		settings    map[string]int64
		advance     time.Duration
		firstUsage  map[string]int
		wantCause   string
		wantPrompt  []string
		wantNoCause []string
	}{
		{
			name:       "time budget",
			itemBudget: `{"max_tokens":100000,"run_seconds":100,"max_turns_fuse":12}`,
			advance:    101 * time.Second,
			firstUsage: map[string]int{"total_tokens": 10},
			wantCause:  agentapp.BudgetCauseTime,
			wantPrompt: []string{"运行时间"},
		},
		{
			name:       "turn budget",
			settings:   map[string]int64{"agent.max_turns": 2},
			firstUsage: map[string]int{"total_tokens": 10},
			wantCause:  agentapp.BudgetCauseTurns,
			wantPrompt: []string{"轮次预算"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRunnerFixture(t)
			for key, value := range tc.settings {
				fixture.settings.ints[key] = value
			}
			item := hostItem()
			if tc.itemBudget != "" {
				item.Budget = json.RawMessage(tc.itemBudget)
			}
			if tc.advance > 0 {
				fixture.chat.onChat = func(index int, _ llmapp.ChatRequest) {
					if index == 0 {
						fixture.clock.Advance(tc.advance)
					}
				}
			}
			fixture.chat.responses = []llmapp.ChatResponse{
				{
					ToolCalls: []llmapp.ToolCall{{ID: "call_1", Name: tools.ToolContextVersionPack, Arguments: `{}`}},
					Usage:     tc.firstUsage,
				},
				{Content: "预算原因已说明，以下为收尾。"},
			}

			result, err := mustRun(t, fixture, item)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Status != "done" || result.Turns != 2 {
				t.Fatalf("result = %#v, want a settled done run after 2 turns", result)
			}
			if result.SettlementCause != tc.wantCause {
				t.Fatalf("settlement cause = %q, want %q", result.SettlementCause, tc.wantCause)
			}
			if strings.TrimSpace(result.SettlementDetail) == "" {
				t.Fatal("settlement detail is empty; the cause must carry observable usage/limits")
			}
			if result.TerminalReason != tc.wantCause {
				t.Fatalf("result terminal reason = %q, want the concrete %q", result.TerminalReason, tc.wantCause)
			}
			// The agent_run row is what the Agent Center lists: a settled done run
			// must explain itself instead of showing an empty terminal reason.
			finished := fixture.repo.lastFinished(t)
			if finished.TerminalReason == nil || *finished.TerminalReason != tc.wantCause {
				t.Fatalf("persisted terminal reason = %v, want %q", finished.TerminalReason, tc.wantCause)
			}
			// The settlement turn must name the real cause so the model's final
			// summary (and any human reading it) is not misled.
			prompt := requestPrompt(fixture.chat.request(1))
			for _, want := range tc.wantPrompt {
				if !strings.Contains(prompt, want) {
					t.Fatalf("settlement prompt %q does not mention %q", prompt, want)
				}
			}
			if len(fixture.chat.request(1).Tools) != 0 {
				t.Fatalf("settlement turn still exposes %d tools", len(fixture.chat.request(1).Tools))
			}
		})
	}
}

// TestRunnerNaturalCompletionHasNoSettlementCause keeps the cause honest: a
// model that finishes on its own must not be labelled as a budget stop.
func TestRunnerNaturalCompletionHasNoSettlementCause(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "直接回复，无预算触发。"}}

	result, err := mustRun(t, fixture, hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.SettlementCause != "" || result.TerminalReason != "" {
		t.Fatalf("natural completion reported a cause: %#v", result)
	}
	finished := fixture.repo.lastFinished(t)
	if finished.TerminalReason != nil {
		t.Fatalf("natural completion persisted terminal reason %v, want nil", finished.TerminalReason)
	}
}
