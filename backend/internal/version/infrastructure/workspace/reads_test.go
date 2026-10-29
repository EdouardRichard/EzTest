package workspace

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// fakeQueryer records the provider SQL and returns canned rows.
type fakeQueryer struct {
	sql   string
	args  []any
	rows  pgx.Rows
	err   error
	calls int
}

func (q *fakeQueryer) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.calls++
	q.sql = sql
	q.args = args
	return q.rows, q.err
}

// fakeRows is the minimal pgx.Rows implementation of the provider unit tests.
type fakeRows struct {
	values [][]any
	index  int
	err    error
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return r.err }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Values() ([]any, error)                       { return r.values[r.index-1], nil }

func (r *fakeRows) Next() bool {
	return r.index < len(r.values)
}

// Scan assigns the current canned row into dest with the same nil/typed-pointer
// rules pgx applies to the columns these providers read, then advances.
func (r *fakeRows) Scan(dest ...any) error {
	if r.index < 0 || r.index >= len(r.values) {
		return errors.New("fake rows: Scan called out of range")
	}
	row := r.values[r.index]
	r.index++
	if len(dest) != len(row) {
		return fmt.Errorf("fake rows: scan %d destinations into %d values", len(dest), len(row))
	}
	for index := range dest {
		if err := assignValue(dest[index], row[index]); err != nil {
			return err
		}
	}
	return nil
}

// assignValue implements the subset of pgx's scanning used by the tests:
// scalar pointers and pointer-to-pointer for nullable columns.
func assignValue(dest, value any) error {
	target := reflect.ValueOf(dest)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("fake rows: destination %T is not a pointer", dest)
	}
	element := target.Elem()
	if value == nil {
		element.Set(reflect.Zero(element.Type()))
		return nil
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(element.Type()) {
		element.Set(source)
		return nil
	}
	if element.Kind() == reflect.Pointer && source.Type().AssignableTo(element.Type().Elem()) {
		pointer := reflect.New(element.Type().Elem())
		pointer.Elem().Set(source)
		element.Set(pointer)
		return nil
	}
	return fmt.Errorf("fake rows: cannot assign %T to %T", value, dest)
}

func TestReadsReviewingVersions(t *testing.T) {
	firstAt := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(-time.Hour)
	queryer := &fakeQueryer{rows: &fakeRows{values: [][]any{
		{int64(11), "版本甲", "reviewing", firstAt},
		{int64(12), "版本乙", "reviewing", secondAt},
	}}}
	reads := &Reads{q: queryer}

	rows, err := reads.ReviewingVersions(context.Background())
	if err != nil {
		t.Fatalf("ReviewingVersions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].VersionID != 11 || rows[0].VersionName != "版本甲" ||
		rows[0].Status != "reviewing" || !rows[0].ReviewingAt.Equal(firstAt) {
		t.Fatalf("row 0 = %+v", rows[0])
	}
	if rows[1].VersionID != 12 || !rows[1].ReviewingAt.Equal(secondAt) {
		t.Fatalf("row 1 = %+v", rows[1])
	}
	for _, needle := range []string{
		"FROM version",
		"status = 'reviewing'",
		"ORDER BY updated_at DESC, id DESC",
	} {
		if !strings.Contains(queryer.sql, needle) {
			t.Fatalf("review SQL %q does not contain %q", queryer.sql, needle)
		}
	}
}

func TestReadsReviewingVersionsPropagatesError(t *testing.T) {
	queryer := &fakeQueryer{err: errors.New("db unavailable")}
	_, err := (&Reads{q: queryer}).ReviewingVersions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reviewing versions") {
		t.Fatalf("error = %v, want a wrapped reviewing versions error", err)
	}
}

func TestReadsNilProvider(t *testing.T) {
	var reads Reads
	if _, err := reads.ReviewingVersions(context.Background()); err == nil {
		t.Fatal("nil provider must error")
	}
}

func TestReadsImplementsReviewProvider(t *testing.T) {
	var _ workspaceapp.ReviewProvider = (*Reads)(nil)
}
