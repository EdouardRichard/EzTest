// Package agent adapts the P1 agent application services to the P5 mindmap
// AgentRuntime port (spec §4.3/§8.1). It also performs the idempotent P5
// profile seed through the P1 profile repository's EnsureBuiltin, so the
// mindmap context never imports P1 infrastructure. The adapter only maps
// profiles, sessions, runs and status; it never starts a run by itself.
package agent

import (
	"context"
	"errors"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// sessionActivityStatusRunning and sessionActivityStatusIdle are the mindmap
// session activity values derived from the P1 SessionDetail.Running flag.
const (
	sessionActivityStatusRunning = "running"
	sessionActivityStatusIdle    = "idle"
)

// ProfileSeeder is the narrow slice of the P1 profile repository the adapter
// needs for the idempotent P5 seed. *agent/infrastructure/postgres.ProfileRepository
// satisfies it; injecting the interface keeps P1 infrastructure out of the
// mindmap import graph.
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
	Start(ctx context.Context, sessionID int64, message string, actor *int64) (agentdomain.StartResult, error)
	Abort(ctx context.Context, sessionID int64, actor *int64) (agentdomain.AbortResult, error)
}

// AgentRuntimeAdapter implements application.AgentRuntime on top of the P1
// profile, session and run services.
type AgentRuntimeAdapter struct {
	profiles ProfileResolver
	seeder   ProfileSeeder
	sessions AgentSessions
	runs     AgentRuns
}

// NewAgentRuntimeAdapter wires the adapter to the P1 profile resolver, the
// idempotent seed writer, the session service and the run service.
func NewAgentRuntimeAdapter(profiles ProfileResolver, seeder ProfileSeeder, sessions AgentSessions, runs AgentRuns) *AgentRuntimeAdapter {
	return &AgentRuntimeAdapter{profiles: profiles, seeder: seeder, sessions: sessions, runs: runs}
}

var _ mindmapapp.AgentRuntime = (*AgentRuntimeAdapter)(nil)

// EnsureProfile seeds one P5 agent profile through the P1 EnsureBuiltin. The
// seed is idempotent: an existing row is never overwritten, so an admin can
// iterate the prompt/model/tools through the P1 admin API.
func (a *AgentRuntimeAdapter) EnsureProfile(ctx context.Context, profile mindmapapp.ProfileSeed) error {
	if a == nil || a.seeder == nil {
		return errors.New("mindmap: agent profile seeder is not configured")
	}
	_, err := a.seeder.EnsureBuiltin(ctx, profileFromSeed(profile))
	return mapAgentError(err)
}

// CreateSession creates a P1 agent session on the profile resolved from
// profileKey and stamps the version activity channel. The version id is passed
// as the decimal string the P1 activity envelope expects.
func (a *AgentRuntimeAdapter) CreateSession(ctx context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error) {
	if a == nil || a.profiles == nil || a.sessions == nil {
		return 0, errors.New("mindmap: agent sessions are not configured")
	}
	profile, err := a.profiles.GetByKey(ctx, profileKey)
	if err != nil {
		return 0, mapAgentError(err)
	}
	options := agentapp.SessionOptions{ActivityChannel: channel}
	if versionID != "" {
		version := versionID
		options.ActivityVersionID = &version
	}
	session, err := a.sessions.CreateWithOptions(ctx, profile.ID, title, actorPtr(actor), options)
	if err != nil {
		return 0, mapAgentError(err)
	}
	return session.ID, nil
}

// StartText starts one text-only P1 run and returns its run id.
func (a *AgentRuntimeAdapter) StartText(ctx context.Context, sessionID int64, text string, actor int64) (int64, error) {
	if a == nil || a.runs == nil {
		return 0, errors.New("mindmap: agent runs are not configured")
	}
	result, err := a.runs.Start(ctx, sessionID, text, actorPtr(actor))
	if err != nil {
		return 0, mapAgentError(err)
	}
	return result.RunID, nil
}

// GetSession projects the P1 session detail onto the mindmap status read model.
// Status is the derived activity status (running while a run is active, idle
// otherwise); LastRun is the active run when one exists, otherwise the newest
// terminal run, and nil before the first run.
func (a *AgentRuntimeAdapter) GetSession(ctx context.Context, sessionID int64) (mindmapapp.AgentSessionStatus, error) {
	if a == nil || a.sessions == nil {
		return mindmapapp.AgentSessionStatus{}, errors.New("mindmap: agent sessions are not configured")
	}
	detail, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return mindmapapp.AgentSessionStatus{}, mapAgentError(err)
	}
	status := mindmapapp.AgentSessionStatus{
		SessionID: detail.Session.ID,
		Status:    sessionActivityStatus(detail.Running),
	}
	run := detail.ActiveRun
	if run == nil {
		run = detail.LastRun
	}
	if run != nil {
		status.LastRun = &mindmapapp.AgentRunStatus{
			ID:     run.ID,
			Status: string(run.Status),
			Error:  runError(run),
		}
	}
	return status, nil
}

// Abort cancels the session's active P1 run. An idle session is not an error:
// the P1 run service reports "idle" and the mindmap caller treats that as done.
func (a *AgentRuntimeAdapter) Abort(ctx context.Context, sessionID int64, actor int64) error {
	if a == nil || a.runs == nil {
		return errors.New("mindmap: agent runs are not configured")
	}
	_, err := a.runs.Abort(ctx, sessionID, actorPtr(actor))
	return mapAgentError(err)
}

// profileFromSeed converts the mindmap profile seed into the P1 profile shape.
// The tool list is cloned so the seed value never aliases the stored profile.
func profileFromSeed(seed mindmapapp.ProfileSeed) agentdomain.AgentProfile {
	tools := make([]string, len(seed.AllowedTools))
	copy(tools, seed.AllowedTools)
	return agentdomain.AgentProfile{
		Key:                seed.Key,
		Name:               seed.Name,
		Description:        seed.Description,
		Builtin:            seed.Builtin,
		Enabled:            seed.Enabled,
		SystemPrompt:       seed.SystemPrompt,
		Model:              seed.Model,
		AllowedTools:       tools,
		MaxTurns:           seed.MaxTurns,
		RunSeconds:         seed.RunSeconds,
		HardTimeoutSeconds: seed.HardTimeoutSeconds,
		Thinking:           seed.Thinking,
		ReasoningEffort:    seed.ReasoningEffort,
		MaxOutputTokens:    seed.MaxOutputTokens,
	}
}

// sessionActivityStatus maps the P1 running flag onto the mindmap activity
// status.
func sessionActivityStatus(running bool) string {
	if running {
		return sessionActivityStatusRunning
	}
	return sessionActivityStatusIdle
}

// runError returns the failure detail of one P1 run: the model/tool error when
// present, otherwise the abort message.
func runError(run *agentdomain.Run) string {
	if run.ErrorMessage != "" {
		return run.ErrorMessage
	}
	return run.AbortMessage
}

// mapAgentError translates a P1 agent error into the mindmap error kinds and
// reasons (spec §7.4). Unknown failures are returned unchanged.
func mapAgentError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, agentdomain.ErrNotFound):
		return &mindmapdomain.Error{Kind: mindmapdomain.KindNotFound, Reason: "not_found", Message: err.Error()}
	case errors.Is(err, agentdomain.ErrProfileDisabled):
		return mindmapdomain.NewStateConflict("agent_profile_disabled", err.Error())
	case errors.Is(err, agentdomain.ErrRunActive):
		// The P5 single-flight run rule surfaces as the spec §7.4 conflict.
		return mindmapdomain.NewConflict("run_in_progress", err.Error())
	default:
		return err
	}
}

// actorPtr converts the mindmap actor id to the P1 optional actor. A zero actor
// stays nil so system-started runs are not attributed to user 0.
func actorPtr(actor int64) *int64 {
	if actor <= 0 {
		return nil
	}
	return &actor
}
