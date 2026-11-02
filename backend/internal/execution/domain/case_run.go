package execution

import "time"

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
