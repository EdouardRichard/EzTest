package application

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Repository error contract. Repositories return these sentinels (wrapped with
// the offending ids) so the application layer can map them to a spec §6.4
// ReasonError without importing pgx. State-conflict sentinels that already
// exist for the domain live in domain/errors.go and are returned directly, for
// example domain.ErrBatchActive and domain.ErrCaseRunTerminal.
var (
	// ErrBatchNotFound reports a missing or foreign batch.
	ErrBatchNotFound = errors.New("execution: batch not found")
	// ErrCaseRunNotFound reports a missing or foreign case run.
	ErrCaseRunNotFound = errors.New("execution: case run not found")
	// ErrEvidenceNotFound reports a missing evidence row.
	ErrEvidenceNotFound = errors.New("execution: evidence not found")
	// ErrNoPendingCaseRun reports that a batch has no run in pending status.
	ErrNoPendingCaseRun = errors.New("execution: no pending case run")
	// ErrConflict reports a database uniqueness/check conflict that is not one
	// of the domain's more specific state conflicts.
	ErrConflict = errors.New("execution: repository conflict")
)

// BatchRepository persists run_batch rows and the case runs created with a
// batch. Every write that changes identity-relevant state is conditional (CAS):
// a lost race is reported, never silently overwritten.
type BatchRepository interface {
	// CreateBatchWithRuns inserts the batch and every run atomically. The stored
	// case_count is len(runs). It returns domain.ErrBatchActive when another
	// planned/running batch already exists for the version, and ErrConflict for
	// a duplicate (version_id, batch_no) or primary key.
	CreateBatchWithRuns(ctx context.Context, batch domain.RunBatch, runs []domain.CaseRun) (domain.RunBatch, error)
	// GetBatch returns one batch of versionID. A missing or foreign batch is
	// ErrBatchNotFound.
	GetBatch(ctx context.Context, versionID, batchID int64) (domain.RunBatch, error)
	// ListBatches returns one page newest first (id DESC). beforeID is an
	// exclusive id cursor; zero starts at the newest batch. hasMore reports that
	// another page exists.
	ListBatches(ctx context.Context, versionID int64, beforeID int64, limit int) ([]domain.RunBatch, bool, error)
	// UpdateBatchStatus is a status CAS: it applies from -> to only when the
	// stored status is still from, stamps started_at when entering running and
	// finished_at when finishedAt is non-nil. A terminal stored status is
	// domain.ErrBatchFinished; any other mismatch is ErrConflict.
	UpdateBatchStatus(ctx context.Context, batchID int64, from, to domain.BatchStatus, finishedAt *time.Time) (domain.RunBatch, error)
	// SetPaused toggles the pause flag only while the batch is planned/running
	// and only when the flag actually changes. A no-op toggle is
	// domain.ErrAlreadyPaused (pause) or domain.ErrNotPaused (resume); a
	// terminal batch is domain.ErrBatchFinished.
	SetPaused(ctx context.Context, batchID int64, paused bool, mode domain.PauseMode, reason string) (domain.RunBatch, error)
	// UpdateCounts recomputes the stored tally from the batch's case runs
	// instead of accepting caller-supplied counters: case_count is the number
	// of runs and each terminal counter is the runs in that status. Recounts
	// are serialized on the batch row, so concurrent finishers cannot overwrite
	// a fresher tally. Recounting is accepted while the batch is planned or
	// running and also once it is done, so the finish that ends the batch can
	// still settle it; a failed or cancelled batch is domain.ErrBatchFinished.
	// A missing batch is ErrBatchNotFound.
	UpdateCounts(ctx context.Context, batchID int64) (domain.BatchCounts, error)
	// UpdateBatchFailure records the terminal failure reason.
	UpdateBatchFailure(ctx context.Context, batchID int64, reason string) (domain.RunBatch, error)
}

// CaseRunRepository persists case_run rows. The snapshot recorded at creation
// is never rewritten by any method here.
type CaseRunRepository interface {
	// CreateCaseRun inserts one case run with its explicit Snowflake id. A
	// duplicate (batch_id, case_node_id, attempt) or primary key is ErrConflict.
	CreateCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error)
	// GetCaseRun returns one run of batchID. A missing or foreign run is
	// ErrCaseRunNotFound.
	GetCaseRun(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error)
	// GetCaseRunForUpdate is GetCaseRun with SELECT ... FOR UPDATE. It reads the
	// row inside one transaction, so a concurrent finish/pause writer is
	// serialized with the read; multi-statement read-modify-write must still use
	// the SetStatus/FinishCaseRun CAS.
	GetCaseRunForUpdate(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error)
	// ListCaseRuns returns one page newest first (id DESC). An empty status
	// matches every status; beforeID is an exclusive id cursor.
	ListCaseRuns(ctx context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error)
	// NextPending returns the next dispatchable run without changing it: pending
	// runs ordered by snapshot priority P0..P3, then case_node_id ascending.
	// The SELECT is FOR UPDATE SKIP LOCKED so concurrent schedulers never block
	// on the same row. No pending run is ErrNoPendingCaseRun.
	NextPending(ctx context.Context, batchID int64) (domain.CaseRun, error)
	// UpdateCaseRun rewrites every mutable execution column of an existing run
	// (identity, snapshot and attempt are immutable). State transitions must use
	// SetStatus/FinishCaseRun instead.
	UpdateCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error)
	// SetStatus is a status CAS: it applies from -> to only when the stored
	// status is still from, stamps started_at when entering running and
	// finished_at when entering a terminal status. A terminal stored status is
	// domain.ErrCaseRunTerminal; any other mismatch is ErrConflict.
	SetStatus(ctx context.Context, caseRunID int64, from, to domain.CaseRunStatus, at time.Time) (domain.CaseRun, error)
	// FinishCaseRun stores a terminal outcome (status, failure class, reason,
	// summary, finished_at, duration, cleanup flags) with a CAS that requires the
	// stored run to still be running. An interrupted/paused or already terminal run
	// is domain.ErrCaseRunTerminal.
	FinishCaseRun(ctx context.Context, run domain.CaseRun) (domain.CaseRun, error)
}

// StepRepository persists step_result rows.
type StepRepository interface {
	// Upsert inserts the step, or updates the step of the same
	// (case_run_id, seq) in place and returns the stored row (the existing id is
	// kept).
	Upsert(ctx context.Context, step domain.StepResult) (domain.StepResult, error)
	// ListByCaseRun returns the recorded steps ordered by seq.
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.StepResult, error)
}

// AssertionRepository persists assertion_result rows.
type AssertionRepository interface {
	// Upsert inserts the assertion, or updates the assertion of the same
	// (case_run_id, assertion_id) in place and returns the stored row.
	Upsert(ctx context.Context, assertion domain.AssertionResult) (domain.AssertionResult, error)
	// ListByCaseRun returns the recorded assertions ordered by id.
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.AssertionResult, error)
}

// EvidenceRepository persists immutable evidence metadata. There is no update
// or delete: evidence is append-only (spec §5.7).
type EvidenceRepository interface {
	// Create inserts one evidence row. A nil Metadata is stored as '{}'.
	Create(ctx context.Context, evidence domain.Evidence) (domain.Evidence, error)
	// Get returns one evidence row. A missing row is ErrEvidenceNotFound.
	Get(ctx context.Context, evidenceID int64) (domain.Evidence, error)
	// ListByCaseRun returns the artifacts of one run ordered by created_at, id.
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.Evidence, error)
}

// EvidenceRefRepository persists the links between evidence and the rows it
// supports.
type EvidenceRefRepository interface {
	// Create inserts one evidence link. Repeating the same
	// (evidence_id, ref_kind, ref_id) is idempotent and returns the stored row.
	Create(ctx context.Context, ref domain.EvidenceRef) (domain.EvidenceRef, error)
}
