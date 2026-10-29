package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// scopeTestObservation is one tool execution seen by scopeTestTool: the
// invocation the runner handed it and whether the executor injected one at all.
type scopeTestObservation struct {
	invocation application.Invocation
	ok         bool
}

// scopeTestTool records the Invocation of every call it serves. The runner
// executes tools synchronously, so the test reads the records after WaitIdle;
// the mutex keeps the recording race-free anyway.
type scopeTestTool struct {
	mu   sync.Mutex
	seen []scopeTestObservation
}

// Name implements domain.Tool.
func (t *scopeTestTool) Name() string { return "test.scope" }

// Description implements domain.Tool.
func (t *scopeTestTool) Description() string { return "record the run scope seen by a tool" }

// InputSchema implements domain.Tool.
func (t *scopeTestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Execute implements domain.Tool.
func (t *scopeTestTool) Execute(ctx context.Context, _ json.RawMessage) (domain.ToolResult, error) {
	invocation, ok := application.InvocationFromContext(ctx)
	t.mu.Lock()
	t.seen = append(t.seen, scopeTestObservation{invocation: invocation, ok: ok})
	t.mu.Unlock()
	return domain.ToolResult{Content: "ok"}, nil
}

// observations returns a copy of everything the tool saw.
func (t *scopeTestTool) observations() []scopeTestObservation {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]scopeTestObservation(nil), t.seen...)
}

// scopeTestEnvironment builds a run service whose only allowed tool records the
// invocation of each call. Every run makes two model turns (a tool call, then a
// final answer), so runs is the number of runs the scripted caller can serve.
func scopeTestEnvironment(t *testing.T, runs int) (*application.RunService, *runTestState, *scopeTestTool) {
	t.Helper()
	profile := runTestProfile(t)
	profile.AllowedTools = []string{"test.scope"}
	state := runTestNewState(t, profile, 7)

	tool := &scopeTestTool{}
	scripts := make([]runFakeScript, 0, runs*2)
	for i := 0; i < runs; i++ {
		callID := fmt.Sprintf("call_scope_%d", i+1)
		scripts = append(scripts,
			runFakeScript{events: []application.LLMStreamEvent{{
				Done:         true,
				FinishReason: "tool_calls",
				ToolCalls: []domain.ToolCall{{
					ID: callID, Name: tool.Name(), Arguments: `{}`,
				}},
			}}},
			runFakeScript{events: []application.LLMStreamEvent{{
				Delta: "完成", Done: true, FinishReason: "stop",
			}}},
		)
	}
	caller := &runFakeCaller{model: "deepseek-flash", contextWindow: 100000, scripts: scripts}
	service, _, _ := runTestNewService(t, state, caller, runTestRegisterTool(tool))
	return service, state, tool
}

// TestStartWithScopeThreadsBatchAndCaseRunIntoInvocation pins the P6 plumbing:
// the scope given to StartWithScope must reach the tool's Invocation, otherwise
// execution tools would always read BatchID/CaseRunID 0.
func TestStartWithScopeThreadsBatchAndCaseRunIntoInvocation(t *testing.T) {
	service, _, tool := scopeTestEnvironment(t, 1)

	scope := domain.RunScope{BatchID: 41, CaseRunID: 42}
	result, err := service.StartWithScope(context.Background(), 7,
		[]domain.Part{{Type: "text", Text: "执行用例"}}, nil, scope)
	if err != nil {
		t.Fatalf("StartWithScope returned error: %v", err)
	}
	if result.RunID == 0 || result.UserMessageID == 0 {
		t.Fatalf("StartResult=%+v want both ids", result)
	}
	runTestWaitIdle(t, service, 7)

	seen := tool.observations()
	if len(seen) != 1 {
		t.Fatalf("tool executions=%d want 1", len(seen))
	}
	if !seen[0].ok {
		t.Fatal("the tool received no Invocation in its context")
	}
	if seen[0].invocation.BatchID != scope.BatchID || seen[0].invocation.CaseRunID != scope.CaseRunID {
		t.Fatalf("invocation batch=%d case_run=%d want %d/%d",
			seen[0].invocation.BatchID, seen[0].invocation.CaseRunID,
			scope.BatchID, scope.CaseRunID)
	}
	if seen[0].invocation.RunID != result.RunID {
		t.Fatalf("invocation run=%d want the started run %d", seen[0].invocation.RunID, result.RunID)
	}
}

// TestStartWithoutScopeKeepsZeroInvocation pins backward compatibility: the
// existing Start entry point (P1 text-only) starts a run with no execution
// scope, so tools still see the zero value.
func TestStartWithoutScopeKeepsZeroInvocation(t *testing.T) {
	service, _, tool := scopeTestEnvironment(t, 1)

	if _, err := service.Start(context.Background(), 7, "检查运行时健康", nil); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	seen := tool.observations()
	if len(seen) != 1 {
		t.Fatalf("tool executions=%d want 1", len(seen))
	}
	if !seen[0].ok {
		t.Fatal("the tool received no Invocation in its context")
	}
	if seen[0].invocation.BatchID != 0 || seen[0].invocation.CaseRunID != 0 {
		t.Fatalf("invocation batch=%d case_run=%d want 0/0 for the P1 Start path",
			seen[0].invocation.BatchID, seen[0].invocation.CaseRunID)
	}
}

// TestRunScopeDoesNotLeakAcrossRuns pins per-run isolation: two sequential runs
// with different scopes must each observe their own ids. A scope stored on the
// service (instead of the run) would leak the first run's ids into the second.
func TestRunScopeDoesNotLeakAcrossRuns(t *testing.T) {
	service, state, tool := scopeTestEnvironment(t, 2)

	scopes := []domain.RunScope{
		{BatchID: 41, CaseRunID: 42},
		{BatchID: 43, CaseRunID: 44},
	}
	for index, scope := range scopes {
		message := fmt.Sprintf("执行用例 %d", index+1)
		if _, err := service.StartWithScope(context.Background(), 7,
			[]domain.Part{{Type: "text", Text: message}}, nil, scope); err != nil {
			t.Fatalf("StartWithScope run %d returned error: %v", index+1, err)
		}
		runTestWaitIdle(t, service, 7)
	}

	if finished := state.finishedResults(); len(finished) != len(scopes) {
		t.Fatalf("finished runs=%d want %d", len(finished), len(scopes))
	}
	seen := tool.observations()
	if len(seen) != len(scopes) {
		t.Fatalf("tool executions=%d want %d", len(seen), len(scopes))
	}
	for index, scope := range scopes {
		if !seen[index].ok {
			t.Fatalf("run %d: the tool received no Invocation in its context", index+1)
		}
		if seen[index].invocation.BatchID != scope.BatchID || seen[index].invocation.CaseRunID != scope.CaseRunID {
			t.Fatalf("run %d: invocation batch=%d case_run=%d want %d/%d",
				index+1, seen[index].invocation.BatchID, seen[index].invocation.CaseRunID,
				scope.BatchID, scope.CaseRunID)
		}
	}
}
