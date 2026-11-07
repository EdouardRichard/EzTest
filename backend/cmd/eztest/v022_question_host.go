package main

// v0.22 Host question-run validation (Task 37 fix round 1).
//
// The question create/reopen routes receive X-Agent-Run-Id from the Host. A
// logged-in user could forge that header, so the execution QuestionService asks
// this composition-root validator whether the run is real, active, a Host
// profile and scoped to the path version's conversation. The query is
// cross-context (agent_run + version conversation); it lives here in the
// composition root, next to the other cross-context adapters, instead of
// coupling the execution context to the agent/version schemas.

import (
	"context"
	"errors"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// questionHostRunValidator implements executionapp.HostRunValidator.
type questionHostRunValidator struct {
	pool *pgxpool.Pool
}

var _ executionapp.HostRunValidator = questionHostRunValidator{}

// ValidateHostRun resolves one agent run and checks the Host ownership chain:
// the run exists, is running, belongs to a Host profile and its conversation
// belongs to the requested version. Every failure is the same typed forbidden
// response, so a forged header cannot probe which run ids exist.
func (v questionHostRunValidator) ValidateHostRun(ctx context.Context, versionID, agentRunID int64) (executionapp.HostRunScope, error) {
	if v.pool == nil {
		return executionapp.HostRunScope{}, platform.Unavailable("question host run validation is not configured")
	}
	if versionID <= 0 || agentRunID <= 0 {
		return executionapp.HostRunScope{}, platform.Forbidden()
	}
	var (
		profileKey     string
		status         agent.AgentRunStatus
		conversationID *int64
	)
	err := v.pool.QueryRow(ctx, `
		SELECT profile_key, conversation_id, status
		FROM agent_run
		WHERE id = $1
	`, agentRunID).Scan(&profileKey, &conversationID, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return executionapp.HostRunScope{}, platform.Forbidden()
		}
		return executionapp.HostRunScope{}, err
	}
	if status != agent.AgentRunRunning || conversationID == nil || !isQuestionHostProfile(profileKey) {
		return executionapp.HostRunScope{}, platform.Forbidden()
	}
	var conversationVersion int64
	err = v.pool.QueryRow(ctx, `
		SELECT version_id FROM conversation WHERE id = $1
	`, *conversationID).Scan(&conversationVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return executionapp.HostRunScope{}, platform.Forbidden()
		}
		return executionapp.HostRunScope{}, err
	}
	if conversationVersion != versionID {
		return executionapp.HostRunScope{}, platform.Forbidden()
	}
	return executionapp.HostRunScope{AgentRunID: agentRunID, ConversationID: *conversationID}, nil
}

// isQuestionHostProfile reports whether the run's profile is allowed to write
// structured questions. The two conversation Host profiles are the only ones
// with the question.ask tool; every other profile is rejected.
func isQuestionHostProfile(profileKey string) bool {
	return profileKey == agentapp.ProfileKeyClarificationHost ||
		profileKey == agentapp.ProfileKeyDesignHost
}
