package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// finishCASQuerier is a querier whose case_run row never matches the UPDATE
// predicate and whose stored status is configurable. It lets the finish CAS be
// exercised without a database: the repository sees a lost CAS and falls back
// to reading the stored status.
type finishCASQuerier struct {
	updateSQL string
	status    domain.CaseRunStatus
}

// Exec implements querier; FinishCaseRun never executes a statement directly.
func (q *finishCASQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec in finish CAS test")
}

// Query implements querier; FinishCaseRun only uses QueryRow.
func (q *finishCASQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query in finish CAS test")
}

// QueryRow records the UPDATE predicate and answers the status fallback read.
func (q *finishCASQuerier) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if strings.HasPrefix(strings.TrimSpace(sql), "UPDATE case_run") {
		q.updateSQL = sql
		return finishCASRow{err: pgx.ErrNoRows}
	}
	if strings.Contains(sql, "SELECT status FROM case_run") {
		return finishCASRow{status: q.status}
	}
	return finishCASRow{err: errors.New("unexpected QueryRow in finish CAS test")}
}

// finishCASRow implements pgx.Row for the one status column or a lost CAS.
type finishCASRow struct {
	status domain.CaseRunStatus
	err    error
}

// Scan implements pgx.Row.
func (r finishCASRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("finish CAS row: %d destinations for one status", len(dest))
	}
	target, ok := dest[0].(*domain.CaseRunStatus)
	if !ok {
		return fmt.Errorf("finish CAS row: destination %T is not a case run status", dest[0])
	}
	*target = r.status
	return nil
}

// TestFinishCaseRunCASRejectsInterruptedCheckpoint pins the T15 residual: a late
// finish must never overwrite an interrupted checkpoint. Recovery, a pause and
// the recorder all store interrupted while the run leaves running, so the finish
// CAS accepts status='running' only; accepting an interrupted row would erase the
// resume checkpoint (cleanup_required, interrupted_step_seq) with a stale
// verdict. The fake row loses the CAS and reports interrupted, so the repository
// must answer ErrCaseRunTerminal, and the captured SQL pins the predicate the
// unit test cannot execute against a real database.
func TestFinishCaseRunCASRejectsInterruptedCheckpoint(t *testing.T) {
	q := &finishCASQuerier{status: domain.CaseRunStatusInterrupted}
	repo := &CaseRunRepository{q: q}

	_, err := repo.FinishCaseRun(context.Background(), domain.CaseRun{
		ID:     77,
		Status: domain.CaseRunStatusFailed,
	})
	if !errors.Is(err, domain.ErrCaseRunTerminal) {
		t.Fatalf("FinishCaseRun(interrupted) error = %v, want ErrCaseRunTerminal", err)
	}
	if q.updateSQL == "" {
		t.Fatal("FinishCaseRun did not issue the finish UPDATE")
	}
	// The whole predicate: only a running row may be finished. An
	// interrupted row is a checkpoint resume owns, not a verdict to overwrite.
	if !strings.Contains(q.updateSQL, "WHERE id = $1 AND status = 'running'") {
		t.Fatalf("finish CAS must accept only a running row, got predicate:\n%s", q.updateSQL)
	}
}
