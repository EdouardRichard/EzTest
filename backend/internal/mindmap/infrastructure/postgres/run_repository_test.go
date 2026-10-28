package postgres

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// runScanTestTime is a lossless TIMESTAMPTZ reading: UTC, whole seconds.
var runScanTestTime = time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)

// runFakeScanRow is a rowScanner whose values are assigned to the scan
// destinations. It pins the case_design_run column order without a database.
type runFakeScanRow struct {
	values []any
}

// Scan implements rowScanner.
func (r runFakeScanRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("fake row: %d destinations for %d values", len(dest), len(r.values))
	}
	for i := range dest {
		if err := assignRunScanValue(dest[i], r.values[i]); err != nil {
			return fmt.Errorf("fake row column %d: %w", i+1, err)
		}
	}
	return nil
}

// assignRunScanValue emulates the pgx codecs: a nil value clears the
// destination (SQL NULL), a value is assigned through a pointer, and an
// optional column (pointer destination) is allocated with the value.
func assignRunScanValue(dest, value any) error {
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
	if elem.Kind() == reflect.Pointer {
		pointer := reflect.New(elem.Type().Elem())
		if source.Type().AssignableTo(pointer.Elem().Type()) {
			pointer.Elem().Set(source)
			elem.Set(pointer)
			return nil
		}
		if source.Type().ConvertibleTo(pointer.Elem().Type()) {
			pointer.Elem().Set(source.Convert(pointer.Elem().Type()))
			elem.Set(pointer)
			return nil
		}
	}
	return fmt.Errorf("cannot assign %T to %s", value, elem.Type())
}

// runRowValues renders one case_design_run row in runColumnList order with the
// given batch id value (nil is SQL NULL).
func runRowValues(batchID any) []any {
	return []any{
		int64(11), int64(22), application.RunKindSupplement, domain.RunRunning,
		nil, nil, "deepseek", "p7-supplement-v1",
		[]byte(`{"kind":"supplement","batchId":"77"}`), nil, nil, nil,
		int64(9), runScanTestTime, runScanTestTime, nil, nil,
		batchID,
	}
}

// recordingRunQuerier captures the INSERT of RunRepository.Create and returns
// the configured RETURNING row.
type recordingRunQuerier struct {
	query string
	args  []any
	row   rowScanner
}

func (q *recordingRunQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (q *recordingRunQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("recordingRunQuerier: Query is not used")
}

func (q *recordingRunQuerier) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	q.query = query
	q.args = args
	return q.row
}

func (q *recordingRunQuerier) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}

// TestScanRunMapsBatchID pins the P7 case_design_run.batch_id column: a stored
// batch id is projected onto the run and NULL stays nil.
func TestScanRunMapsBatchID(t *testing.T) {
	batchID := int64(77)
	cases := []struct {
		name    string
		value   any
		wantNil bool
		want    int64
	}{
		{name: "batch run", value: batchID, want: batchID},
		{name: "no batch", value: nil, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, err := scanRun(runFakeScanRow{values: runRowValues(tc.value)})
			if err != nil {
				t.Fatalf("scanRun: %v", err)
			}
			if run.ID != 11 || run.VersionID != 22 || run.Kind != application.RunKindSupplement ||
				run.Status != domain.RunRunning || run.PromptVersion != "p7-supplement-v1" {
				t.Fatalf("run identity = %+v", run)
			}
			if tc.wantNil {
				if run.BatchID != nil {
					t.Fatalf("batch id = %v, want nil", run.BatchID)
				}
				return
			}
			if run.BatchID == nil || *run.BatchID != tc.want {
				t.Fatalf("batch id = %v, want %d", run.BatchID, tc.want)
			}
		})
	}
}

// TestCreateRunPersistsBatchID pins that Create writes the run's parent batch
// id (the column T14/T16 read back) and scans it from RETURNING.
func TestCreateRunPersistsBatchID(t *testing.T) {
	batchID := int64(77)
	q := &recordingRunQuerier{row: runFakeScanRow{values: runRowValues(batchID)}}
	repo := newRunRepository(q)

	created, err := repo.Create(context.Background(), domain.CaseDesignRun{
		ID:            11,
		VersionID:     22,
		Kind:          application.RunKindSupplement,
		Status:        domain.RunRunning,
		PromptVersion: "p7-supplement-v1",
		CreatedBy:     9,
		CreatedAt:     runScanTestTime,
		UpdatedAt:     runScanTestTime,
		BatchID:       &batchID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.BatchID == nil || *created.BatchID != batchID {
		t.Fatalf("created batch id = %v, want %d", created.BatchID, batchID)
	}
	if !strings.Contains(q.query, "batch_id") {
		t.Fatalf("insert query misses the batch_id column: %s", q.query)
	}
	if !runArgsContain(q.args, batchID) {
		t.Fatalf("insert args %v miss the batch id %d", q.args, batchID)
	}
}

// runArgsContain reports whether any argument equals value.
func runArgsContain(args []any, value int64) bool {
	for _, arg := range args {
		if got, ok := arg.(int64); ok && got == value {
			return true
		}
	}
	return false
}
