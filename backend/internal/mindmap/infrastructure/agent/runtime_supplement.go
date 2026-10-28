// This file extends the mindmap P1 agent-runtime adapter for the P7 supplement
// runs (design spec §9.1): the run must be started through
// AgentRuntime.StartWithScope so the tools can read
// RunScope{VersionID,BatchID,SupplementRunID} from the P1 invocation. The legacy
// P5 kinds keep the unscoped behavior; the session activity channel stays
// version:{id} for every kind.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// supplementRunLookupFailed is the stable reason of a pending-run read failure
// that is not a plain not-found: only a missing run may fall back to the
// unscoped start, every other store error is propagated (and logged with its
// cause) so a broken run store never silently degrades the run scope.
const supplementRunLookupFailed = "supplement_run_lookup_failed"

// ScopedAgentRuns is the P1 run-service slice the supplement runtime needs: the
// unscoped text start for the legacy kinds, StartWithScope for the P7 kinds and
// the abort used by the start compensation path.
type ScopedAgentRuns interface {
	Start(ctx context.Context, sessionID int64, message string, actor *int64) (agentdomain.StartResult, error)
	StartWithScope(ctx context.Context, sessionID int64, parts []agentdomain.Part, actor *int64, scope agentdomain.RunScope) (agentdomain.StartResult, error)
	Abort(ctx context.Context, sessionID int64, actor *int64) (agentdomain.AbortResult, error)
}

// SupplementRunStore is the mindmap run-repository slice the runtime needs to
// resolve the queued supplement run created just before the agent session: the
// P5 port has no start-scope parameter, so the runtime resolves the pending run
// by its per-kind single-flight slot and version.
type SupplementRunStore interface {
	ActiveByKind(ctx context.Context, versionID int64, kind string) (mindmapdomain.CaseDesignRun, error)
}

// pendingSupplement is the session state recorded at CreateSession: the profile
// key decides whether StartText must attach the P7 run scope, and the version
// addresses the pending run.
type pendingSupplement struct {
	profileKey string
	versionID  int64
}

// SupplementRuntime implements mindmap/application.AgentRuntime for the run
// coordinator with the P7 scoped start. It wraps the P5 adapter so profile
// seeding, session handling, status polling and abort stay identical.
type SupplementRuntime struct {
	base  *AgentRuntimeAdapter
	runs  ScopedAgentRuns
	store SupplementRunStore

	mu      sync.Mutex
	pending map[int64]pendingSupplement
}

// NewSupplementRuntime wires the supplement runtime to the P1 profile resolver,
// the idempotent seed writer, the session service, the scoped run service and the
// mindmap run store.
func NewSupplementRuntime(profiles ProfileResolver, seeder ProfileSeeder, sessions AgentSessions, runs ScopedAgentRuns, store SupplementRunStore) *SupplementRuntime {
	return &SupplementRuntime{
		base:    NewAgentRuntimeAdapter(profiles, seeder, sessions, runs),
		runs:    runs,
		store:   store,
		pending: make(map[int64]pendingSupplement),
	}
}

var _ mindmapapp.AgentRuntime = (*SupplementRuntime)(nil)

// EnsureProfile seeds one profile through the shared P5 adapter.
func (a *SupplementRuntime) EnsureProfile(ctx context.Context, profile mindmapapp.ProfileSeed) error {
	if a == nil || a.base == nil {
		return errors.New("mindmap: supplement agent runtime is not configured")
	}
	return a.base.EnsureProfile(ctx, profile)
}

// CreateSession creates the P1 session on the version activity channel and, for
// a supplement-agent session, remembers the version so StartText can attach the
// run scope.
func (a *SupplementRuntime) CreateSession(ctx context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error) {
	if a == nil || a.base == nil {
		return 0, errors.New("mindmap: supplement agent runtime is not configured")
	}
	// Every mindmap agent session lives on the version activity channel; default
	// it here so the P7 contract holds even if a caller omits the channel.
	if strings.TrimSpace(channel) == "" && strings.TrimSpace(versionID) != "" {
		channel = "version:" + strings.TrimSpace(versionID)
	}
	sessionID, err := a.base.CreateSession(ctx, profileKey, title, actor, channel, versionID)
	if err != nil {
		return 0, err
	}
	if profileKey == mindmapapp.ProfileKeySupplement {
		version := int64(0)
		if parsed, parseErr := strconv.ParseInt(versionID, 10, 64); parseErr == nil && parsed > 0 {
			version = parsed
		}
		a.mu.Lock()
		if a.pending == nil {
			a.pending = make(map[int64]pendingSupplement)
		}
		a.pending[sessionID] = pendingSupplement{profileKey: profileKey, versionID: version}
		a.mu.Unlock()
	}
	return sessionID, nil
}

// StartText starts one text run. A supplement-agent session whose queued run can
// be resolved is started through StartWithScope with the P7 run scope; a missing
// pending run (domain.KindNotFound, the agent-start read race) keeps the
// unscoped P5 start so that race never degrades the run. Every other run-store
// failure is propagated with the stable supplement_run_lookup_failed reason.
func (a *SupplementRuntime) StartText(ctx context.Context, sessionID int64, text string, actor int64) (int64, error) {
	if a == nil || a.base == nil {
		return 0, errors.New("mindmap: supplement agent runtime is not configured")
	}
	pending, ok := a.takePending(sessionID)
	if !ok || pending.profileKey != mindmapapp.ProfileKeySupplement || pending.versionID <= 0 {
		return a.base.StartText(ctx, sessionID, text, actor)
	}
	run, found, err := a.activeSupplementRun(ctx, pending.versionID)
	if err != nil {
		return 0, err
	}
	if !found {
		return a.base.StartText(ctx, sessionID, text, actor)
	}
	scope := agentdomain.RunScope{
		VersionID:       run.VersionID,
		SupplementRunID: run.ID,
	}
	if run.BatchID != nil {
		scope.BatchID = *run.BatchID
	}
	result, err := a.runs.StartWithScope(ctx, sessionID,
		[]agentdomain.Part{{Type: "text", Text: text}}, actorPtr(actor), scope)
	if err != nil {
		return 0, mapAgentError(err)
	}
	return result.RunID, nil
}

// GetSession projects the P1 session detail through the shared P5 adapter.
func (a *SupplementRuntime) GetSession(ctx context.Context, sessionID int64) (mindmapapp.AgentSessionStatus, error) {
	if a == nil || a.base == nil {
		return mindmapapp.AgentSessionStatus{}, errors.New("mindmap: supplement agent runtime is not configured")
	}
	return a.base.GetSession(ctx, sessionID)
}

// Abort cancels the session's active P1 run through the shared P5 adapter.
func (a *SupplementRuntime) Abort(ctx context.Context, sessionID int64, actor int64) error {
	if a == nil || a.base == nil {
		return errors.New("mindmap: supplement agent runtime is not configured")
	}
	return a.base.Abort(ctx, sessionID, actor)
}

// takePending consumes the CreateSession state of one session.
func (a *SupplementRuntime) takePending(sessionID int64) (pendingSupplement, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending, ok := a.pending[sessionID]
	if ok {
		delete(a.pending, sessionID)
	}
	return pending, ok
}

// activeSupplementRun returns the active supplement or incremental_design run of
// a version (the per-kind single-flight slot makes at most one of them active in
// practice; a version cannot be executing and continuing at the same time). A
// missing run of one kind falls through to the other kind; any other store error
// is logged with its cause and returned with the stable reason.
func (a *SupplementRuntime) activeSupplementRun(ctx context.Context, versionID int64) (mindmapdomain.CaseDesignRun, bool, error) {
	if a.store == nil {
		return mindmapdomain.CaseDesignRun{}, false, nil
	}
	for _, kind := range []string{mindmapapp.RunKindSupplement, mindmapapp.RunKindIncrementalDesign} {
		run, err := a.store.ActiveByKind(ctx, versionID, kind)
		if err != nil {
			if isRunStoreNotFound(err) {
				continue
			}
			slog.ErrorContext(ctx, "mindmap: read the active supplement run failed",
				"reason", supplementRunLookupFailed, "version_id", versionID, "kind", kind, "error", err)
			return mindmapdomain.CaseDesignRun{}, false, fmt.Errorf("mindmap: %s: %w", supplementRunLookupFailed, err)
		}
		if !run.IsActive() {
			continue
		}
		return run, true, nil
	}
	return mindmapdomain.CaseDesignRun{}, false, nil
}

// isRunStoreNotFound reports whether a run-store read failed because the row is
// absent (the only case in which the runtime may fall back to the unscoped
// start).
func isRunStoreNotFound(err error) bool {
	var domainErr *mindmapdomain.Error
	return errors.As(err, &domainErr) && domainErr != nil && domainErr.Kind == mindmapdomain.KindNotFound
}
