package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// visualProfilePatcher is an in-memory ExecutionProfilePatcher.
type visualProfilePatcher struct {
	profile   agentdomain.AgentProfile
	getErr    error
	updateErr error
	updates   []agentdomain.UpdateParams
}

func (p *visualProfilePatcher) GetByKey(context.Context, string) (agentdomain.AgentProfile, error) {
	if p.getErr != nil {
		return agentdomain.AgentProfile{}, p.getErr
	}
	return p.profile, nil
}

func (p *visualProfilePatcher) Update(_ context.Context, id int64, params agentdomain.UpdateParams, _ *int64) (agentdomain.AgentProfile, error) {
	if p.updateErr != nil {
		return agentdomain.AgentProfile{}, p.updateErr
	}
	if id != p.profile.ID {
		return agentdomain.AgentProfile{}, errors.New("unexpected profile id")
	}
	if err := p.profile.Apply(params); err != nil {
		return agentdomain.AgentProfile{}, err
	}
	p.updates = append(p.updates, params)
	return p.profile, nil
}

// TestEnsureExecutionVisualToolAppendsMissingToolAndRule pins the P8 upgrade of
// an already-seeded profile: the tool and the §9.3 rule are appended only when
// missing, and administrator text survives.
func TestEnsureExecutionVisualToolAppendsMissingToolAndRule(t *testing.T) {
	stored := domain.ExecutionProfile()
	stored.ID = 7
	stored.AllowedTools = []string{"execution.case_context"}
	stored.SystemPrompt = "admin-edited prompt"
	patcher := &visualProfilePatcher{profile: stored}

	if err := EnsureExecutionVisualTool(context.Background(), patcher, nil); err != nil {
		t.Fatalf("EnsureExecutionVisualTool: %v", err)
	}
	if len(patcher.updates) != 1 {
		t.Fatalf("updates=%d want 1", len(patcher.updates))
	}
	params := patcher.updates[0]
	if params.AllowedTools == nil {
		t.Fatal("allowed tools were not updated")
	}
	tools := *params.AllowedTools
	if len(tools) != 2 || tools[1] != domain.ExecutionVisualAssertTool {
		t.Fatalf("allowed tools=%v want the visual tool appended", tools)
	}
	if params.SystemPrompt == nil ||
		!strings.Contains(*params.SystemPrompt, "admin-edited prompt") ||
		!strings.Contains(*params.SystemPrompt, ExecutionVisualAssertPromptRule) {
		t.Fatalf("system prompt was not appended to: %v", params.SystemPrompt)
	}

	// Idempotent: a second startup performs no update.
	if err := EnsureExecutionVisualTool(context.Background(), patcher, nil); err != nil {
		t.Fatalf("second EnsureExecutionVisualTool: %v", err)
	}
	if len(patcher.updates) != 1 {
		t.Fatalf("updates after second run=%d want 1", len(patcher.updates))
	}
}

// TestEnsureExecutionVisualToolKeepsCompleteProfile proves a profile that
// already allows the tool and mentions it in the prompt is not written at all.
func TestEnsureExecutionVisualToolKeepsCompleteProfile(t *testing.T) {
	stored := domain.ExecutionProfile()
	stored.ID = 9
	stored.SystemPrompt = "custom prompt using " + domain.ExecutionVisualAssertTool
	patcher := &visualProfilePatcher{profile: stored}
	if err := EnsureExecutionVisualTool(context.Background(), patcher, nil); err != nil {
		t.Fatalf("EnsureExecutionVisualTool: %v", err)
	}
	if len(patcher.updates) != 0 {
		t.Fatalf("updates=%d want none", len(patcher.updates))
	}
}

// TestEnsureExecutionVisualToolFailsLoudly pins the fail-closed startup: an
// unreadable or unwritable profile aborts the seed instead of silently leaving
// the visual capability disabled.
func TestEnsureExecutionVisualToolFailsLoudly(t *testing.T) {
	readErr := &visualProfilePatcher{getErr: errors.New("db down")}
	if err := EnsureExecutionVisualTool(context.Background(), readErr, nil); err == nil {
		t.Fatal("want an error when the profile cannot be read")
	}
	stored := domain.ExecutionProfile()
	stored.ID = 3
	stored.AllowedTools = nil
	stored.SystemPrompt = ""
	writeErr := &visualProfilePatcher{profile: stored, updateErr: errors.New("db down")}
	if err := EnsureExecutionVisualTool(context.Background(), writeErr, nil); err == nil {
		t.Fatal("want an error when the profile cannot be updated")
	}
	if err := EnsureExecutionVisualTool(context.Background(), nil, nil); err == nil {
		t.Fatal("want an error for a nil profile service")
	}
}
