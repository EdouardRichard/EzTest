package executionpg

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

const waveColumns = `id, version_id, status, trigger, counts, started_at, finished_at,
	paused_at, paused_by, pause_reason, pause_mode, report_id, created_at, updated_at`

// WaveRepository implements executionapp.WaveRepository.
type WaveRepository struct{ base }

// CreateWithCaseRuns writes a wave, claims its intents and inserts the pending
// case_runs in one transaction. It is the only wave creation path so the
// single-active-wave partial unique index can never be bypassed.
func (r *WaveRepository) CreateWithCaseRuns(ctx context.Context, wave execution.RunWave, intents []execution.ExecutionIntent, caseRuns []execution.CaseRun) error {
	ctx = contextOrBackground(ctx)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return wrapDB("create wave: begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	counts, err := encodeJSONObject("counts", wave.Counts.Normalize())
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_wave (id, version_id, status, trigger, counts)
		VALUES ($1, $2, $3, $4, $5::jsonb)
	`, r.insertID(wave.ID), wave.VersionID, string(wave.Status), wave.Trigger, counts); err != nil {
		return wrapDB("insert run wave", err)
	}

	for _, intent := range intents {
		tag, err := tx.Exec(ctx, `
			UPDATE execution_intent SET status = 'claimed', updated_at = now()
			WHERE id = $1 AND status = 'open'
		`, intent.ID)
		if err != nil {
			return wrapDB("claim intent for wave", err)
		}
		if tag.RowsAffected() == 0 {
			return execution.ErrStateConflict
		}
	}

	for _, caseRun := range caseRuns {
		summary, err := encodeJSONObject("summary", caseRun.Summary)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts,
				failure_class, summary, dispatch_epoch, dispatch_token)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
		`, r.insertID(caseRun.ID), waveIDOf(caseRun, wave), caseRun.IntentID, caseRun.CaseVersionID,
			string(caseRun.Status), caseRun.Attempts, string(caseRun.FailureClass), summary,
			caseRun.DispatchEpoch, caseRun.DispatchToken); err != nil {
			return wrapDB("insert case run", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapDB("create wave: commit", err)
	}
	return nil
}

func waveIDOf(caseRun execution.CaseRun, wave execution.RunWave) int64 {
	if caseRun.WaveID > 0 {
		return caseRun.WaveID
	}
	return wave.ID
}

// Get loads one wave.
func (r *WaveRepository) Get(ctx context.Context, id int64) (execution.RunWave, error) {
	ctx = contextOrBackground(ctx)
	wave, err := scanWave(r.pool.QueryRow(ctx, `SELECT `+waveColumns+` FROM run_wave WHERE id = $1`, id))
	if err != nil {
		return execution.RunWave{}, wrapDB("get run wave", err)
	}
	return wave, nil
}

// GetActive returns the active wave of a version, or nil when there is none.
func (r *WaveRepository) GetActive(ctx context.Context, versionID int64) (*execution.RunWave, error) {
	ctx = contextOrBackground(ctx)
	wave, err := scanWave(r.pool.QueryRow(ctx, `
		SELECT `+waveColumns+` FROM run_wave
		WHERE version_id = $1 AND status IN ('planned', 'running', 'paused')
		ORDER BY id DESC LIMIT 1
	`, versionID))
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, wrapDB("get active wave", err)
	}
	return &wave, nil
}

// List returns one descending-ID page of a version's waves.
func (r *WaveRepository) List(ctx context.Context, versionID int64, limit int, beforeID *int64) ([]execution.RunWave, bool, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+waveColumns+` FROM run_wave
		WHERE version_id = $1 AND ($2::BIGINT IS NULL OR id < $2)
		ORDER BY id DESC
		LIMIT $3
	`, versionID, beforeID, fetchLimit(limit))
	if err != nil {
		return nil, false, wrapDB("list run waves", err)
	}
	defer rows.Close()
	items := make([]execution.RunWave, 0)
	for rows.Next() {
		wave, err := scanWave(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, wave)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	limit = effectiveLimit(limit)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

// Update persists the mutable wave columns.
func (r *WaveRepository) Update(ctx context.Context, wave execution.RunWave) (bool, error) {
	ctx = contextOrBackground(ctx)
	counts, err := encodeJSONObject("counts", wave.Counts.Normalize())
	if err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE run_wave SET status = $2, trigger = $3, counts = $4::jsonb,
			started_at = $5, finished_at = $6, paused_at = $7, paused_by = $8,
			pause_reason = $9, pause_mode = $10, updated_at = now()
		WHERE id = $1
	`, wave.ID, string(wave.Status), wave.Trigger, counts, wave.StartedAt, wave.FinishedAt,
		wave.PausedAt, nullableID(wave.PausedBy), wave.PauseReason, wave.PauseMode)
	if err != nil {
		return false, wrapDB("update run wave", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Transition applies a guarded status change plus optional pause/start fields.
func (r *WaveRepository) Transition(ctx context.Context, id int64, from []execution.WaveStatus, to execution.WaveStatus, fields execution.RunWave) (bool, error) {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		UPDATE run_wave SET status = $3, updated_at = now(),
			started_at = COALESCE($4, started_at),
			finished_at = COALESCE($5, finished_at),
			paused_at = COALESCE($6, paused_at),
			paused_by = COALESCE($7, paused_by),
			pause_reason = CASE WHEN $8 = '' THEN pause_reason ELSE $8 END,
			pause_mode = CASE WHEN $9 = '' THEN pause_mode ELSE $9 END
		WHERE id = $1 AND status = ANY($2::text[])
	`, id, statusStrings(from), string(to), fields.StartedAt, fields.FinishedAt, fields.PausedAt,
		nullableID(fields.PausedBy), fields.PauseReason, fields.PauseMode)
	if err != nil {
		return false, wrapDB("transition run wave", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	return false, r.existsOrNotFound(ctx, "run_wave", id, "transition run wave")
}

// Counts recomputes the wave counts from case_run and evidence rows.
func (r *WaveRepository) Counts(ctx context.Context, waveID int64) (execution.WaveCounts, error) {
	ctx = contextOrBackground(ctx)
	counts := execution.WaveCounts{ByVersionSUT: map[string]execution.SUTCounts{}}
	err := r.pool.QueryRow(ctx, `
		SELECT count(*),
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'running'),
			count(*) FILTER (WHERE status = 'paused'),
			count(*) FILTER (WHERE status = 'passed'),
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status = 'blocked'),
			count(*) FILTER (WHERE status = 'inconclusive'),
			count(*) FILTER (WHERE status = 'skipped'),
			count(*) FILTER (WHERE status = 'interrupted'),
			count(*) FILTER (WHERE status = 'superseded')
		FROM case_run WHERE wave_id = $1
	`, waveID).Scan(&counts.Total, &counts.Pending, &counts.Running, &counts.Paused,
		&counts.Passed, &counts.Failed, &counts.Blocked, &counts.Inconclusive,
		&counts.Skipped, &counts.Interrupted, &counts.Superseded)
	if err != nil {
		return execution.WaveCounts{}, wrapDB("count wave case runs", err)
	}
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM evidence e
		JOIN case_run c ON c.id = e.case_run_id
		WHERE c.wave_id = $1
	`, waveID).Scan(&counts.Evidence); err != nil {
		return execution.WaveCounts{}, wrapDB("count wave evidence", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT context.version_sut_id,
			count(DISTINCT run.id),
			count(DISTINCT run.id) FILTER (WHERE run.status = 'passed'),
			count(DISTINCT run.id) FILTER (WHERE run.status = 'failed'),
			count(DISTINCT run.id) FILTER (WHERE run.status = 'blocked'),
			count(DISTINCT run.id) FILTER (WHERE run.status = 'inconclusive')
		FROM case_run_sut_context context
		JOIN case_run run ON run.id = context.case_run_id
		WHERE run.wave_id = $1
		GROUP BY context.version_sut_id
		ORDER BY context.version_sut_id
	`, waveID)
	if err != nil {
		return execution.WaveCounts{}, wrapDB("count wave case runs by version sut", err)
	}
	defer rows.Close()
	for rows.Next() {
		var versionSUTID int64
		var sutCounts execution.SUTCounts
		if err := rows.Scan(&versionSUTID, &sutCounts.Total, &sutCounts.Passed,
			&sutCounts.Failed, &sutCounts.Blocked, &sutCounts.Inconclusive); err != nil {
			return execution.WaveCounts{}, err
		}
		counts.ByVersionSUT[platformid.Format(versionSUTID)] = sutCounts
	}
	if err := rows.Err(); err != nil {
		return execution.WaveCounts{}, err
	}
	return counts, nil
}

// ListActive returns every active wave (recovery sweep).
func (r *WaveRepository) ListActive(ctx context.Context) ([]execution.RunWave, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+waveColumns+` FROM run_wave
		WHERE status IN ('planned', 'running', 'paused')
		ORDER BY id
	`)
	if err != nil {
		return nil, wrapDB("list active waves", err)
	}
	defer rows.Close()
	items := make([]execution.RunWave, 0)
	for rows.Next() {
		wave, err := scanWave(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, wave)
	}
	return items, rows.Err()
}

func scanWave(row rowScanner) (execution.RunWave, error) {
	var wave execution.RunWave
	var status, trigger, pauseMode string
	var countsRaw []byte
	var pausedBy *int64
	err := row.Scan(&wave.ID, &wave.VersionID, &status, &trigger, &countsRaw,
		&wave.StartedAt, &wave.FinishedAt, &wave.PausedAt, &pausedBy,
		&wave.PauseReason, &pauseMode, &wave.ReportID, &wave.CreatedAt, &wave.UpdatedAt)
	if err != nil {
		return execution.RunWave{}, err
	}
	wave.Status = execution.WaveStatus(status)
	wave.Trigger = trigger
	wave.PauseMode = pauseMode
	wave.PausedBy = derefInt64(pausedBy)
	if len(countsRaw) > 0 {
		var counts execution.WaveCounts
		if err := decodeJSONObject("counts", countsRaw, &counts); err != nil {
			return execution.RunWave{}, err
		}
		wave.Counts = counts.Normalize()
	}
	return wave, nil
}
