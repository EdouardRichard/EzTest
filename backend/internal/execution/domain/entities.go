package domain

import "time"

// RunBatch is one execution batch of a version. A version has at most one
// active batch (planned or running), enforced by uk_run_batch_active.
type RunBatch struct {
	ID                 int64
	VersionID          int64
	BatchNo            int
	Status             BatchStatus
	Paused             bool
	PauseMode          PauseMode
	PauseReason        string
	BaselineHash       string
	CodeBaselineCommit *string
	HeadCommit         *string

	CaseCount         int
	PassedCount       int
	FailedCount       int
	BlockedCount      int
	InconclusiveCount int
	SkippedCount      int
	InterruptedCount  int

	CreatedBy     int64
	StartedAt     *time.Time
	FinishedAt    *time.Time
	FailureReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CaseRun is one attempt at executing a case inside a batch. Its Snapshot is
// immutable once the batch is created.
type CaseRun struct {
	ID           int64
	BatchID      int64
	VersionID    int64
	CaseNodeID   int64
	CaseRevision int
	CaseHash     string
	Snapshot     CaseSnapshot

	Attempt       int
	Status        CaseRunStatus
	FailureClass  *FailureClass
	FailureReason string
	Summary       string

	SessionID         *int64
	SessionSnapshotID *int64
	AgentSessionID    *int64
	AgentRunID        *int64
	RetryOfCaseRunID  *int64

	CleanupRequired    bool
	InterruptedStepSeq *int

	StartedAt  *time.Time
	FinishedAt *time.Time
	DurationMS int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// StepResult is the recorded outcome of one case step.
type StepResult struct {
	ID            int64
	BatchID       int64
	CaseRunID     int64
	Seq           int
	Action        string
	Expected      string
	Actual        string
	Status        StepStatus
	EvidenceCount int
	StartedAt     *time.Time
	FinishedAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// AssertionResult is the recorded outcome of one case assertion. Verdict is the
// assertion-level value (passed, failed, not_run), not the case-level Verdict.
type AssertionResult struct {
	ID            int64
	BatchID       int64
	CaseRunID     int64
	StepResultID  *int64
	AssertionID   int64
	Kind          string
	Target        string
	Expected      string
	Actual        string
	Verdict       string
	FailureReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Evidence is an immutable artifact captured during a case run. UpdatedAt is
// intentionally absent: evidence rows are never updated after creation.
type Evidence struct {
	ID           int64
	BatchID      int64
	CaseRunID    *int64
	StepResultID *int64
	Kind         string
	Path         string
	SHA256       string
	SizeBytes    int64
	Mime         string
	Metadata     map[string]any
	CreatedAt    time.Time
}

// EvidenceRef links an evidence row to the step, assertion, case run, batch,
// report or issue that references it.
type EvidenceRef struct {
	EvidenceID int64
	RefKind    string
	RefID      int64
	CreatedAt  time.Time
}
