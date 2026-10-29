// Package agent adapts the P1 agent runtime (profiles, sessions, runs) to the
// delivery context's AnalysisAgentRuntime and AnalysisAgentProfileReader ports
// (spec §9.1). The delivery analysis worker only knows those ports: it never
// imports P1 infrastructure.
package agent

import (
	"context"
	"errors"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// sessionActivityStatusRunning and sessionActivityStatusIdle are the activity
// values derived from the P1 SessionDetail.Running flag.
const (
	sessionActivityStatusRunning = "running"
	sessionActivityStatusIdle    = "idle"
)

// ProfileSeeder is the narrow slice of the P1 profile repository the adapter
// needs for the idempotent analysis profile seed. Its EnsureBuiltin is the
// ON CONFLICT (key) DO NOTHING writer, so an administrator-edited row is never
// overwritten. *agent/infrastructure/postgres.ProfileRepository satisfies it.
type ProfileSeeder interface {
	EnsureBuiltin(ctx context.Context, profile agentdomain.AgentProfile) (bool, error)
}

// ProfileResolver is the narrow slice of the P1 profile service the adapter
// needs. *agent/application.ProfileService satisfies it.
type ProfileResolver interface {
	GetByKey(ctx context.Context, key string) (agentdomain.AgentProfile, error)
}

// AgentSessions is the narrow slice of the P1 session service the adapter needs.
// *agent/application.SessionService satisfies it.
type AgentSessions interface {
	CreateWithOptions(ctx context.Context, profileID int64, title string, actor *int64, opts agentapp.SessionOptions) (agentdomain.Session, error)
	Get(ctx context.Context, id int64) (agentdomain.SessionDetail, error)
}

// AgentRuns is the narrow slice of the P1 run service the adapter needs.
// *agent/application.RunService satisfies it.
type AgentRuns interface {
	StartWithScope(ctx context.Context, sessionID int64, parts []agentdomain.Part, actor *int64, scope agentdomain.RunScope) (agentdomain.StartResult, error)
	Abort(ctx context.Context, sessionID int64, actor *int64) (agentdomain.AbortResult, error)
}

// AgentRuntime implements delivery/application.AnalysisAgentRuntime and
// AnalysisAgentProfileReader on top of the P1 profile, session and run services.
type AgentRuntime struct {
	profiles ProfileResolver
	seeder   ProfileSeeder
	sessions AgentSessions
	runs     AgentRuns
}

// NewAgentRuntime wires the adapter to the P1 profile resolver, the idempotent
// seed writer, the session service and the run service.
func NewAgentRuntime(profiles ProfileResolver, seeder ProfileSeeder, sessions AgentSessions, runs AgentRuns) *AgentRuntime {
	return &AgentRuntime{profiles: profiles, seeder: seeder, sessions: sessions, runs: runs}
}

var (
	_ deliveryapp.AnalysisAgentRuntime       = (*AgentRuntime)(nil)
	_ deliveryapp.AnalysisAgentProfileReader = (*AgentRuntime)(nil)
)

// EnsureProfiles idempotently seeds the failure-analysis and issue-triage agent
// profiles (spec §9.1). Every seed is validated first: an invalid seed must fail
// startup rather than be written. The P1 EnsureBuiltin only inserts a missing
// key, so an administrator-edited prompt/model/tool list is never overwritten.
func (a *AgentRuntime) EnsureProfiles(ctx context.Context) error {
	if a == nil || a.seeder == nil {
		return errors.New("delivery: agent profile seeder is not configured")
	}
	seeds := deliverydomain.AnalysisProfiles()
	// The delivery application starts every analysis run by these profile keys;
	// a seed under another key would be unreachable. Fail closed instead.
	if seeds[0].Key != deliveryapp.AnalysisAgentProfileFailure || seeds[1].Key != deliveryapp.AnalysisAgentProfileTriage {
		return errors.New("delivery: analysis profile seeds are out of sync with the application profile keys")
	}
	for _, seed := range seeds {
		if err := seed.Validate(); err != nil {
			return fmt.Errorf("delivery: invalid %s profile seed: %w", seed.Key, err)
		}
		if _, err := a.seeder.EnsureBuiltin(ctx, seed); err != nil {
			return fmt.Errorf("delivery: seed %s profile: %w", seed.Key, err)
		}
	}
	return nil
}

// ProfileModel returns the stored model of one analysis profile so the run (and
// the §7.5 AnalysisView) can expose it. It implements
// delivery/application.AnalysisAgentProfileReader.
func (a *AgentRuntime) ProfileModel(ctx context.Context, profileKey string) (string, error) {
	if a == nil || a.profiles == nil {
		return "", errors.New("delivery: agent profiles are not configured")
	}
	profile, err := a.profiles.GetByKey(ctx, profileKey)
	if err != nil {
		return "", fmt.Errorf("delivery: resolve %s profile: %w", profileKey, err)
	}
	return profile.Model, nil
}

// CreateSession creates one P1 agent session on the profile resolved from
// profileKey and stamps the version activity channel. The version id is passed
// as the decimal string the P1 activity envelope expects.
func (a *AgentRuntime) CreateSession(ctx context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error) {
	if a == nil || a.profiles == nil || a.sessions == nil {
		return 0, errors.New("delivery: agent sessions are not configured")
	}
	profile, err := a.profiles.GetByKey(ctx, profileKey)
	if err != nil {
		return 0, fmt.Errorf("delivery: resolve %s profile: %w", profileKey, err)
	}
	options := agentapp.SessionOptions{ActivityChannel: channel}
	if versionID != "" {
		version := versionID
		options.ActivityVersionID = &version
	}
	session, err := a.sessions.CreateWithOptions(ctx, profile.ID, title, actorPtr(actor), options)
	if err != nil {
		return 0, fmt.Errorf("delivery: create agent session: %w", err)
	}
	return session.ID, nil
}

// Start starts one scoped text run on the analysis profile and returns its run
// id. The scope carries the version, batch and analysis run ids so the analysis
// tools read their owning records from the P1 tool Invocation, never from
// model-supplied arguments (spec §9.1).
func (a *AgentRuntime) Start(ctx context.Context, sessionID int64, instruction string, actor int64, scope deliveryapp.AnalysisAgentRunScope) (int64, error) {
	if a == nil || a.runs == nil {
		return 0, errors.New("delivery: agent runs are not configured")
	}
	if scope.VersionID <= 0 || scope.BatchID <= 0 || scope.AnalysisRunID <= 0 {
		return 0, fmt.Errorf("delivery: analysis run scope needs version, batch and analysis run ids (got %d/%d/%d)",
			scope.VersionID, scope.BatchID, scope.AnalysisRunID)
	}
	result, err := a.runs.StartWithScope(ctx, sessionID,
		[]agentdomain.Part{{Type: "text", Text: instruction}}, actorPtr(actor),
		agentdomain.RunScope{
			VersionID:     scope.VersionID,
			BatchID:       scope.BatchID,
			AnalysisRunID: scope.AnalysisRunID,
		})
	if err != nil {
		return 0, fmt.Errorf("delivery: start analysis agent run: %w", err)
	}
	return result.RunID, nil
}

// GetSession projects the P1 session detail onto the delivery status read model
// polled by the analysis worker. Status is the derived activity status (running
// while a run is active, idle otherwise); LastRun is the active run when one
// exists, otherwise the newest terminal run with its P1 terminal reason (which
// decides degraded vs failed, spec §9.7), and nil before the first run.
func (a *AgentRuntime) GetSession(ctx context.Context, sessionID int64) (deliveryapp.AnalysisAgentSessionStatus, error) {
	if a == nil || a.sessions == nil {
		return deliveryapp.AnalysisAgentSessionStatus{}, errors.New("delivery: agent sessions are not configured")
	}
	detail, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return deliveryapp.AnalysisAgentSessionStatus{}, fmt.Errorf("delivery: read agent session %d: %w", sessionID, err)
	}
	status := deliveryapp.AnalysisAgentSessionStatus{
		SessionID: detail.Session.ID,
		Status:    sessionActivityStatus(detail.Running),
	}
	run := detail.ActiveRun
	if run == nil {
		run = detail.LastRun
	}
	if run != nil {
		status.LastRun = &deliveryapp.AnalysisAgentRunStatus{
			ID:             run.ID,
			Status:         string(run.Status),
			Error:          runErrorMessage(run),
			TerminalReason: string(run.TerminalReason),
		}
	}
	return status, nil
}

// Abort cancels the session's active P1 run. An idle session is not an error:
// the P1 run service reports "idle" and the analysis caller treats that as done.
func (a *AgentRuntime) Abort(ctx context.Context, sessionID int64, actor int64) error {
	if a == nil || a.runs == nil {
		return errors.New("delivery: agent runs are not configured")
	}
	if _, err := a.runs.Abort(ctx, sessionID, actorPtr(actor)); err != nil {
		return fmt.Errorf("delivery: abort agent session %d: %w", sessionID, err)
	}
	return nil
}

// sessionActivityStatus maps the P1 running flag onto the delivery activity
// status.
func sessionActivityStatus(running bool) string {
	if running {
		return sessionActivityStatusRunning
	}
	return sessionActivityStatusIdle
}

// runErrorMessage returns the failure detail of one P1 run: the model/tool error
// when present, otherwise the abort message.
func runErrorMessage(run *agentdomain.Run) string {
	if run.ErrorMessage != "" {
		return run.ErrorMessage
	}
	return run.AbortMessage
}

// actorPtr converts the delivery actor id to the P1 optional actor. A zero actor
// stays nil so system-started runs are not attributed to user 0.
func actorPtr(actor int64) *int64 {
	if actor <= 0 {
		return nil
	}
	return &actor
}
