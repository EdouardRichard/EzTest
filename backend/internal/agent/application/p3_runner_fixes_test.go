package agentapp

import (
	"context"
	"errors"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

type p3Settings map[string]int64

func (s p3Settings) Int(_ context.Context, key string) (int64, error) {
	if value, ok := s[key]; ok {
		return value, nil
	}
	return 0, errors.New("setting not found")
}

func (s p3Settings) Float(context.Context, string) (float64, error) { return 0, errors.New("setting not found") }

func TestMaxTurnsP3ProfilesUseProfileBudget(t *testing.T) {
	runner := &Runner{settings: p3Settings{"agent.max_turns": 12}}
	cases := []struct {
		name    string
		profile agent.AgentProfile
		want    int
	}{
		{
			name:    "case executor keeps its 60-turn case budget",
			profile: agent.AgentProfile{Key: ProfileKeyCaseExecutor, Budgets: agent.ProfileBudgets{MaxTurns: 60}},
			want:    60,
		},
		{
			name:    "failure analyst keeps its 6-turn budget",
			profile: agent.AgentProfile{Key: ProfileKeyFailureAnalyst, Budgets: agent.ProfileBudgets{MaxTurns: 6}},
			want:    6,
		},
		{
			name:    "chat profile still honours the global 12-turn cap",
			profile: agent.AgentProfile{Key: ProfileKeyClarificationHost, Budgets: agent.ProfileBudgets{MaxTurns: 20}},
			want:    12,
		},
	}
	for _, tc := range cases {
		if got := runner.maxTurns(tc.profile); got != tc.want {
			t.Fatalf("%s: maxTurns = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestExecutionTaskProviderOnlyForExecuteCase(t *testing.T) {
	for _, workType := range []string{WorkTypeAnalyzeFailure, WorkTypeAnalyzeCoverage} {
		if isExecutionWorkType(workType) {
			t.Fatalf("%s must not use the execution task provider (no case_run scope)", workType)
		}
	}
	if !isExecutionWorkType(WorkTypeExecuteCase) {
		t.Fatal("execute.case must use the execution task provider")
	}
}
