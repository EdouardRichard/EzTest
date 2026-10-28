package domain

import "time"

// Run kinds, mirrored from the case_design_run.kind CHECK constraint.
const (
	RunKindExplore  = "explore"
	RunKindDesign   = "design"
	RunKindReview   = "review"
	RunKindDataPlan = "data_plan"
)

// Run statuses, mirrored from the case_design_run.status CHECK constraint.
const (
	RunQueued   = "queued"
	RunRunning  = "running"
	RunDone     = "done"
	RunFailed   = "failed"
	RunDegraded = "degraded"
)

// Mindmap event types published on the version channel (spec §6.11).
const (
	EventCaseCreated        = "case.created"
	EventCaseUpdated        = "case.updated"
	EventCaseDeleted        = "case.deleted"
	EventCaseReviewed       = "case.reviewed"
	EventCaseBaselineFrozen = "case.baseline_frozen"
	EventCaseRunStarted     = "case.run_started"
	EventCaseRunFinished    = "case.run_finished"
	EventCaseExportReady    = "case.export_ready"
	EventGatePending        = "gate.pending"
	EventGateResolved       = "gate.resolved"
)

// CaseDesignRun is one asynchronous agent run of the mindmap context. Input,
// Result, Stats and Errors are JSONB documents; Errors never carries LLM or
// mindmap body text (D30). IDs are application-generated Snowflakes. BatchID is
// the parent batch of a supplement/incremental_design run (spec §4.2.7); it is
// nil for the legacy design/review/data_plan kinds.
type CaseDesignRun struct {
	ID             int64
	VersionID      int64
	Kind           string
	Status         string
	AgentSessionID *int64
	AgentRunID     *int64
	Model          string
	PromptVersion  string
	Input          []byte
	Result         []byte
	Stats          []byte
	Errors         []byte
	CreatedBy      int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	BatchID        *int64
}

// IsActive reports whether the run still occupies the per-kind single-flight
// slot (queued or running).
func (r CaseDesignRun) IsActive() bool {
	return r.Status == RunQueued || r.Status == RunRunning
}
