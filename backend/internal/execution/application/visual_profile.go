package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ExecutionVisualAssertPromptRule is the design spec §9.3 rule appended
// idempotently to an already-seeded execution-agent prompt: visual assertions
// run through the dedicated tool and the prescribed finish verdicts follow the
// four-state classification.
const ExecutionVisualAssertPromptRule = "视觉断言必须用 execution.visual_assert 执行；无审批基线 finish inconclusive(case)，视口/DPR 不符 finish inconclusive(environment)，像素差异超阈值 finish failed(application)。"

// ExecutionProfilePatcher is the P1 profile service slice the idempotent
// execution-tool upgrade needs; *agent/application.ProfileService satisfies it.
type ExecutionProfilePatcher interface {
	GetByKey(ctx context.Context, key string) (agentdomain.AgentProfile, error)
	Update(ctx context.Context, id int64, params agentdomain.UpdateParams, actor *int64) (agentdomain.AgentProfile, error)
}

// EnsureExecutionVisualTool upgrades the stored execution-agent profile with the
// P8 visual assertion tool and its prompt rule (design spec §9.3). It appends
// only the missing parts, so administrator edits (prompt text, model, budgets)
// survive a restart. A read or write failure is fatal: a silently unpatched
// profile would offer the agent the visual assertion tool nowhere.
func EnsureExecutionVisualTool(ctx context.Context, patcher ExecutionProfilePatcher, logger *slog.Logger) error {
	if patcher == nil {
		return errors.New("P8 execution visual_assert profile patch: the profile service is not configured")
	}
	seed := domain.ExecutionProfile()
	stored, err := patcher.GetByKey(ctx, seed.Key)
	if err != nil {
		return errors.New("P8 execution visual_assert profile patch: read profile " +
			seed.Key + ": " + err.Error())
	}
	params := agentdomain.UpdateParams{}
	if !containsTool(stored.AllowedTools, domain.ExecutionVisualAssertTool) {
		allowedTools := append(append([]string(nil), stored.AllowedTools...),
			domain.ExecutionVisualAssertTool)
		params.AllowedTools = &allowedTools
	}
	if !strings.Contains(stored.SystemPrompt, domain.ExecutionVisualAssertTool) {
		prompt := strings.TrimRight(stored.SystemPrompt, "\n")
		if prompt == "" {
			prompt = ExecutionVisualAssertPromptRule
		} else {
			prompt += "\n" + ExecutionVisualAssertPromptRule
		}
		params.SystemPrompt = &prompt
	}
	if params.AllowedTools == nil && params.SystemPrompt == nil {
		return nil
	}
	if _, err := patcher.Update(ctx, stored.ID, params, nil); err != nil {
		return errors.New("P8 execution visual_assert profile patch: update profile " +
			seed.Key + ": " + err.Error())
	}
	if logger != nil {
		logger.Info("patched execution profile for visual assertions", "key", seed.Key)
	}
	return nil
}

// containsTool reports whether the allowlist already names tool.
func containsTool(tools []string, tool string) bool {
	for _, candidate := range tools {
		if candidate == tool {
			return true
		}
	}
	return false
}
