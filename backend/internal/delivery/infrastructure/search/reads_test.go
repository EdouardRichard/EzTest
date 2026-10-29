package search

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// queryCall records one provider statement.
type queryCall struct {
	sql  string
	args []any
}

// fakeQueryer serves canned responses in order and records the statements.
type fakeQueryer struct {
	responses []fakeResponse
	calls     []queryCall
}

type fakeResponse struct {
	rows pgx.Rows
	err  error
}

func (q *fakeQueryer) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.calls = append(q.calls, queryCall{sql: sql, args: args})
	index := len(q.calls) - 1
	if index >= len(q.responses) {
		return nil, errors.New("fake queryer: unexpected query")
	}
	return q.responses[index].rows, q.responses[index].err
}

// fakeVersionReader resolves version names in the delivery tests.
type fakeVersionReader struct {
	names map[int64]string
	err   error
	calls []int64
}

func (r *fakeVersionReader) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	r.calls = append(r.calls, id)
	if r.err != nil {
		return nil, r.err
	}
	name, ok := r.names[id]
	if !ok {
		return &versionapp.Version{ID: id}, nil
	}
	return &versionapp.Version{ID: id, Name: name}, nil
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

// Scan assigns the current canned row into dest with the pgx nil rules the
// providers rely on, then advances.
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

// assignValue implements the scalar scanning used by the tests.
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
	if !source.Type().AssignableTo(element.Type()) {
		return fmt.Errorf("fake rows: cannot assign %T to %T", value, dest)
	}
	element.Set(source)
	return nil
}

// TestSearchReturnsIssues proves the delivery provider projects issues into the
// §6.2 item fields, resolves each distinct version name once and builds the
// issues deep link. The description/actual/repro fields stay out of the result.
func TestSearchReturnsIssues(t *testing.T) {
	queryer := &fakeQueryer{responses: []fakeResponse{
		{rows: &fakeRows{values: [][]any{
			{int64(400), "下单失败", "open", int64(11)},
			{int64(401), "下单超时", "confirmed", int64(11)},
		}}},
	}}
	versions := &fakeVersionReader{names: map[int64]string{11: "版本甲"}}
	groups, err := (&Reads{q: queryer, versions: versions}).Search(context.Background(), "下单", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(groups) != 1 || groups[0].Type != searchapp.TypeIssue {
		t.Fatalf("groups = %+v, want one issue group", groups)
	}
	if len(groups[0].Items) != 2 {
		t.Fatalf("issue items = %+v, want 2", groups[0].Items)
	}
	first := groups[0].Items[0]
	if first.ID != "400" || first.Title != "下单失败" || first.Subtitle != "版本甲" ||
		first.VersionID != "11" || first.VersionName != "版本甲" ||
		first.Status != "open" || first.URL != "/versions/11?tab=issues&issue=400" {
		t.Fatalf("first item = %+v", first)
	}
	if len(versions.calls) != 1 || versions.calls[0] != 11 {
		t.Fatalf("version reads = %v, want one read for version 11", versions.calls)
	}
	sql := queryer.calls[0].sql
	for _, needle := range []string{
		"FROM issue", "ILIKE $1", "ORDER BY updated_at DESC, id DESC", "LIMIT $2",
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("issue SQL %q does not contain %q", sql, needle)
		}
	}
	if got := queryer.calls[0].args[0]; got != "%下单%" {
		t.Fatalf("issue pattern = %v, want %%下单%%", got)
	}
}

// TestSearchPropagatesQueryError proves a failing issue query fails the provider.
func TestSearchPropagatesQueryError(t *testing.T) {
	queryer := &fakeQueryer{responses: []fakeResponse{{err: errors.New("db unavailable")}}}
	_, err := (&Reads{q: queryer, versions: &fakeVersionReader{}}).Search(
		context.Background(), "下单", 5)
	if err == nil || !strings.Contains(err.Error(), "delivery search reads") {
		t.Fatalf("error = %v, want a wrapped delivery search reads error", err)
	}
}

// TestSearchPropagatesVersionNameError proves a failed version-name lookup fails
// the provider instead of returning blank names.
func TestSearchPropagatesVersionNameError(t *testing.T) {
	queryer := &fakeQueryer{responses: []fakeResponse{
		{rows: &fakeRows{values: [][]any{
			{int64(400), "下单失败", "open", int64(11)},
		}}},
	}}
	versions := &fakeVersionReader{err: errors.New("version db unavailable")}
	_, err := (&Reads{q: queryer, versions: versions}).Search(context.Background(), "下单", 5)
	if err == nil || !strings.Contains(err.Error(), "version name") {
		t.Fatalf("error = %v, want a wrapped version name error", err)
	}
}

// TestSearchNilProvider proves an unwired provider fails closed.
func TestSearchNilProvider(t *testing.T) {
	var reads Reads
	if _, err := reads.Search(context.Background(), "下单", 5); err == nil {
		t.Fatal("nil provider must error")
	}
}

// TestSearchNilVersionReader proves the provider refuses to return items without
// version names.
func TestSearchNilVersionReader(t *testing.T) {
	queryer := &fakeQueryer{responses: []fakeResponse{
		{rows: &fakeRows{values: [][]any{
			{int64(400), "下单失败", "open", int64(11)},
		}}},
	}}
	_, err := (&Reads{q: queryer}).Search(context.Background(), "下单", 5)
	if err == nil || !strings.Contains(err.Error(), "version name reader is not configured") {
		t.Fatalf("error = %v, want the missing version name reader error", err)
	}
}

// TestReadsImplementsProvider pins the cross-context port.
func TestReadsImplementsProvider(t *testing.T) {
	var _ searchapp.Provider = (*Reads)(nil)
}
