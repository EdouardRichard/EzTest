// Package delivery adapts the execution context's stored reads to the P7
// delivery context's consumer ports (reports, failure analysis and the delivery
// agent tools). It lives in the execution infrastructure because every query is
// scoped to execution-owned tables (run_batch, case_run, step_result,
// assertion_result, evidence); the delivery application never reads those tables
// itself (spec §3.2, Agent.md §6.2). The package exposes the adapter types and
// the P7 execution ports backed by the delivery application services; the
// composition root wires them into both sides.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrBatchNotFound reports an unknown execution batch. It mirrors the execution
// application sentinel so the delivery-side adapters can answer not-found
// without importing the execution domain.
var ErrBatchNotFound = errors.New("execution delivery reads: batch not found")

// querier is the subset of pgxpool.Pool used by the read store.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ReadStore is the read-only SQL projection of the execution tables used by the
// P7 consumer ports. Every method is version/batch scoped: a foreign row is
// reported as not found, never as another version's data.
type ReadStore struct {
	q querier
}

// NewReadStore returns the read store backed by pool.
func NewReadStore(pool *pgxpool.Pool) *ReadStore {
	return &ReadStore{q: pool}
}

// FailedCase is one failed(application) case run of a batch, with the evidence
// ids the failure analysis may cite.
type FailedCase struct {
	CaseRunID     int64
	CaseNodeID    int64
	Title         string
	FailureReason string
	EvidenceIDs   []int64
}

// FailedApplicationCases returns the failed(application) case runs of a batch in
// id order. The failure_class filter runs in SQL before LIMIT (spec §5.6), so a
// bounded read still returns up to limit analyzable cases. EvidenceIDs lists
// every evidence row of the run: it is the materialization whitelist.
func (r *ReadStore) FailedApplicationCases(ctx context.Context, versionID, batchID int64, limit int) ([]FailedCase, error) {
	rows, err := r.q.Query(ctx, `
		SELECT cr.id, cr.case_node_id,
		       COALESCE(cr.case_snapshot->'node'->>'title', ''),
		       cr.failure_reason,
		       COALESCE((
		         SELECT array_agg(e.id ORDER BY e.id)
		         FROM evidence e
		         WHERE e.case_run_id = cr.id), '{}'::bigint[])
		FROM case_run cr
		WHERE cr.version_id = $1 AND cr.batch_id = $2
		  AND cr.status = 'failed' AND cr.failure_class = 'application'
		ORDER BY cr.id
		LIMIT $3`, versionID, batchID, limit)
	if err != nil {
		return nil, fmt.Errorf("execution delivery reads: list failed cases: %w", err)
	}
	defer rows.Close()

	cases := make([]FailedCase, 0, limit)
	for rows.Next() {
		var failedCase FailedCase
		if err := rows.Scan(&failedCase.CaseRunID, &failedCase.CaseNodeID,
			&failedCase.Title, &failedCase.FailureReason, &failedCase.EvidenceIDs); err != nil {
			return nil, fmt.Errorf("execution delivery reads: scan failed case: %w", err)
		}
		cases = append(cases, failedCase)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution delivery reads: list failed cases: %w", err)
	}
	return cases, nil
}

// PreviousBatchID resolves the batch immediately before batchID in the version
// (batch_no-1). ok is false when batchID is the first batch of the version; an
// unknown or foreign batch is ErrBatchNotFound.
func (r *ReadStore) PreviousBatchID(ctx context.Context, versionID, batchID int64) (int64, bool, error) {
	var previousID int64
	err := r.q.QueryRow(ctx, `
		SELECT prev.id
		FROM run_batch cur
		JOIN run_batch prev
		  ON prev.version_id = cur.version_id AND prev.batch_no = cur.batch_no - 1
		WHERE cur.id = $1 AND cur.version_id = $2`, batchID, versionID).Scan(&previousID)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if scanErr := r.q.QueryRow(ctx,
			`SELECT true FROM run_batch WHERE id = $1 AND version_id = $2`,
			batchID, versionID).Scan(&exists); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return 0, false, ErrBatchNotFound
			}
			return 0, false, fmt.Errorf("execution delivery reads: check batch: %w", scanErr)
		}
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("execution delivery reads: previous batch: %w", err)
	}
	return previousID, true, nil
}

// ReportBatch is the run_batch projection a delivery report needs.
type ReportBatch struct {
	ID                int64
	VersionID         int64
	BatchNo           int
	Kind              string
	Status            string
	BaselineHash      string
	GraphHash         string
	CodeBaseCommit    string
	HeadCommit        string
	StartedAt         *time.Time
	FinishedAt        *time.Time
	CaseCount         int
	PassedCount       int
	FailedCount       int
	BlockedCount      int
	InconclusiveCount int
	SkippedCount      int
	InterruptedCount  int
}

// GetReportBatch returns one batch of versionID. A missing or foreign batch is
// ErrBatchNotFound.
func (r *ReadStore) GetReportBatch(ctx context.Context, versionID, batchID int64) (ReportBatch, error) {
	var batch ReportBatch
	err := r.q.QueryRow(ctx, `
		SELECT id, version_id, batch_no, kind, status, baseline_hash, graph_hash,
		       COALESCE(code_baseline_commit, ''), COALESCE(head_commit, ''),
		       started_at, finished_at,
		       case_count, passed_count, failed_count, blocked_count,
		       inconclusive_count, skipped_count, interrupted_count
		FROM run_batch
		WHERE id = $1 AND version_id = $2`, batchID, versionID).Scan(
		&batch.ID, &batch.VersionID, &batch.BatchNo, &batch.Kind, &batch.Status,
		&batch.BaselineHash, &batch.GraphHash, &batch.CodeBaseCommit, &batch.HeadCommit,
		&batch.StartedAt, &batch.FinishedAt,
		&batch.CaseCount, &batch.PassedCount, &batch.FailedCount, &batch.BlockedCount,
		&batch.InconclusiveCount, &batch.SkippedCount, &batch.InterruptedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReportBatch{}, ErrBatchNotFound
	}
	if err != nil {
		return ReportBatch{}, fmt.Errorf("execution delivery reads: report batch: %w", err)
	}
	return batch, nil
}

// FlakyCaseIDs returns the case nodes that failed with failure_class=application
// in an attempt and passed in a later attempt, matching the P6 report rule.
func (r *ReadStore) FlakyCaseIDs(ctx context.Context, batchID int64) ([]int64, error) {
	rows, err := r.q.Query(ctx, `
		WITH history AS (
			SELECT case_node_id, attempt, status, failure_class,
			       min(attempt) FILTER (
			         WHERE status = 'failed' AND failure_class = 'application')
			         OVER (PARTITION BY case_node_id) AS first_failure
			FROM case_run
			WHERE batch_id = $1
		)
		SELECT DISTINCT case_node_id
		FROM history
		WHERE status = 'passed' AND first_failure IS NOT NULL AND attempt > first_failure
		ORDER BY case_node_id`, batchID)
	if err != nil {
		return nil, fmt.Errorf("execution delivery reads: flaky case ids: %w", err)
	}
	defer rows.Close()

	flaky := make([]int64, 0)
	for rows.Next() {
		var caseNodeID int64
		if err := rows.Scan(&caseNodeID); err != nil {
			return nil, fmt.Errorf("execution delivery reads: scan flaky case: %w", err)
		}
		flaky = append(flaky, caseNodeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution delivery reads: flaky case ids: %w", err)
	}
	return flaky, nil
}

// ReportCaseRun is the latest attempt of one case node in the batch, with the
// derived step/assertion/evidence counts.
type ReportCaseRun struct {
	CaseRunID      int64
	CaseNodeID     int64
	Title          string
	Source         string
	Status         string
	FailureClass   string
	Attempt        int
	StepCount      int
	AssertionCount int
	EvidenceCount  int
	UpdatedAt      time.Time
}

// LatestReportCaseRuns returns the newest attempt of every case node of the
// batch ordered by case node id, so the report snapshot and its fingerprint are
// deterministic regardless of attempt insertion order.
func (r *ReadStore) LatestReportCaseRuns(ctx context.Context, batchID int64) ([]ReportCaseRun, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, case_node_id, title, source, status, failure_class, attempt,
		       step_count, assertion_count, evidence_count, updated_at
		FROM (
			SELECT DISTINCT ON (cr.case_node_id)
			       cr.id, cr.case_node_id,
			       COALESCE(cr.case_snapshot->'node'->>'title', '') AS title,
			       cr.source, cr.status, COALESCE(cr.failure_class, '') AS failure_class,
			       cr.attempt, cr.updated_at,
			       (SELECT count(*) FROM step_result sr WHERE sr.case_run_id = cr.id) AS step_count,
			       (SELECT count(*) FROM assertion_result ar WHERE ar.case_run_id = cr.id) AS assertion_count,
			       (SELECT count(*) FROM evidence e WHERE e.case_run_id = cr.id) AS evidence_count
			FROM case_run cr
			WHERE cr.batch_id = $1
			ORDER BY cr.case_node_id, cr.attempt DESC, cr.id DESC
		) latest
		ORDER BY case_node_id`, batchID)
	if err != nil {
		return nil, fmt.Errorf("execution delivery reads: latest report case runs: %w", err)
	}
	defer rows.Close()

	runs := make([]ReportCaseRun, 0)
	for rows.Next() {
		var run ReportCaseRun
		if err := rows.Scan(&run.CaseRunID, &run.CaseNodeID, &run.Title, &run.Source,
			&run.Status, &run.FailureClass, &run.Attempt, &run.StepCount,
			&run.AssertionCount, &run.EvidenceCount, &run.UpdatedAt); err != nil {
			return nil, fmt.Errorf("execution delivery reads: scan report case run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution delivery reads: latest report case runs: %w", err)
	}
	return runs, nil
}

// CaseDetailRow is the header of one failed(application) case run.
type CaseDetailRow struct {
	CaseRunID     int64
	CaseNodeID    int64
	Title         string
	Status        string
	FailureClass  string
	FailureReason string
}

// CaseDetail returns the failed(application) case run of (version, batch). A
// foreign, missing or non-analyzable run is found=false.
func (r *ReadStore) CaseDetail(ctx context.Context, versionID, batchID, caseRunID int64) (CaseDetailRow, bool, error) {
	var detail CaseDetailRow
	err := r.q.QueryRow(ctx, `
		SELECT id, case_node_id, COALESCE(case_snapshot->'node'->>'title', ''),
		       status, COALESCE(failure_class, ''), failure_reason
		FROM case_run
		WHERE id = $1 AND version_id = $2 AND batch_id = $3
		  AND status = 'failed' AND failure_class = 'application'`,
		caseRunID, versionID, batchID).Scan(
		&detail.CaseRunID, &detail.CaseNodeID, &detail.Title,
		&detail.Status, &detail.FailureClass, &detail.FailureReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return CaseDetailRow{}, false, nil
	}
	if err != nil {
		return CaseDetailRow{}, false, fmt.Errorf("execution delivery reads: case detail: %w", err)
	}
	return detail, true, nil
}

// CaseStep is one recorded step of a case run detail.
type CaseStep struct {
	Seq           int
	Action        string
	Expected      string
	Actual        string
	Status        string
	EvidenceCount int
}

// CaseAssertion is one recorded assertion of a case run detail.
type CaseAssertion struct {
	AssertionID   int64
	Kind          string
	Target        string
	Expected      string
	Actual        string
	Verdict       string
	FailureReason string
}

// StepResults returns the steps of one case run ordered by seq.
func (r *ReadStore) StepResults(ctx context.Context, caseRunID int64) ([]CaseStep, error) {
	rows, err := r.q.Query(ctx, `
		SELECT seq, action, expected, actual, status, evidence_count
		FROM step_result
		WHERE case_run_id = $1
		ORDER BY seq`, caseRunID)
	if err != nil {
		return nil, fmt.Errorf("execution delivery reads: case steps: %w", err)
	}
	defer rows.Close()

	steps := make([]CaseStep, 0)
	for rows.Next() {
		var step CaseStep
		if err := rows.Scan(&step.Seq, &step.Action, &step.Expected, &step.Actual,
			&step.Status, &step.EvidenceCount); err != nil {
			return nil, fmt.Errorf("execution delivery reads: scan case step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution delivery reads: case steps: %w", err)
	}
	return steps, nil
}

// AssertionResults returns the assertions of one case run ordered by assertion
// id.
func (r *ReadStore) AssertionResults(ctx context.Context, caseRunID int64) ([]CaseAssertion, error) {
	rows, err := r.q.Query(ctx, `
		SELECT assertion_id, kind, target, expected, actual, verdict, failure_reason
		FROM assertion_result
		WHERE case_run_id = $1
		ORDER BY assertion_id`, caseRunID)
	if err != nil {
		return nil, fmt.Errorf("execution delivery reads: case assertions: %w", err)
	}
	defer rows.Close()

	assertions := make([]CaseAssertion, 0)
	for rows.Next() {
		var assertion CaseAssertion
		if err := rows.Scan(&assertion.AssertionID, &assertion.Kind, &assertion.Target,
			&assertion.Expected, &assertion.Actual, &assertion.Verdict,
			&assertion.FailureReason); err != nil {
			return nil, fmt.Errorf("execution delivery reads: scan case assertion: %w", err)
		}
		assertions = append(assertions, assertion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution delivery reads: case assertions: %w", err)
	}
	return assertions, nil
}

// CaseEvidenceIDs returns the evidence ids of one case run ordered by id.
func (r *ReadStore) CaseEvidenceIDs(ctx context.Context, caseRunID int64) ([]int64, error) {
	rows, err := r.q.Query(ctx,
		`SELECT id FROM evidence WHERE case_run_id = $1 ORDER BY id`, caseRunID)
	if err != nil {
		return nil, fmt.Errorf("execution delivery reads: case evidence ids: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("execution delivery reads: scan evidence id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution delivery reads: case evidence ids: %w", err)
	}
	return ids, nil
}

// EvidenceMetadata returns the metadata of the requested evidence ids that
// belong to the batch's case runs. Missing or foreign ids are omitted.
func (r *ReadStore) EvidenceMetadata(ctx context.Context, versionID, batchID int64, evidenceIDs []int64) ([]EvidenceMetaRow, error) {
	metas := make([]EvidenceMetaRow, 0, len(evidenceIDs))
	if len(evidenceIDs) == 0 {
		return metas, nil
	}
	rows, err := r.q.Query(ctx, `
		SELECT e.id, e.kind, e.sha256, e.size_bytes, e.mime
		FROM evidence e
		JOIN case_run cr ON cr.id = e.case_run_id
		WHERE e.id = ANY($1) AND cr.version_id = $2 AND cr.batch_id = $3
		ORDER BY e.id`, evidenceIDs, versionID, batchID)
	if err != nil {
		return nil, fmt.Errorf("execution delivery reads: evidence metadata: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var meta EvidenceMetaRow
		if err := rows.Scan(&meta.EvidenceID, &meta.Kind, &meta.SHA256,
			&meta.SizeBytes, &meta.Mime); err != nil {
			return nil, fmt.Errorf("execution delivery reads: scan evidence metadata: %w", err)
		}
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("execution delivery reads: evidence metadata: %w", err)
	}
	return metas, nil
}

// EvidenceMetaRow is one evidence metadata record (never the artifact bytes).
type EvidenceMetaRow struct {
	EvidenceID int64
	Kind       string
	SHA256     string
	SizeBytes  int64
	Mime       string
}

// StepResultOwner returns the case run owning one step_result id, used by the
// issue-ref validator. A missing id is found=false.
func (r *ReadStore) StepResultOwner(ctx context.Context, stepResultID int64) (caseRunID int64, found bool, err error) {
	err = r.q.QueryRow(ctx, `SELECT case_run_id FROM step_result WHERE id = $1`, stepResultID).Scan(&caseRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("execution delivery reads: step result owner: %w", err)
	}
	return caseRunID, true, nil
}

// BatchSummary is the by-id batch projection used by the mindmap supplement
// gates and the issue-ref validator.
type BatchSummary struct {
	ID        int64
	VersionID int64
	BatchNo   int
	Kind      string
	Status    string
}

// GetBatchByID returns one batch by id alone. A missing row is ErrBatchNotFound.
func (r *ReadStore) GetBatchByID(ctx context.Context, batchID int64) (BatchSummary, error) {
	var batch BatchSummary
	err := r.q.QueryRow(ctx,
		`SELECT id, version_id, batch_no, kind, status FROM run_batch WHERE id = $1`,
		batchID).Scan(&batch.ID, &batch.VersionID, &batch.BatchNo, &batch.Kind, &batch.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return BatchSummary{}, ErrBatchNotFound
	}
	if err != nil {
		return BatchSummary{}, fmt.Errorf("execution delivery reads: batch by id: %w", err)
	}
	return batch, nil
}

// LatestTerminalBatch returns the newest terminal batch of versionID ordered by
// batch_no then id. A version without a terminal batch is ErrBatchNotFound.
func (r *ReadStore) LatestTerminalBatch(ctx context.Context, versionID int64) (BatchSummary, error) {
	var batch BatchSummary
	err := r.q.QueryRow(ctx, `
		SELECT id, version_id, batch_no, kind, status
		FROM run_batch
		WHERE version_id = $1 AND status IN ('done', 'failed', 'cancelled')
		ORDER BY batch_no DESC, id DESC
		LIMIT 1`, versionID).Scan(&batch.ID, &batch.VersionID, &batch.BatchNo, &batch.Kind, &batch.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return BatchSummary{}, ErrBatchNotFound
	}
	if err != nil {
		return BatchSummary{}, fmt.Errorf("execution delivery reads: latest terminal batch: %w", err)
	}
	return batch, nil
}

// LatestBatchResult returns the recent-result summary of one batch: the stored
// tallies plus its current failed/skipped cases. The latest attempt per case node
// is selected before the status filter, so a case that failed and then passed (or
// was skipped) is not reported as a current failure. It is the supplement agent's
// optional "recent results" context (a missing batch is ErrBatchNotFound).
func (r *ReadStore) LatestBatchResult(ctx context.Context, versionID, batchID int64) (BatchResultRow, error) {
	var summary BatchResultRow
	err := r.q.QueryRow(ctx, `
		SELECT id, status, passed_count, failed_count, blocked_count,
		       inconclusive_count, skipped_count
		FROM run_batch
		WHERE id = $1 AND version_id = $2`, batchID, versionID).Scan(
		&summary.BatchID, &summary.Status, &summary.Passed, &summary.Failed,
		&summary.Blocked, &summary.Inconclusive, &summary.Skipped)
	if errors.Is(err, pgx.ErrNoRows) {
		return BatchResultRow{}, ErrBatchNotFound
	}
	if err != nil {
		return BatchResultRow{}, fmt.Errorf("execution delivery reads: batch result summary: %w", err)
	}

	rows, err := r.q.Query(ctx, `
		SELECT case_node_id, title, status, failure_class
		FROM (
			SELECT DISTINCT ON (case_node_id)
			       case_node_id,
			       COALESCE(case_snapshot->'node'->>'title', '') AS title,
			       status,
			       COALESCE(failure_class, '') AS failure_class
			FROM case_run
			WHERE batch_id = $1
			ORDER BY case_node_id, attempt DESC, id DESC
		) latest_attempt
		WHERE status IN ('failed', 'skipped')
		ORDER BY case_node_id`, batchID)
	if err != nil {
		return BatchResultRow{}, fmt.Errorf("execution delivery reads: batch result cases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var resultCase BatchResultCaseRow
		if err := rows.Scan(&resultCase.CaseNodeID, &resultCase.Title,
			&resultCase.Status, &resultCase.FailureClass); err != nil {
			return BatchResultRow{}, fmt.Errorf("execution delivery reads: scan batch result case: %w", err)
		}
		summary.Cases = append(summary.Cases, resultCase)
	}
	if err := rows.Err(); err != nil {
		return BatchResultRow{}, fmt.Errorf("execution delivery reads: batch result cases: %w", err)
	}
	return summary, nil
}

// BatchResultRow is the recent-result summary of one batch.
type BatchResultRow struct {
	BatchID      int64
	Status       string
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
	Skipped      int
	Cases        []BatchResultCaseRow
}

// BatchResultCaseRow is one failed/skipped case of the summary.
type BatchResultCaseRow struct {
	CaseNodeID   int64
	Title        string
	Status       string
	FailureClass string
}
