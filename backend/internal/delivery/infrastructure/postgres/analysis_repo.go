package deliverypg

import (
	"context"
	"encoding/json"
	"fmt"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5/pgxpool"
)

// delivery_analysis_run columns in scan order.
const analysisColumns = `id, version_id, kind, status, dedupe_key, input_ref,
	output, agent_run_id, error, created_at, updated_at`

const analysisInputRefMax = 200

// analysisKinds is the frozen delivery_analysis_run.kind enum. P4 writes only
// triage; failure/coverage stay reserved (spec §3.4).
var analysisKinds = map[string]bool{
	"failure":  true,
	"coverage": true,
	"triage":   true,
}

const insertAnalysisRunSQL = `
	INSERT INTO delivery_analysis_run (
		id, version_id, kind, status, dedupe_key, input_ref, output,
		agent_run_id, error, created_at, updated_at
	)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	ON CONFLICT (version_id, kind, dedupe_key) WHERE status IN ('queued', 'running') DO NOTHING
	RETURNING ` + analysisColumns

// AnalysisRepository persists delivery_analysis_run rows. It implements
// deliveryapp.AnalysisRepository and owns the database-enforced invariants of
// the analysis lifecycle: one active run per (version, kind, dedupe_key), one
// agent_run per analysis run, and terminal-only completion.
type AnalysisRepository struct {
	pool *pgxpool.Pool
	ids  platformid.Generator
}

var _ deliveryapp.AnalysisRepository = (*AnalysisRepository)(nil)

// NewAnalysisRepository wires the adapter over a pgx pool and the process-wide
// snowflake generator seeded with this replica's registry-allocated node. A
// caller-supplied positive ID is preserved for deterministic replays.
func NewAnalysisRepository(pool *pgxpool.Pool, ids platformid.Generator) *AnalysisRepository {
	if ids == nil {
		panic("deliverypg: analysis repository requires an id generator")
	}
	return &AnalysisRepository{pool: pool, ids: ids}
}

// CreateQueued inserts one queued analysis run. A repeat of the active partial
// unique key (version_id, kind, dedupe_key) WHERE status IN queued/running
// returns the existing active run with no error so StartTriage is idempotent;
// finished runs keep their history and never block a new run.
func (r *AnalysisRepository) CreateQueued(ctx context.Context, run delivery.AnalysisRun) (delivery.AnalysisRun, error) {
	ctx = contextOrBackground(ctx)
	run, err := normalizeQueuedAnalysis(run)
	if err != nil {
		return delivery.AnalysisRun{}, err
	}
	// A conflicting active row can finish between the INSERT and the follow-up
	// SELECT; one retry keeps the idempotent contract without an unbounded loop.
	for attempt := 0; attempt < 2; attempt++ {
		created, err := scanAnalysisRun(r.pool.QueryRow(ctx, insertAnalysisRunSQL,
			r.insertID(run.ID), run.VersionID, run.Kind, string(run.Status),
			run.DedupeKey, run.InputRef, run.Output, run.AgentRunID, run.Error,
			run.CreatedAt, run.UpdatedAt,
		))
		if isNoRows(err) {
			existing, found, findErr := r.findActive(ctx, run)
			if findErr != nil {
				return delivery.AnalysisRun{}, findErr
			}
			if found {
				return existing, nil
			}
			continue
		}
		if err != nil {
			if isUniqueViolation(err) {
				return delivery.AnalysisRun{}, delivery.ErrStateConflict
			}
			return delivery.AnalysisRun{}, wrapDB("create analysis run", err)
		}
		return created, nil
	}
	return delivery.AnalysisRun{}, delivery.ErrStateConflict
}

// MarkRunning binds one agent run to a queued (or not-yet-bound running)
// analysis run. The agent_run_id partial unique index enforces the 1:1 binding;
// rebinding the same pair is idempotent and a terminal run is a state conflict.
func (r *AnalysisRepository) MarkRunning(ctx context.Context, id int64, agentRunID int64) error {
	ctx = contextOrBackground(ctx)
	if id <= 0 || agentRunID <= 0 {
		return delivery.ErrInvalidArgument
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE delivery_analysis_run
		SET status = 'running',
			agent_run_id = $2,
			updated_at = $3
		WHERE id = $1
		  AND (status = 'queued' OR (status = 'running' AND agent_run_id IS NULL))
	`, id, agentRunID, nowUTC())
	if err != nil {
		if isUniqueViolation(err) {
			return delivery.ErrStateConflict
		}
		return wrapDB("mark analysis run running", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	var status string
	var current *int64
	err = r.pool.QueryRow(ctx, `
		SELECT status, agent_run_id FROM delivery_analysis_run WHERE id = $1
	`, id).Scan(&status, &current)
	if isNoRows(err) {
		return delivery.ErrNotFound
	}
	if err != nil {
		return wrapDB("mark analysis run running: load row", err)
	}
	if delivery.AnalysisStatus(status) == delivery.AnalysisStatusRunning && current != nil && *current == agentRunID {
		return nil
	}
	return delivery.ErrStateConflict
}

// Complete writes the terminal status, output JSON and error of one active
// analysis run. Terminal rows are immutable; an empty output is stored as {}.
func (r *AnalysisRepository) Complete(ctx context.Context, id int64, status delivery.AnalysisStatus, output json.RawMessage, errMsg string) error {
	ctx = contextOrBackground(ctx)
	if id <= 0 || !analysisTerminal(status) {
		return delivery.ErrInvalidArgument
	}
	normalized, err := normalizeAnalysisOutput(output)
	if err != nil {
		return err
	}
	if len([]rune(errMsg)) > 2000 {
		return delivery.ErrInvalidArgument
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE delivery_analysis_run
		SET status = $2,
			output = $3,
			error = $4,
			updated_at = $5
		WHERE id = $1 AND status IN ('queued', 'running')
	`, id, string(status), normalized, errMsg, nowUTC())
	if err != nil {
		return wrapDB("complete analysis run", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var current string
	err = r.pool.QueryRow(ctx, `SELECT status FROM delivery_analysis_run WHERE id = $1`, id).Scan(&current)
	if isNoRows(err) {
		return delivery.ErrNotFound
	}
	if err != nil {
		return wrapDB("complete analysis run: load row", err)
	}
	return delivery.ErrStateConflict
}

// LatestByIssue returns the newest finished triage analysis run whose input_ref
// names the issue. In-flight runs are not projected: the issue-detail triage
// takes the latest done/degraded run (spec §5.3). The issue id is the analysis
// input key, not a delivery FK (spec §3.4).
func (r *AnalysisRepository) LatestByIssue(ctx context.Context, issueID int64) (delivery.AnalysisRun, bool, error) {
	ctx = contextOrBackground(ctx)
	if issueID <= 0 {
		return delivery.AnalysisRun{}, false, delivery.ErrInvalidArgument
	}
	run, err := scanAnalysisRun(r.pool.QueryRow(ctx, `
		SELECT `+analysisColumns+`
		FROM delivery_analysis_run
		WHERE kind = 'triage'
		  AND status IN ('done', 'degraded')
		  AND input_ref->>'issue_id' = $1
		ORDER BY id DESC
		LIMIT 1
	`, fmt.Sprintf("%d", issueID)))
	if isNoRows(err) {
		return delivery.AnalysisRun{}, false, nil
	}
	if err != nil {
		return delivery.AnalysisRun{}, false, wrapDB("latest analysis run by issue", err)
	}
	return run, true, nil
}

// findActive loads the active run matched by the partial unique key.
func (r *AnalysisRepository) findActive(ctx context.Context, run delivery.AnalysisRun) (delivery.AnalysisRun, bool, error) {
	found, err := scanAnalysisRun(r.pool.QueryRow(ctx, `
		SELECT `+analysisColumns+`
		FROM delivery_analysis_run
		WHERE version_id = $1 AND kind = $2 AND dedupe_key = $3
		  AND status IN ('queued', 'running')
		ORDER BY id
		LIMIT 1
	`, run.VersionID, run.Kind, run.DedupeKey))
	if isNoRows(err) {
		return delivery.AnalysisRun{}, false, nil
	}
	if err != nil {
		return delivery.AnalysisRun{}, false, wrapDB("load active analysis run", err)
	}
	return found, true, nil
}

func (r *AnalysisRepository) insertID(explicit int64) int64 {
	if explicit > 0 {
		return explicit
	}
	return r.ids.Next()
}

// normalizeQueuedAnalysis validates and defaults the staged analysis row.
func normalizeQueuedAnalysis(run delivery.AnalysisRun) (delivery.AnalysisRun, error) {
	if run.VersionID <= 0 || !analysisKinds[run.Kind] {
		return delivery.AnalysisRun{}, delivery.ErrInvalidArgument
	}
	dedupeKey := run.DedupeKey
	if dedupeKey == "" || len([]rune(dedupeKey)) > analysisInputRefMax {
		return delivery.AnalysisRun{}, delivery.ErrInvalidArgument
	}
	status := run.Status
	if status == "" {
		status = delivery.AnalysisStatusQueued
	}
	if status != delivery.AnalysisStatusQueued {
		return delivery.AnalysisRun{}, delivery.ErrInvalidArgument
	}
	input, err := normalizeAnalysisJSON(run.InputRef)
	if err != nil {
		return delivery.AnalysisRun{}, err
	}
	output, err := normalizeAnalysisJSON(run.Output)
	if err != nil {
		return delivery.AnalysisRun{}, err
	}
	now := nowUTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	if run.AgentRunID != nil && *run.AgentRunID <= 0 {
		return delivery.AnalysisRun{}, delivery.ErrInvalidArgument
	}
	if len([]rune(run.Error)) > 2000 {
		return delivery.AnalysisRun{}, delivery.ErrInvalidArgument
	}
	run.Status = status
	run.InputRef = input
	run.Output = output
	return run, nil
}

// normalizeAnalysisOutput validates the JSON object written by Complete.
func normalizeAnalysisOutput(output json.RawMessage) (json.RawMessage, error) {
	return normalizeAnalysisJSON(output)
}

// normalizeAnalysisJSON accepts an empty value as {} and otherwise requires a
// JSON object (the analysis input/output contract is a JSONB object).
func normalizeAnalysisJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(raw) {
		return nil, delivery.ErrInvalidArgument
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, delivery.ErrInvalidArgument
	}
	return append(json.RawMessage(nil), raw...), nil
}

func analysisTerminal(status delivery.AnalysisStatus) bool {
	switch status {
	case delivery.AnalysisStatusDone, delivery.AnalysisStatusFailed, delivery.AnalysisStatusDegraded:
		return true
	default:
		return false
	}
}

// scanAnalysisRun maps one delivery_analysis_run row.
func scanAnalysisRun(row rowScanner) (delivery.AnalysisRun, error) {
	var (
		run    delivery.AnalysisRun
		status string
		input  []byte
		output []byte
	)
	if err := row.Scan(
		&run.ID, &run.VersionID, &run.Kind, &status, &run.DedupeKey, &input,
		&output, &run.AgentRunID, &run.Error, &run.CreatedAt, &run.UpdatedAt,
	); err != nil {
		return delivery.AnalysisRun{}, err
	}
	run.Status = delivery.AnalysisStatus(status)
	run.InputRef = json.RawMessage(input)
	run.Output = json.RawMessage(output)
	return run, nil
}
