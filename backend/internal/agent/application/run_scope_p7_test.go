package application_test

// P7 delivery run scope plumbing (spec §9.1): RunScope gains VersionID,
// SupplementRunID and AnalysisRunID on top of the P6 BatchID/CaseRunID pair.
// These tests pin that the delivery scope given to StartWithScope reaches the
// tool's Invocation unchanged, that the executor forwards it on its own
// (independent of the run service), and that the zero value keeps P1–P6 runs
// behaving exactly as before.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// TestStartWithScopeThreadsDeliveryScopeIntoInvocation pins the full P7 chain:
// StartWithScope → runExecution → tool executor → Invocation. Missing any link
// would leave analysis/supplement tools reading a zero version or analysis id.
func TestStartWithScopeThreadsDeliveryScopeIntoInvocation(t *testing.T) {
	service, _, tool := scopeTestEnvironment(t, 1)

	scope := domain.RunScope{
		VersionID:       71,
		BatchID:         41,
		CaseRunID:       42,
		SupplementRunID: 43,
		AnalysisRunID:   44,
	}
	result, err := service.StartWithScope(context.Background(), 7,
		[]domain.Part{{Type: "text", Text: "分析失败用例"}}, nil, scope)
	if err != nil {
		t.Fatalf("StartWithScope returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	seen := tool.observations()
	if len(seen) != 1 {
		t.Fatalf("tool executions=%d want 1", len(seen))
	}
	if !seen[0].ok {
		t.Fatal("the tool received no Invocation in its context")
	}
	invocation := seen[0].invocation
	if invocation.VersionID != scope.VersionID ||
		invocation.BatchID != scope.BatchID ||
		invocation.CaseRunID != scope.CaseRunID ||
		invocation.SupplementRunID != scope.SupplementRunID ||
		invocation.AnalysisRunID != scope.AnalysisRunID {
		t.Fatalf("invocation version=%d batch=%d case_run=%d supplement_run=%d analysis_run=%d want %d/%d/%d/%d/%d",
			invocation.VersionID, invocation.BatchID, invocation.CaseRunID,
			invocation.SupplementRunID, invocation.AnalysisRunID,
			scope.VersionID, scope.BatchID, scope.CaseRunID,
			scope.SupplementRunID, scope.AnalysisRunID)
	}
	if invocation.RunID != result.RunID {
		t.Fatalf("invocation run=%d want the started run %d", invocation.RunID, result.RunID)
	}
}

// TestToolExecutorForwardsDeliveryScope pins the executor hop alone: a
// ToolCallInput carrying the delivery ids must surface them in the Invocation.
func TestToolExecutorForwardsDeliveryScope(t *testing.T) {
	var seen application.Invocation
	var seenOK bool
	stub := &invocationTestTool{
		toolName: "test.scope_echo",
		execute: func(ctx context.Context, _ json.RawMessage) (domain.ToolResult, error) {
			seen, seenOK = application.InvocationFromContext(ctx)
			return domain.ToolResult{Content: "ok"}, nil
		},
	}
	exec := application.NewToolExecutor(application.NewToolRegistry(stub), nil, time.Second)

	got := exec.Execute(context.Background(), application.ToolCallInput{
		RunID:           7,
		SessionID:       9,
		VersionID:       71,
		BatchID:         41,
		CaseRunID:       42,
		SupplementRunID: 43,
		AnalysisRunID:   44,
		Call:            domain.ToolCall{ID: "call_1", Name: "test.scope_echo", Arguments: `{}`},
		Allowed:         []string{"test.scope_echo"},
	})
	if got.Status != application.ToolStatusOK {
		t.Fatalf("execution status=%q want %q", got.Status, application.ToolStatusOK)
	}
	if !seenOK {
		t.Fatal("the tool received no Invocation in its context")
	}
	want := application.Invocation{
		RunID:           7,
		SessionID:       9,
		VersionID:       71,
		BatchID:         41,
		CaseRunID:       42,
		SupplementRunID: 43,
		AnalysisRunID:   44,
	}
	if seen != want {
		t.Fatalf("invocation=%+v want %+v", seen, want)
	}
}

// TestStartWithScopeZeroKeepsDeliveryScopeZero pins backward compatibility: a
// caller using the zero scope (all P1/P3/P5/P6 entry points do) must still see
// zero delivery ids in the Invocation.
func TestStartWithScopeZeroKeepsDeliveryScopeZero(t *testing.T) {
	service, _, tool := scopeTestEnvironment(t, 1)

	if _, err := service.StartWithScope(context.Background(), 7,
		[]domain.Part{{Type: "text", Text: "检查运行时健康"}}, nil, domain.RunScope{}); err != nil {
		t.Fatalf("StartWithScope returned error: %v", err)
	}
	runTestWaitIdle(t, service, 7)

	seen := tool.observations()
	if len(seen) != 1 {
		t.Fatalf("tool executions=%d want 1", len(seen))
	}
	if !seen[0].ok {
		t.Fatal("the tool received no Invocation in its context")
	}
	invocation := seen[0].invocation
	if invocation.VersionID != 0 || invocation.BatchID != 0 || invocation.CaseRunID != 0 ||
		invocation.SupplementRunID != 0 || invocation.AnalysisRunID != 0 {
		t.Fatalf("zero scope produced invocation version=%d batch=%d case_run=%d supplement_run=%d analysis_run=%d, want all zero",
			invocation.VersionID, invocation.BatchID, invocation.CaseRunID,
			invocation.SupplementRunID, invocation.AnalysisRunID)
	}
}
