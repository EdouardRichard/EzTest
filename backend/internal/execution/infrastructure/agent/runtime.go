// Package agent adapts the P1 agent runtime (profiles, sessions, runs) to the
// execution context's AgentRuntimePort (design spec §5.5, §7.3). The execution
// scheduler only knows this port: it never imports P1 infrastructure.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const (
	// sessionActivityStatusRunning and sessionActivityStatusIdle are the
	// Sessions tab activity values derived from the P1 SessionDetail.Running
	// flag.
	sessionActivityStatusRunning = "running"
	sessionActivityStatusIdle    = "idle"
	// sessionTitleMaxRunes mirrors the P1 session title bound.
	sessionTitleMaxRunes = 120
	// listSessionsLimit bounds the Sessions tab page; the port is unpaged.
	listSessionsLimit = 100
	// activityChannelPrefix is the P6 event channel every execution session is
	// stamped with (design spec §5.5).
	activityChannelPrefix = "version:"
)

// ProfileSeeder is the narrow slice of the P1 profile repository the adapter
// needs for the idempotent execution profile seed.
// *agent/infrastructure/postgres.ProfileRepository satisfies it.
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
	ListByChannel(ctx context.Context, channel string, beforeSeq int64, limit int) ([]agentdomain.Session, bool, error)
}

// AgentRuns is the narrow slice of the P1 run service the adapter needs.
// *agent/application.RunService satisfies it.
type AgentRuns interface {
	StartWithScope(ctx context.Context, sessionID int64, parts []agentdomain.Part, actor *int64, scope agentdomain.RunScope) (agentdomain.StartResult, error)
	Abort(ctx context.Context, sessionID int64, actor *int64) (agentdomain.AbortResult, error)
}

// caseLease is the leased session of one case run plus the agent session that
// runs it. The tools need the session's storage state and authentication
// headers, which only the execution CaseRunner holds; the adapter receives it
// with CaseRunRequest and exposes it through SessionForCaseRun.
type caseLease struct {
	agentSessionID int64
	session        executionapp.SessionRef
}

// CaseContextSource is the narrow slice of the execution Recorder the resume
// prompt needs: the pinned case read model including the completed steps'
// actuals/evidence counts and the remaining steps (spec §5.5). The execution
// Recorder satisfies it.
type CaseContextSource interface {
	CaseContext(ctx context.Context, caseRunID int64) (executionapp.CaseContextView, error)
}

// AgentRuntime implements execution/application.AgentRuntimePort on top of the
// P1 profile, session and run services.
type AgentRuntime struct {
	profiles ProfileResolver
	seeder   ProfileSeeder
	sessions AgentSessions
	runs     AgentRuns

	mu         sync.Mutex
	leases     map[int64]caseLease // case run id -> lease
	agents     map[int64]int64     // agent session id -> case run id
	resumeFrom CaseContextSource   // optional resume read model
}

// NewAgentRuntime wires the adapter to the P1 profile resolver, the idempotent
// seed writer, the session service and the run service.
func NewAgentRuntime(profiles ProfileResolver, seeder ProfileSeeder, sessions AgentSessions, runs AgentRuns) *AgentRuntime {
	return &AgentRuntime{
		profiles: profiles,
		seeder:   seeder,
		sessions: sessions,
		runs:     runs,
		leases:   make(map[int64]caseLease),
		agents:   make(map[int64]int64),
	}
}

var _ executionapp.AgentRuntimePort = (*AgentRuntime)(nil)

// SetCaseContextSource wires the resume read model StartCaseRun renders into
// the new run's user message. It is optional (and normally set once at startup
// by the composition root): without a source the prompt falls back to the
// completed seqs carried by CaseRunRequest.
func (a *AgentRuntime) SetCaseContextSource(source CaseContextSource) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resumeFrom = source
}

// caseContextSource returns the configured resume source.
func (a *AgentRuntime) caseContextSource() CaseContextSource {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resumeFrom
}

// EnsureProfile idempotently seeds the execution-agent profile. The P1
// EnsureBuiltin only inserts a missing key, so an administrator-edited row is
// never overwritten. The seed is validated first: an invalid seed must fail
// startup rather than be written.
func (a *AgentRuntime) EnsureProfile(ctx context.Context) error {
	if a == nil || a.seeder == nil {
		return errors.New("execution: agent profile seeder is not configured")
	}
	seed := executiondomain.ExecutionProfile()
	if err := seed.Validate(); err != nil {
		return fmt.Errorf("execution: invalid %s profile seed: %w", seed.Key, err)
	}
	_, err := a.seeder.EnsureBuiltin(ctx, seed)
	return err
}

// StartCaseRun creates one execution agent session on the execution-agent
// profile and starts its run on the version activity channel. The user message
// is the case-context prompt: title, ids, target allowlist, step/assertion
// summaries and the resume checkpoint.
func (a *AgentRuntime) StartCaseRun(ctx context.Context, req executionapp.CaseRunRequest) (executionapp.AgentRunRef, error) {
	if a == nil || a.profiles == nil || a.sessions == nil || a.runs == nil {
		return executionapp.AgentRunRef{}, errors.New("execution: agent runtime is not configured")
	}
	profile, err := a.profiles.GetByKey(ctx, executiondomain.ExecutionProfileKey)
	if err != nil {
		return executionapp.AgentRunRef{}, fmt.Errorf("execution: resolve %s profile: %w", executiondomain.ExecutionProfileKey, err)
	}
	// Read the resume checkpoint before creating the session so a failure cannot
	// leave an orphan session. A run started with a stale checkpoint would
	// re-execute completed steps (spec §5.12 invariant 5).
	var resume *executionapp.CaseContextView
	if source := a.caseContextSource(); source != nil {
		view, err := source.CaseContext(ctx, req.CaseRunID)
		if err != nil {
			return executionapp.AgentRunRef{}, fmt.Errorf("execution: read resume context of case run %d: %w", req.CaseRunID, err)
		}
		resume = &view
	}
	versionID := strconv.FormatInt(req.VersionID, 10)
	session, err := a.sessions.CreateWithOptions(ctx, profile.ID, executionSessionTitle(req), nil, agentapp.SessionOptions{
		ActivityChannel:   activityChannelPrefix + versionID,
		ActivityVersionID: &versionID,
	})
	if err != nil {
		return executionapp.AgentRunRef{}, fmt.Errorf("execution: create agent session: %w", err)
	}

	prompt := caseRunPrompt(req, resume)
	result, err := a.runs.StartWithScope(ctx, session.ID,
		[]agentdomain.Part{{Type: "text", Text: prompt}}, nil,
		agentdomain.RunScope{BatchID: req.BatchID, CaseRunID: req.CaseRunID})
	if err != nil {
		return executionapp.AgentRunRef{}, fmt.Errorf("execution: start agent run: %w", err)
	}
	a.rememberLease(req.CaseRunID, session.ID, req.Session)
	return executionapp.AgentRunRef{SessionID: session.ID, RunID: result.RunID}, nil
}

// SessionForCaseRun returns the leased session captured when the case run
// started. The agent tools use it to initialize the executor context with the
// storage state/sessionStorage and to inject http.request authentication
// headers (design spec §5.9). The composition root wires it as the
// agent_tool.Limits.Session resolver. The lease is forgotten once the agent run
// reaches a terminal state.
func (a *AgentRuntime) SessionForCaseRun(_ context.Context, caseRunID int64) (executionapp.SessionRef, error) {
	if a == nil {
		return executionapp.SessionRef{}, errors.New("execution: agent runtime is not configured")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	lease, ok := a.leases[caseRunID]
	if !ok {
		return executionapp.SessionRef{}, fmt.Errorf("execution: no leased session for case run %d", caseRunID)
	}
	return lease.session, nil
}

// rememberLease stores the session of a started case run, replacing any lease
// left by a previous attempt of the same case run.
func (a *AgentRuntime) rememberLease(caseRunID, agentSessionID int64, session executionapp.SessionRef) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.leases == nil {
		a.leases = make(map[int64]caseLease)
	}
	if a.agents == nil {
		a.agents = make(map[int64]int64)
	}
	if previous, ok := a.leases[caseRunID]; ok {
		delete(a.agents, previous.agentSessionID)
	}
	a.leases[caseRunID] = caseLease{agentSessionID: agentSessionID, session: session}
	a.agents[agentSessionID] = caseRunID
}

// forgetLease drops the lease of a terminal agent run.
func (a *AgentRuntime) forgetLease(agentSessionID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	caseRunID, ok := a.agents[agentSessionID]
	if !ok {
		return
	}
	delete(a.agents, agentSessionID)
	delete(a.leases, caseRunID)
}

// GetRun returns the current state of the execution run of an agent session:
// the active run while it runs, otherwise the newest terminal run.
func (a *AgentRuntime) GetRun(ctx context.Context, agentSessionID int64) (executionapp.AgentRunState, error) {
	if a == nil || a.sessions == nil {
		return executionapp.AgentRunState{}, errors.New("execution: agent sessions are not configured")
	}
	detail, err := a.sessions.Get(ctx, agentSessionID)
	if err != nil {
		return executionapp.AgentRunState{}, err
	}
	run := detail.ActiveRun
	if run == nil {
		run = detail.LastRun
	}
	if run == nil {
		return executionapp.AgentRunState{}, fmt.Errorf("execution: agent session %d has no run", agentSessionID)
	}
	if isTerminalAgentStatus(string(run.Status)) {
		a.forgetLease(agentSessionID)
	}
	return executionapp.AgentRunState{
		RunID:          run.ID,
		Status:         string(run.Status),
		TerminalReason: string(run.TerminalReason),
		Error:          runErrorMessage(run),
		FinishedAt:     run.EndedAt,
	}, nil
}

// CancelRun aborts the session's active run. An idle session is not an error:
// the P1 run service reports "idle" and cancellation is already satisfied.
func (a *AgentRuntime) CancelRun(ctx context.Context, agentSessionID int64) error {
	if a == nil || a.runs == nil {
		return errors.New("execution: agent runs are not configured")
	}
	_, err := a.runs.Abort(ctx, agentSessionID, nil)
	return err
}

// ListSessions lists the execution agent sessions of a version for the Sessions
// tab, newest first as the P1 channel repository orders them.
func (a *AgentRuntime) ListSessions(ctx context.Context, versionID int64) ([]executionapp.AgentSessionRef, error) {
	if a == nil || a.sessions == nil {
		return nil, errors.New("execution: agent sessions are not configured")
	}
	channel := activityChannelPrefix + strconv.FormatInt(versionID, 10)
	sessions, _, err := a.sessions.ListByChannel(ctx, channel, 0, listSessionsLimit)
	if err != nil {
		return nil, err
	}
	refs := make([]executionapp.AgentSessionRef, 0, len(sessions))
	for _, session := range sessions {
		status := sessionActivityStatusIdle
		if detail, err := a.sessions.Get(ctx, session.ID); err == nil && detail.Running {
			status = sessionActivityStatusRunning
		}
		refs = append(refs, executionapp.AgentSessionRef{
			SessionID: session.ID,
			Title:     session.Title,
			Status:    status,
			CreatedAt: session.CreatedAt,
			UpdatedAt: session.UpdatedAt,
		})
	}
	return refs, nil
}

// executionSessionTitle builds the human-readable Sessions tab title of one
// case run, bounded by the P1 title limit.
func executionSessionTitle(req executionapp.CaseRunRequest) string {
	title := strings.TrimSpace(req.CaseNodeTitle)
	if title == "" {
		title = fmt.Sprintf("用例 %d", req.CaseNodeID)
	} else {
		title = fmt.Sprintf("用例 %d：%s", req.CaseNodeID, title)
	}
	runes := []rune(title)
	if len(runes) <= sessionTitleMaxRunes {
		return title
	}
	return string(runes[:sessionTitleMaxRunes])
}

// caseRunPrompt renders the user message of one case run. It carries the pinned
// ids and the resume checkpoint (spec §5.5): the completed steps with their
// actuals/evidence counts, the remaining steps, and whether cleanup is required.
// The agent reads the full snapshot through execution.case_context. resume may
// be nil when the composition root did not wire a CaseContextSource.
func caseRunPrompt(req executionapp.CaseRunRequest, resume *executionapp.CaseContextView) string {
	var builder strings.Builder
	builder.WriteString("执行以下 EzTest 用例。\n")
	fmt.Fprintf(&builder, "用例 %d：%s\n", req.CaseNodeID, strings.TrimSpace(req.CaseNodeTitle))
	fmt.Fprintf(&builder, "case_run_id: %d\n", req.CaseRunID)
	fmt.Fprintf(&builder, "batch_id: %d\n", req.BatchID)
	fmt.Fprintf(&builder, "version_id: %d\n", req.VersionID)
	fmt.Fprintf(&builder, "base_url: %s\n", req.Environment.BaseURL)
	fmt.Fprintf(&builder, "allowed_hosts: %s\n", strings.Join(req.Environment.AllowedHosts, ", "))
	if req.CleanupRequired {
		builder.WriteString("cleanup_required=true：先用浏览器/HTTP 工具完成 cleanup/seed，再继续剩余步骤。\n")
	} else {
		builder.WriteString("cleanup_required=false\n")
	}
	if len(req.CompletedSeqs) > 0 {
		fmt.Fprintf(&builder, "已完成步骤: %s\n", joinSeqs(req.CompletedSeqs))
	}
	if resume != nil && len(resume.CompletedSteps) > 0 {
		builder.WriteString("已完成步骤结果（不要重跑）：\n")
		for _, step := range resume.CompletedSteps {
			actual := strings.TrimSpace(step.Actual)
			if actual == "" {
				actual = "（未填写）"
			}
			fmt.Fprintf(&builder, "- 第 %d 步（%s）：%s（证据 %d 条）\n",
				step.Seq, step.Status, actual, step.EvidenceCount)
		}
	}
	if req.InterruptedStepSeq != nil {
		fmt.Fprintf(&builder, "第 %d 步被中断，恢复时从该步骤继续，不要重跑已完成步骤。\n", *req.InterruptedStepSeq)
	}
	if resume != nil && len(resume.RemainingSteps) > 0 {
		builder.WriteString("剩余步骤：\n")
		for _, step := range resume.RemainingSteps {
			fmt.Fprintf(&builder, "- 第 %d 步：%s（预期：%s）\n", step.Seq, step.Action, step.Expected)
		}
	}
	builder.WriteString("步骤：\n")
	for _, step := range req.Snapshot.Steps {
		fmt.Fprintf(&builder, "- 第 %d 步：%s（预期：%s）\n", step.Seq, step.Action, step.Expected)
	}
	if len(req.Snapshot.Assertions) > 0 {
		builder.WriteString("断言：\n")
		for _, assertion := range req.Snapshot.Assertions {
			fmt.Fprintf(&builder, "- assertion_id %d（第 %d 步，%s %s，预期：%s）\n",
				assertion.ID, assertion.StepSeq, assertion.Kind, assertion.Target, assertion.Expected)
		}
	}
	builder.WriteString("先调用 execution.case_context 获取完整快照；严格按步骤顺序执行，每一步用 execution.step_record 记录，断言用 execution.assertion_record 记录并附证据。\n")
	return builder.String()
}

// joinSeqs renders a completed-step checkpoint list.
func joinSeqs(seqs []int) string {
	parts := make([]string, 0, len(seqs))
	for _, seq := range seqs {
		parts = append(parts, strconv.Itoa(seq))
	}
	return strings.Join(parts, ", ")
}

// runErrorMessage returns the failure detail of one P1 run.
func runErrorMessage(run *agentdomain.Run) string {
	if run == nil {
		return ""
	}
	if run.ErrorMessage != "" {
		return run.ErrorMessage
	}
	return run.AbortMessage
}

// isTerminalAgentStatus reports whether a P1 run status is terminal.
func isTerminalAgentStatus(status string) bool {
	switch status {
	case "done", "failed", "aborted", "interrupted", "canceled", "cancelled":
		return true
	default:
		return false
	}
}
