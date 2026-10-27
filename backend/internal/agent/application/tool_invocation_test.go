package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// invocationTestTool is a domain.Tool that can inspect the execution context.
// The invocation context test uses it to observe what the executor injected
// before calling Execute.
type invocationTestTool struct {
	toolName string
	execute  func(ctx context.Context, args json.RawMessage) (domain.ToolResult, error)
}

// Name implements domain.Tool.
func (t *invocationTestTool) Name() string { return t.toolName }

// Description implements domain.Tool.
func (t *invocationTestTool) Description() string { return "stub tool for invocation tests" }

// InputSchema implements domain.Tool.
func (t *invocationTestTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Execute implements domain.Tool.
func (t *invocationTestTool) Execute(ctx context.Context, args json.RawMessage) (domain.ToolResult, error) {
	if t.execute != nil {
		return t.execute(ctx, args)
	}
	return domain.ToolResult{Content: "ok"}, nil
}

// TestToolInvocationContext pins the P1 P3-compatibility contract: the executor
// must expose SessionID/RunID/ActorUserID/ProfileKey to the tool through the
// context, so P3 tools never read the actor from model-supplied arguments
// (spec §8.2 item 3, §8.7).
func TestToolInvocationContext(t *testing.T) {
	actor := int64(42)
	var seen application.Invocation
	var seenOK bool
	stub := &invocationTestTool{
		toolName: "test.echo",
		execute: func(ctx context.Context, _ json.RawMessage) (domain.ToolResult, error) {
			seen, seenOK = application.InvocationFromContext(ctx)
			return domain.ToolResult{Content: "ok"}, nil
		},
	}
	exec := application.NewToolExecutor(application.NewToolRegistry(stub), nil, time.Second)

	got := exec.Execute(context.Background(), application.ToolCallInput{
		RunID:       7,
		SessionID:   9,
		ActorUserID: &actor,
		ProfileKey:  "clarification-dialog",
		Call:        domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: `{}`},
		Allowed:     []string{"test.echo"},
	})
	if got.Status != application.ToolStatusOK || got.Result.IsError {
		t.Fatalf("execution=%+v want a successful tool call", got)
	}
	if !seenOK {
		t.Fatal("InvocationFromContext returned ok=false inside the tool, the executor did not inject the invocation")
	}
	if seen.SessionID != 9 || seen.RunID != 7 {
		t.Fatalf("invocation session=%d run=%d want 9/7", seen.SessionID, seen.RunID)
	}
	if seen.ActorUserID == nil || *seen.ActorUserID != actor {
		t.Fatalf("invocation actor=%v want %d", seen.ActorUserID, actor)
	}
	if seen.ProfileKey != "clarification-dialog" {
		t.Fatalf("invocation profile_key=%q want clarification-dialog", seen.ProfileKey)
	}
}

// TestToolInvocationContextIsAuthoritative checks that the executor's own
// ToolCallInput always wins over an invocation that a caller put in the context:
// the run-scoped identity must come from the run, not from an ambient value.
func TestToolInvocationContextIsAuthoritative(t *testing.T) {
	staleActor := int64(1)
	actor := int64(2)
	var seen application.Invocation
	stub := &invocationTestTool{
		toolName: "test.echo",
		execute: func(ctx context.Context, _ json.RawMessage) (domain.ToolResult, error) {
			seen, _ = application.InvocationFromContext(ctx)
			return domain.ToolResult{Content: "ok"}, nil
		},
	}
	exec := application.NewToolExecutor(application.NewToolRegistry(stub), nil, time.Second)
	ctx := application.WithToolInvocation(context.Background(), application.Invocation{
		RunID:       1,
		SessionID:   2,
		ActorUserID: &staleActor,
		ProfileKey:  "runtime-diagnostic",
	})

	got := exec.Execute(ctx, application.ToolCallInput{
		RunID:       7,
		SessionID:   9,
		ActorUserID: &actor,
		ProfileKey:  "requirement-analysis",
		Call:        domain.ToolCall{ID: "call_1", Name: "test.echo", Arguments: `{}`},
		Allowed:     []string{"test.echo"},
	})
	if got.Status != application.ToolStatusOK {
		t.Fatalf("Status=%q want %q", got.Status, application.ToolStatusOK)
	}
	if seen.SessionID != 9 || seen.RunID != 7 || seen.ProfileKey != "requirement-analysis" {
		t.Fatalf("tool saw %+v, want the executor's invocation 9/7/requirement-analysis", seen)
	}
	if seen.ActorUserID == nil || *seen.ActorUserID != actor {
		t.Fatalf("tool saw actor %v, want the executor's actor %d", seen.ActorUserID, actor)
	}
}

// TestInvocationFromContextAbsent documents the empty-context behavior: no
// invocation is reported for a plain or nil context instead of a zero value
// with ok=true.
func TestInvocationFromContextAbsent(t *testing.T) {
	if invocation, ok := application.InvocationFromContext(context.Background()); ok {
		t.Fatalf("InvocationFromContext(background)=%+v,true want false", invocation)
	}
	var nilCtx context.Context
	if invocation, ok := application.InvocationFromContext(nilCtx); ok {
		t.Fatalf("InvocationFromContext(nil)=%+v,true want false", invocation)
	}
}

// TestClarificationProfilesAllowedToolsRegistered verifies the profile seeds
// against the application ToolRegistry: every allowed tool must be registered
// when the 11 P3 tools exist, otherwise the run would silently offer fewer
// tools than the profile declares (spec §8.1).
func TestClarificationProfilesAllowedToolsRegistered(t *testing.T) {
	catalogNames := []string{
		"version.context_get",
		"requirement.update_draft",
		"clarification.question_upsert",
		"clarification.question_answer",
		"decision.propose",
		"relation.propose",
		"attachment.list",
		"attachment.read_text",
		"context.summary_update",
		"code.tree",
		"code.read",
	}
	tools := make([]domain.Tool, 0, len(catalogNames))
	for _, name := range catalogNames {
		tools = append(tools, &invocationTestTool{toolName: name})
	}
	registry := application.NewToolRegistry(tools...)

	profiles := domain.ClarificationProfiles()
	if len(profiles) != 3 {
		t.Fatalf("len(ClarificationProfiles())=%d want 3", len(profiles))
	}
	for _, profile := range profiles {
		if len(profile.AllowedTools) == 0 {
			t.Fatalf("profile %q has no allowed tools", profile.Key)
		}
		for _, name := range profile.AllowedTools {
			if tool, ok := registry.Get(name); !ok || tool == nil {
				t.Errorf("profile %q allows unregistered tool %q", profile.Key, name)
			}
		}
	}
}
