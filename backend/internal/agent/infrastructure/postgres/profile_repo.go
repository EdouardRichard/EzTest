package agentpg

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// UpdateProfile applies one partial profile PATCH inside a row lock. It
// returns the stored profile and whether any writable field actually changed;
// a no-op PATCH never bumps updated_at.
func (r *RuntimeRepository) UpdateProfile(ctx context.Context, key string, update agent.ProfileUpdate) (agent.AgentProfile, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return agent.AgentProfile{}, false, fmt.Errorf("begin profile update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanProfile(tx.QueryRow(ctx, `
		SELECT `+profileColumns+` FROM agent_profile WHERE key = $1 FOR UPDATE
	`, key))
	if err != nil {
		return agent.AgentProfile{}, false, wrapDB("lock agent profile", err)
	}

	next := current
	if update.SetModel {
		var model *string
		if update.Model != nil {
			trimmed := strings.TrimSpace(*update.Model)
			if trimmed != "" {
				model = &trimmed
			}
		}
		next.Model = model
	}
	if update.Budgets != nil {
		next.Budgets = *update.Budgets
	}
	if update.AllowedTools != nil {
		next.AllowedTools = append([]string(nil), (*update.AllowedTools)...)
	}
	if next.AllowedTools == nil {
		next.AllowedTools = []string{}
	}

	changed := !sameModel(current.Model, next.Model) ||
		current.Budgets != next.Budgets ||
		!sameStringSlice(current.AllowedTools, next.AllowedTools)
	if !changed {
		if err := tx.Commit(ctx); err != nil {
			return agent.AgentProfile{}, false, fmt.Errorf("commit profile no-op: %w", err)
		}
		return current, false, nil
	}

	encodedTools, err := encodeJSONArray("allowed tools", next.AllowedTools)
	if err != nil {
		return agent.AgentProfile{}, false, err
	}
	encodedBudgets, err := encodeJSONObject("profile budgets", next.Budgets)
	if err != nil {
		return agent.AgentProfile{}, false, err
	}
	stored, err := scanProfile(tx.QueryRow(ctx, `
		UPDATE agent_profile
		SET model = $2, budgets = $3::jsonb, allowed_tools = $4::jsonb, updated_at = now()
		WHERE key = $1
		RETURNING `+profileColumns,
		key, next.Model, encodedBudgets, encodedTools))
	if err != nil {
		return agent.AgentProfile{}, false, wrapDB("update agent profile", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agent.AgentProfile{}, false, fmt.Errorf("commit profile update: %w", err)
	}
	return stored, true, nil
}

func sameModel(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
