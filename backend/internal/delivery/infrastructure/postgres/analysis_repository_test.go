package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// analysisTestTime is a lossless TIMESTAMPTZ reading.
var analysisTestTime = time.Date(2026, 10, 28, 20, 30, 0, 0, time.UTC)

// analysisScanRow is a rowScanner whose values are assigned to the scan
// destinations. It pins the analysis_run column order without a database.
type analysisScanRow struct{ values []any }

// Scan implements analysisRowScanner.
func (r analysisScanRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("analysis scan row: %d destinations for %d values", len(dest), len(r.values))
	}
	for index := range dest {
		if err := analysisAssignScanValue(dest[index], r.values[index]); err != nil {
			return fmt.Errorf("analysis scan row column %d: %w", index+1, err)
		}
	}
	return nil
}

// analysisAssignScanValue mimics the pgx codecs: nil clears the destination and
// an optional column (pointer destination) is allocated with the value.
func analysisAssignScanValue(dest, value any) error {
	target := reflect.ValueOf(dest)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("destination %T is not a non-nil pointer", dest)
	}
	elem := target.Elem()
	if value == nil {
		elem.Set(reflect.Zero(elem.Type()))
		return nil
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(elem.Type()) {
		elem.Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(elem.Type()) {
		elem.Set(source.Convert(elem.Type()))
		return nil
	}
	if elem.Kind() == reflect.Pointer && source.Type().AssignableTo(elem.Type().Elem()) {
		pointer := reflect.New(elem.Type().Elem())
		pointer.Elem().Set(source)
		elem.Set(pointer)
		return nil
	}
	return fmt.Errorf("cannot assign %T to %s", value, elem.Type())
}

// analysisFakeQuerier is a querier whose row/exec answers are scripted.
type analysisFakeQuerier struct {
	execSQL      string
	execRows     int64
	querySQL     string
	queryValues  [][]any
	queryErr     error
	rowValues    []any
	rowErr       error
	selectStatus string
}

// Exec implements analysisQuerier.
func (q *analysisFakeQuerier) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	q.execSQL = sql
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", q.execRows)), nil
}

// Query implements analysisQuerier.
func (q *analysisFakeQuerier) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	q.querySQL = sql
	if q.queryErr != nil {
		return nil, q.queryErr
	}
	return &analysisFakeRows{values: q.queryValues}, nil
}

// analysisFakeRows implements pgx.Rows over scripted column values.
type analysisFakeRows struct {
	values  [][]any
	index   int
	current []any
	err     error
}

// Close implements pgx.Rows.
func (r *analysisFakeRows) Close() {}

// Err implements pgx.Rows.
func (r *analysisFakeRows) Err() error { return r.err }

// CommandTag implements pgx.Rows.
func (r *analysisFakeRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

// FieldDescriptions implements pgx.Rows.
func (r *analysisFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

// Next implements pgx.Rows.
func (r *analysisFakeRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.current = r.values[r.index]
	r.index++
	return true
}

// Scan implements pgx.Rows.
func (r *analysisFakeRows) Scan(dest ...any) error {
	return analysisScanRow{values: r.current}.Scan(dest...)
}

// Values implements pgx.Rows.
func (r *analysisFakeRows) Values() ([]any, error) { return r.current, nil }

// RawValues implements pgx.Rows.
func (r *analysisFakeRows) RawValues() [][]byte { return nil }

// Conn implements pgx.Rows.
func (r *analysisFakeRows) Conn() *pgx.Conn { return nil }

// QueryRow implements analysisQuerier.
func (q *analysisFakeQuerier) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if strings.HasPrefix(strings.TrimSpace(sql), "UPDATE analysis_run") {
		if q.rowErr != nil {
			return analysisFakeRow{err: q.rowErr}
		}
		return analysisFakeRow{values: q.rowValues}
	}
	if strings.Contains(sql, "SELECT status FROM analysis_run") {
		return analysisFakeRow{values: []any{q.selectStatus}}
	}
	if q.rowErr != nil {
		return analysisFakeRow{err: q.rowErr}
	}
	return analysisFakeRow{values: q.rowValues}
}

// analysisFakeRow implements pgx.Row.
type analysisFakeRow struct {
	values []any
	err    error
}

// Scan implements pgx.Row.
func (r analysisFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return analysisScanRow{values: r.values}.Scan(dest...)
}

// TestScanAnalysisRunRoundTrip pins the analysis_run column order and the
// nullable agent id / timestamp / JSONB columns.
func TestScanAnalysisRunRoundTrip(t *testing.T) {
	sessionID := int64(5001)
	agentRunID := int64(6001)
	started := analysisTestTime
	finished := analysisTestTime.Add(2 * time.Minute)
	input := []byte(`{"versionId":"7"}`)
	result := []byte(`{"candidates":[]}`)
	stats := []byte(`{"truncated":true}`)
	failures := []byte(`{"reason":"agent_failed"}`)

	run, err := scanAnalysisRun(analysisScanRow{values: []any{
		int64(11), int64(7), int64(9), domain.AnalysisKindFailure, domain.AnalysisDone,
		sessionID, agentRunID, "deepseek", "p7-failure-v1",
		input, result, stats, failures, int64(42),
		analysisTestTime, analysisTestTime.Add(time.Second), started, finished,
	}})
	if err != nil {
		t.Fatalf("scanAnalysisRun: %v", err)
	}
	if run.ID != 11 || run.VersionID != 7 || run.BatchID != 9 || run.Kind != domain.AnalysisKindFailure ||
		run.Status != domain.AnalysisDone || run.Model != "deepseek" || run.PromptVersion != "p7-failure-v1" {
		t.Fatalf("scanned run = %+v", run)
	}
	if run.AgentSessionID == nil || *run.AgentSessionID != sessionID ||
		run.AgentRunID == nil || *run.AgentRunID != agentRunID {
		t.Fatalf("agent ids = %v/%v", run.AgentSessionID, run.AgentRunID)
	}
	if string(run.Input) != string(input) || string(run.Result) != string(result) ||
		string(run.Stats) != string(stats) || string(run.Errors) != string(failures) {
		t.Fatalf("json documents = %s/%s/%s/%s", run.Input, run.Result, run.Stats, run.Errors)
	}
	if run.StartedAt == nil || !run.StartedAt.Equal(started) || run.FinishedAt == nil || !run.FinishedAt.Equal(finished) {
		t.Fatalf("timestamps = %v/%v", run.StartedAt, run.FinishedAt)
	}

	var decoded map[string]any
	if err := json.Unmarshal(run.Input, &decoded); err != nil || decoded["versionId"] != "7" {
		t.Fatalf("input does not round-trip: %s", run.Input)
	}
}

// TestScanAnalysisRunNulls pins the optional NULL columns.
func TestScanAnalysisRunNulls(t *testing.T) {
	run, err := scanAnalysisRun(analysisScanRow{values: []any{
		int64(11), int64(7), int64(9), domain.AnalysisKindIssueTriage, domain.AnalysisQueued,
		nil, nil, "", "", nil, nil, nil, nil, int64(42), analysisTestTime, analysisTestTime, nil, nil,
	}})
	if err != nil {
		t.Fatalf("scanAnalysisRun: %v", err)
	}
	if run.AgentSessionID != nil || run.AgentRunID != nil || run.StartedAt != nil || run.FinishedAt != nil {
		t.Fatalf("nullable columns = %+v", run)
	}
	if len(run.Input) != 0 || len(run.Result) != 0 {
		t.Fatalf("NULL json columns = %q/%q, want empty", run.Input, run.Result)
	}
}

// TestRecoverStaleReturnsRecoveredRuns pins the boot recovery SQL of spec §6.7
// and the recovered rows the service needs to re-run the triage fallback.
func TestRecoverStaleReturnsRecoveredRuns(t *testing.T) {
	finished := analysisTestTime
	q := &analysisFakeQuerier{queryValues: [][]any{{
		int64(11), int64(7), int64(9), domain.AnalysisKindIssueTriage, domain.AnalysisFailed,
		nil, nil, "", "", nil, nil, nil, []byte(`{"reason":"stale_run_recovered"}`), int64(42),
		analysisTestTime, analysisTestTime, nil, &finished,
	}}}
	repo := newAnalysisRepository(q)
	recovered, err := repo.RecoverStale(context.Background())
	if err != nil || len(recovered) != 1 {
		t.Fatalf("RecoverStale = %d runs, %v; want 1, nil", len(recovered), err)
	}
	if recovered[0].ID != 11 || recovered[0].Kind != domain.AnalysisKindIssueTriage ||
		recovered[0].Status != domain.AnalysisFailed || recovered[0].FinishedAt == nil {
		t.Fatalf("recovered run = %+v, want the failed triage row with finished_at", recovered[0])
	}
	for _, needle := range []string{"status = 'failed'", "stale_run_recovered", "'queued', 'running'", "RETURNING"} {
		if !strings.Contains(q.querySQL, needle) {
			t.Fatalf("recovery SQL %q does not contain %q", q.querySQL, needle)
		}
	}
}

// TestRecoverStaleReturnsNoRunsWhenNothingIsActive pins the counts returned to
// the worker when every run is already terminal.
func TestRecoverStaleReturnsNoRunsWhenNothingIsActive(t *testing.T) {
	repo := newAnalysisRepository(&analysisFakeQuerier{})
	recovered, err := repo.RecoverStale(context.Background())
	if err != nil || len(recovered) != 0 {
		t.Fatalf("RecoverStale = %d runs, %v; want 0, nil", len(recovered), err)
	}
}

// TestCreateMapsActiveUniqueConflict pins the analysis_active conflict of the
// partial unique index.
func TestCreateMapsActiveUniqueConflict(t *testing.T) {
	q := &analysisFakeQuerier{rowErr: &pgconn.PgError{Code: "23505", ConstraintName: "uk_analysis_run_active"}}
	repo := newAnalysisRepository(q)
	_, err := repo.Create(context.Background(), domain.AnalysisRun{ID: 11})
	if err == nil {
		t.Fatal("Create succeeded on a unique violation")
	}
	if reason := domain.AnalysisReason(err); reason != domain.ReasonAnalysisActive {
		t.Fatalf("reason = %q, want %q", reason, domain.ReasonAnalysisActive)
	}
}

// TestUpdateStatusMapsLostCASToStateConflict pins the CAS failure mapping.
func TestUpdateStatusMapsLostCASToStateConflict(t *testing.T) {
	q := &analysisFakeQuerier{rowErr: pgx.ErrNoRows, selectStatus: domain.AnalysisDone}
	repo := newAnalysisRepository(q)
	_, err := repo.UpdateStatus(context.Background(), 11, domain.AnalysisRunning, domain.AnalysisDone, application.AnalysisRunPatch{})
	if !errors.Is(err, application.ErrAnalysisStateConflict) {
		t.Fatalf("UpdateStatus error = %v, want ErrAnalysisStateConflict", err)
	}
}

// TestListActiveClampsLimit keeps a non-positive worker limit from querying the
// whole table.
func TestListActiveClampsLimit(t *testing.T) {
	repo := newAnalysisRepository(&analysisFakeQuerier{})
	if _, err := repo.ListActive(context.Background(), 0); err == nil {
		t.Fatal("ListActive(0) succeeded, want a limit error")
	}
}
