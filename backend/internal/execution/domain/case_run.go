package execution

import (
	"strings"
	"time"
)

// CaseRunStatus is the merged four-state/process state of one case_run
// (P3-D-05): passed/failed/blocked/inconclusive are results; skipped,
// interrupted and superseded are process states.
type CaseRunStatus string

const (
	CaseRunPending      CaseRunStatus = "pending"
	CaseRunRunning      CaseRunStatus = "running"
	CaseRunPaused       CaseRunStatus = "paused"
	CaseRunPassed       CaseRunStatus = "passed"
	CaseRunFailed       CaseRunStatus = "failed"
	CaseRunBlocked      CaseRunStatus = "blocked"
	CaseRunInconclusive CaseRunStatus = "inconclusive"
	CaseRunSkipped      CaseRunStatus = "skipped"
	CaseRunInterrupted  CaseRunStatus = "interrupted"
	CaseRunSuperseded   CaseRunStatus = "superseded"
)

// FailureClass is the nine-value P3 failure taxonomy (P3-D-12).
type FailureClass string

const (
	FailureClassApplication  FailureClass = "application"
	FailureClassEnvironment  FailureClass = "environment"
	FailureClassSession      FailureClass = "session"
	FailureClassModel        FailureClass = "model"
	FailureClassAutomation   FailureClass = "automation"
	FailureClassTimeout      FailureClass = "timeout"
	FailureClassExecutorLost FailureClass = "executor_lost"
	FailureClassBlocked      FailureClass = "blocked"
	FailureClassUnknown      FailureClass = "unknown"
)

// DefaultMaxDispatchAttempts is the P3 redispatch budget (spec §5.5).
const DefaultMaxDispatchAttempts = 3

// Valid reports whether status is a persisted case_run status.
func (s CaseRunStatus) Valid() bool {
	switch s {
	case "", CaseRunPending, CaseRunRunning, CaseRunPaused, CaseRunPassed, CaseRunFailed,
		CaseRunBlocked, CaseRunInconclusive, CaseRunSkipped, CaseRunInterrupted, CaseRunSuperseded:
		return true
	default:
		return false
	}
}

// Active reports whether the case_run still consumes execution capacity.
func (s CaseRunStatus) Active() bool {
	switch s {
	case CaseRunPending, CaseRunRunning, CaseRunPaused:
		return true
	default:
		return false
	}
}

// Terminal reports whether the case_run is in a final state with no possible
// redispatch. interrupted is excluded because §5.5 redispatches it to pending.
func (s CaseRunStatus) Terminal() bool {
	switch s {
	case CaseRunPassed, CaseRunFailed, CaseRunBlocked, CaseRunInconclusive, CaseRunSkipped, CaseRunSuperseded:
		return true
	default:
		return false
	}
}

// Finished reports whether the case_run no longer needs scheduler attention,
// including an interrupted run that is about to be redispatched.
func (s CaseRunStatus) Finished() bool {
	return s.Terminal() || s == CaseRunInterrupted
}

// FourState reports whether the status is one of the four verdict states.
func (s CaseRunStatus) FourState() bool {
	switch s {
	case CaseRunPassed, CaseRunFailed, CaseRunBlocked, CaseRunInconclusive:
		return true
	default:
		return false
	}
}

// CanTransitionTo implements the §4.3 case_run state machine.
func (s CaseRunStatus) CanTransitionTo(next CaseRunStatus) bool {
	switch s {
	case CaseRunPending:
		return next == CaseRunRunning || next == CaseRunPaused || next == CaseRunSuperseded || next == CaseRunSkipped
	case CaseRunRunning:
		return next == CaseRunPaused || next == CaseRunPassed || next == CaseRunFailed ||
			next == CaseRunBlocked || next == CaseRunInconclusive || next == CaseRunInterrupted ||
			next == CaseRunSuperseded
	case CaseRunPaused:
		return next == CaseRunPending || next == CaseRunFailed || next == CaseRunSuperseded
	case CaseRunInterrupted:
		return next == CaseRunPending || next == CaseRunFailed || next == CaseRunSuperseded
	default:
		return false
	}
}

// Valid reports whether class is one of the nine failure classes. The empty
// string is allowed (no failure class).
func (c FailureClass) Valid() bool {
	switch c {
	case "", FailureClassApplication, FailureClassEnvironment, FailureClassSession, FailureClassModel,
		FailureClassAutomation, FailureClassTimeout, FailureClassExecutorLost, FailureClassBlocked,
		FailureClassUnknown:
		return true
	default:
		return false
	}
}

// IsIssueCandidate reports whether the class may produce a P4 issue candidate.
// Only failed(application) qualifies (P3-D-12).
func (c FailureClass) IsIssueCandidate() bool { return c == FailureClassApplication }

// Terminal causes written to case_run.summary.terminal_cause when the agent run
// ended without executing all steps. They are stable codes (the UI localizes
// them) and always name a concrete resource/provider/agent stop, never a generic
// "unknown".
const (
	TerminalCauseTurnBudget   = "turn_budget"
	TerminalCauseTokenBudget  = "token_budget"
	TerminalCauseTimeBudget   = "time_budget"
	TerminalCauseAgentStopped = "agent_stopped"
)

// NextAction is the stable suggested human next step of a terminal run. The
// system never auto-retries a terminal incomplete run: the human decides to
// rerun with a fresh execution budget, resolve the blocker first, review the
// failure, repair the case/visual baseline, or reconfirm a superseded case.
const (
	NextActionRerun          = "rerun"
	NextActionResolveBlocker = "resolve_blocker"
	NextActionReviewFailure  = "review_failure"
	NextActionRepairVisual   = "repair_visual_baseline"
	NextActionRepairCase     = "repair_case_version"
	NextActionReconfirm      = "reconfirm_case_version"
	// NextActionReplanned is written on a superseded run whose replacement was
	// auto-requeued; it tells the human why no manual action is required.
	NextActionReplanned = "case_version_superseded_replanned"
)

// TerminalCauseCaseVersionSuperseded is the stable cause of a run retired
// because its confirmed case version left the executable set.
const TerminalCauseCaseVersionSuperseded = "case_version_superseded"

// TerminalExplanation returns the stable cause and the human next action of a
// terminal case_run. An explicit settlement cause (for example token_budget)
// and an already persisted next action always win. A row settled before those
// fields existed derives the cause from its persisted status, failure class and
// verdict reason, so the API/UI never show an unexplained terminal failure
// (QA Q2). Passed/skipped/non-terminal rows keep whatever they carried.
func TerminalExplanation(status CaseRunStatus, class FailureClass, summary CaseRunSummary) (string, string) {
	cause, nextAction := summary.TerminalCause, summary.NextAction
	switch status {
	case CaseRunFailed, CaseRunBlocked, CaseRunInconclusive:
		if cause == "" {
			cause = persistedTerminalCause(status, class, summary)
		}
		if nextAction == "" {
			nextAction = terminalNextAction(status, cause)
		}
	case CaseRunSuperseded:
		if cause == "" {
			if summary.VerdictReason != "" {
				cause = summary.VerdictReason
			} else {
				cause = TerminalCauseCaseVersionSuperseded
			}
		}
		if nextAction == "" {
			nextAction = NextActionReconfirm
		}
	}
	return cause, nextAction
}

// persistedTerminalCause derives a concrete cause from the terminal data that
// was persisted before terminal_cause existed. An agent failure is reported as
// its concrete failure class (environment/timeout/model/...) instead of the
// generic agent_failure code.
func persistedTerminalCause(status CaseRunStatus, class FailureClass, summary CaseRunSummary) string {
	if reason := strings.TrimSpace(summary.VerdictReason); reason != "" {
		if reason == VerdictReasonAgentFailure && class != "" && class != FailureClassApplication {
			return string(class)
		}
		return reason
	}
	if class != "" {
		return string(class)
	}
	return string(status)
}

// terminalNextAction maps a terminal cause/status onto the human next step. The
// cause-specific guidance wins over the status: a blocked run that recorded a
// failed assertion is a failure to review, not a generic blocker.
func terminalNextAction(status CaseRunStatus, cause string) string {
	switch cause {
	case VerdictReasonAssertionFailed, VerdictReasonStepFailed, string(FailureClassApplication):
		return NextActionReviewFailure
	case VerdictReasonEmptyCase, VerdictReasonNoAssertions:
		return NextActionRepairCase
	case VerdictReasonVisualAssertionNotRun, "missing_visual_baseline", "visual_environment_mismatch":
		return NextActionRepairVisual
	case TerminalCauseCaseVersionSuperseded:
		return NextActionReconfirm
	}
	if status == CaseRunBlocked {
		return NextActionResolveBlocker
	}
	return NextActionRerun
}

// TerminalCauseNeedsHuman reports whether a terminal cause has no automatic
// recovery path and must be handed to a human. Application defects are excluded:
// the P4 issue-candidate pipeline owns them (a "finding" post is emitted
// separately). A superseded run is excluded because the system requeues the
// replacement itself.
func TerminalCauseNeedsHuman(cause string) bool {
	switch cause {
	case "", VerdictReasonAssertionFailed, VerdictReasonStepFailed, string(FailureClassApplication),
		TerminalCauseCaseVersionSuperseded, VerdictReasonPassed:
		return false
	}
	return true
}

// CaseRunUsage is the agent-run resource usage persisted on the terminal
// case_run.summary. QA Q2: the same numbers were previously only visible in
// Agent Center; the execution page needs them next to the terminal explanation.
// All fields are observed values (never estimates).
type CaseRunUsage struct {
	Turns            int `json:"turns,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
}

// NewCaseRunUsage maps one finished agent run's usage onto the summary shape.
// Providers report either prompt/completion or input/output keys; the total is
// taken from total_tokens or derived from the two components. It returns nil
// when the run carried no usage at all, so a clean zero-value is never invented.
func NewCaseRunUsage(turns int, usage map[string]int) *CaseRunUsage {
	prompt := usage["prompt_tokens"]
	if prompt == 0 {
		prompt = usage["input_tokens"]
	}
	completion := usage["completion_tokens"]
	if completion == 0 {
		completion = usage["output_tokens"]
	}
	total := usage["total_tokens"]
	if total == 0 {
		total = prompt + completion
	}
	if total == 0 && prompt == 0 && completion == 0 && turns <= 0 {
		return nil
	}
	return &CaseRunUsage{
		Turns:            turns,
		TotalTokens:      total,
		PromptTokens:     prompt,
		CompletionTokens: completion,
	}
}

// CaseRunDispatchDiagnostic is the dispatch-time executor snapshot persisted in
// case_run.summary["dispatch"] (v0.2.3 X-01). It records which executor the
// scheduler picked for one attempt (owner/endpoint/health/route) so a later
// failure can be attributed without a new table or migration.
type CaseRunDispatchDiagnostic struct {
	OwnerExecutorID int64  `json:"owner_executor_id"`
	Endpoint        string `json:"endpoint,omitempty"`
	ExecutorStatus  string `json:"executor_status,omitempty"`
	LastHeartbeat   string `json:"last_heartbeat,omitempty"`
	ExecutorVersion string `json:"version,omitempty"`
	// RegistryHit is the selection-time flag: true when the scheduler picked a
	// registered executor row (owner id > 0), not a dial-time claim. The
	// dial-route truth is the router-level registry_hit in the client error
	// details (false there means primary-endpoint fallback).
	RegistryHit bool   `json:"registry_hit"`
	DispatchAt  string `json:"dispatch_at,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
}

// CaseRunSummary is the case_run.summary JSONB projection.
type CaseRunSummary struct {
	VerdictReason      string `json:"verdict_reason,omitempty"`
	AssertionTotal     int    `json:"assertion_total"`
	AssertionFailed    int    `json:"assertion_failed"`
	StepTotal          int    `json:"step_total"`
	StepFailed         int    `json:"step_failed"`
	EvidenceCount      int    `json:"evidence_count"`
	VisualSkipped      int    `json:"visual_skipped"`
	VisualPassed       int    `json:"visual_passed"`
	VisualFailed       int    `json:"visual_failed"`
	VisualInconclusive int    `json:"visual_inconclusive"`
	HumanRequestID     int64  `json:"human_request_id,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	// TerminalCause/NextAction explain a terminal incomplete run: which budget
	// (or agent stop) ended it and what the human should do next (QA Q2).
	TerminalCause string `json:"terminal_cause,omitempty"`
	NextAction    string `json:"next_action,omitempty"`
	// Usage is the observed agent-run usage of the terminal run (QA Q2).
	Usage *CaseRunUsage `json:"usage,omitempty"`
	// Dispatch is the latest dispatch-time executor snapshot (v0.2.3 X-01).
	Dispatch *CaseRunDispatchDiagnostic `json:"dispatch,omitempty"`
}

// EvidenceCountValue returns the evidence count as an int.
func (c CaseRunSummary) EvidenceCountValue() int { return c.EvidenceCount }

// CaseRun is one case execution inside a wave.
type CaseRun struct {
	ID              int64
	WaveID          int64
	IntentID        int64
	CaseVersionID   int64
	Status          CaseRunStatus
	Attempts        int
	FailureClass    FailureClass
	Summary         CaseRunSummary
	StartedAt       *time.Time
	FinishedAt      *time.Time
	OwnerInstance   string
	OwnerExecutorID *int64
	DispatchEpoch   int64
	DispatchToken   string
	LeaseUntil      *time.Time
	PausedAt        *time.Time
	PausedBy        *int64
	PauseReason     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SUTContextStatus is the case_run_sut_context lifecycle status.
type SUTContextStatus string

const (
	SUTContextCreating SUTContextStatus = "creating"
	SUTContextActive   SUTContextStatus = "active"
	SUTContextClosed   SUTContextStatus = "closed"
	SUTContextFailed   SUTContextStatus = "failed"
)

// CaseRunSUTContext is one executor context reserved for a (version_sut,
// account_role) pair of a case_run. storage_state is sensitive and never
// returned by the API.
type CaseRunSUTContext struct {
	ID               int64
	CaseRunID        int64
	VersionSUTID     int64
	AccountRole      string
	AccountID        *int64
	BrowserContextID string
	// ExecutorID is the executor_instance that owns BrowserContextID (P8
	// per-executor routing). Nil for historical rows created before P8; those
	// fall back to the primary executor endpoint.
	ExecutorID   *int64
	StorageState map[string]any
	LeaseEpoch   int64
	Status       SUTContextStatus
	ClosedAt     *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Valid reports whether status is a persisted SUT context status.
func (s SUTContextStatus) Valid() bool {
	switch s {
	case SUTContextCreating, SUTContextActive, SUTContextClosed, SUTContextFailed:
		return true
	default:
		return false
	}
}
