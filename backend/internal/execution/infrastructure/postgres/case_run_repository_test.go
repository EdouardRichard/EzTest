package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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

// finishPersistQuerier answers the finish UPDATE with a stored row whose
// session columns come from the SET clause parameter, the way PostgreSQL would:
// a column the UPDATE does not set keeps its old NULL value. This exercises the
// repository without a database and catches a missing SET even though the
// RETURNING column list is shared.
type finishPersistQuerier struct {
	sql  string
	args []any
}

// Exec implements querier; FinishCaseRun never executes a statement directly.
func (q *finishPersistQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec in finish persist test")
}

// Query implements querier; FinishCaseRun only uses QueryRow.
func (q *finishPersistQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query in finish persist test")
}

// QueryRow records the finish UPDATE and returns the row PostgreSQL would store.
func (q *finishPersistQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.sql = sql
	q.args = args
	return fakeScanRow{values: []any{
		int64(31), int64(11), int64(22), int64(7), 3, strings.Repeat("d", 64), []byte(`{}`),
		1, domain.CaseRunStatusFailed, nil, "assertion failed", "",
		finishSetParam(sql, args, "session_id"),
		finishSetParam(sql, args, "session_snapshot_id"),
		nil, nil, nil,
		false, nil, nil, nil, int64(0),
		scanTestTime, scanTestTime,
	}}
}

// finishSetParam resolves the argument bound to a "$N" SET expression, or nil
// when the UPDATE does not set that column (an old NULL staying untouched).
func finishSetParam(sql string, args []any, column string) any {
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(column) + ` = \$(\d+)`)
	match := pattern.FindStringSubmatch(sql)
	if match == nil {
		return nil
	}
	position, err := strconv.Atoi(match[1])
	if err != nil || position < 1 || position > len(args) {
		return nil
	}
	return args[position-1]
}

// TestFinishCaseRunPersistsSessionIDs pins the T15 residual that left
// case_run.session_id NULL: the takeover gate scan reads the stored column, so a
// blocked(session) run finished with the acquired session must write it (and the
// snapshot id) in the finish UPDATE, not only in RETURNING. A missing SET clause
// would be invisible to the shared RETURNING/scan and is caught here as a NULL
// stored session.
func TestFinishCaseRunPersistsSessionIDs(t *testing.T) {
	sessionID, snapshotID := int64(51), int64(52)
	q := &finishPersistQuerier{}
	repo := &CaseRunRepository{q: q}

	stored, err := repo.FinishCaseRun(context.Background(), domain.CaseRun{
		ID:                31,
		Status:            domain.CaseRunStatusFailed,
		FailureReason:     "takeover_required",
		SessionID:         &sessionID,
		SessionSnapshotID: &snapshotID,
	})
	if err != nil {
		t.Fatalf("FinishCaseRun: %v", err)
	}
	if stored.SessionID == nil || *stored.SessionID != sessionID {
		t.Fatalf("finished session_id = %v, want %d", stored.SessionID, sessionID)
	}
	if stored.SessionSnapshotID == nil || *stored.SessionSnapshotID != snapshotID {
		t.Fatalf("finished session_snapshot_id = %v, want %d", stored.SessionSnapshotID, snapshotID)
	}
	if !strings.Contains(q.sql, "session_id = $") {
		t.Fatalf("finish UPDATE does not set session_id:\n%s", q.sql)
	}
	if !strings.Contains(q.sql, "session_snapshot_id = $") {
		t.Fatalf("finish UPDATE does not set session_snapshot_id:\n%s", q.sql)
	}

	// A run finished without a session must stay NULL, not retain a stray value.
	q = &finishPersistQuerier{}
	repo = &CaseRunRepository{q: q}
	stored, err = repo.FinishCaseRun(context.Background(), domain.CaseRun{
		ID:     32,
		Status: domain.CaseRunStatusFailed,
	})
	if err != nil {
		t.Fatalf("FinishCaseRun without session: %v", err)
	}
	if stored.SessionID != nil || stored.SessionSnapshotID != nil {
		t.Fatalf("finished nullable session columns = %v/%v, want nil/nil",
			stored.SessionID, stored.SessionSnapshotID)
	}
	if !strings.Contains(q.sql, "session_id = $") {
		t.Fatalf("finish UPDATE does not set session_id for a NULL run:\n%s", q.sql)
	}
}
