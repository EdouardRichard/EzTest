package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// RunByBatchReader is the P7 by-batch run lookup of spec §6.4: the autonomous
// execution worker knows only the parent batch id, so it reads the newest
// supplement or incremental_design run pointing at that batch. It is a separate
// concrete reader (not a RunRepository method) so the frozen P5 repository port
// and its fakes stay unchanged.
type RunByBatchReader struct {
	q querier
}

// NewRunByBatchReader returns the by-batch run reader backed by pool.
func NewRunByBatchReader(pool *pgxpool.Pool) *RunByBatchReader {
	return &RunByBatchReader{q: pool}
}

// LatestByBatch returns the newest supplement/incremental_design run of one
// parent batch. found=false means the batch has no P7 design run yet; a run
// belonging to another batch is never returned.
func (r *RunByBatchReader) LatestByBatch(ctx context.Context, batchID int64) (domain.CaseDesignRun, bool, error) {
	if r == nil || r.q == nil {
		return domain.CaseDesignRun{}, false, errors.New("postgres: run by batch reader is not configured")
	}
	if batchID <= 0 {
		return domain.CaseDesignRun{}, false, nil
	}
	run, err := scanRun(r.q.QueryRow(ctx, `
		SELECT `+runColumnList+`
		FROM case_design_run
		WHERE batch_id = $1 AND kind IN ('supplement', 'incremental_design')
		ORDER BY id DESC
		LIMIT 1`, batchID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CaseDesignRun{}, false, nil
	}
	if err != nil {
		return domain.CaseDesignRun{}, false, fmt.Errorf("postgres: read latest run of batch %d: %w", batchID, err)
	}
	return run, true, nil
}
