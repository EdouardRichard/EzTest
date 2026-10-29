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

// TestSearchReturnsVersionAndRequirementGroups proves the version provider reads
// its own tables, projects the §6.2 item fields and builds the deep links.
func TestSearchReturnsVersionAndRequirementGroups(t *testing.T) {
	queryer := &fakeQueryer{responses: []fakeResponse{
		{rows: &fakeRows{values: [][]any{
			{int64(11), "登录版本", "标签甲", "reviewing"},
		}}},
		{rows: &fakeRows{values: [][]any{
			{int64(21), "登录需求", "clarified", int64(11), "登录版本"},
		}}},
	}}
	groups, err := (&Reads{q: queryer}).Search(context.Background(), "登录", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want version and requirement: %+v", len(groups), groups)
	}
	versionGroup := groups[0]
	if versionGroup.Type != searchapp.TypeVersion || len(versionGroup.Items) != 1 {
		t.Fatalf("version group = %+v", versionGroup)
	}
	version := versionGroup.Items[0]
	if version.ID != "11" || version.Title != "登录版本" || version.Subtitle != "标签甲" ||
		version.VersionID != "11" || version.VersionName != "登录版本" ||
		version.Status != "reviewing" || version.URL != "/versions/11?tab=overview" {
		t.Fatalf("version item = %+v", version)
	}

	requirementGroup := groups[1]
	if requirementGroup.Type != searchapp.TypeRequirement || len(requirementGroup.Items) != 1 {
		t.Fatalf("requirement group = %+v", requirementGroup)
	}
	requirement := requirementGroup.Items[0]
	if requirement.ID != "21" || requirement.Title != "登录需求" ||
		requirement.VersionID != "11" || requirement.VersionName != "登录版本" ||
		requirement.Status != "clarified" ||
		requirement.URL != "/versions/11?tab=requirements&requirement=21" {
		t.Fatalf("requirement item = %+v", requirement)
	}

	if len(queryer.calls) != 2 {
		t.Fatalf("queries = %d, want 2", len(queryer.calls))
	}
	versionSQL := queryer.calls[0].sql
	for _, needle := range []string{
		"FROM version", "ILIKE $1", "ORDER BY updated_at DESC, id DESC", "LIMIT $2",
	} {
		if !strings.Contains(versionSQL, needle) {
			t.Fatalf("version SQL %q does not contain %q", versionSQL, needle)
		}
	}
	requirementSQL := queryer.calls[1].sql
	for _, needle := range []string{
		"FROM requirement", "JOIN version", "ILIKE $1", "LIMIT $2",
	} {
		if !strings.Contains(requirementSQL, needle) {
			t.Fatalf("requirement SQL %q does not contain %q", requirementSQL, needle)
		}
	}
	if got := queryer.calls[0].args[0]; got != "%登录%" {
		t.Fatalf("version pattern = %v, want %%登录%%", got)
	}
	if got := queryer.calls[0].args[1]; got != 5 {
		t.Fatalf("version limit arg = %v, want 5", got)
	}
}

// TestSearchEscapesLikeWildcards proves a keyword containing LIKE meta
// characters is matched literally.
func TestSearchEscapesLikeWildcards(t *testing.T) {
	queryer := &fakeQueryer{responses: []fakeResponse{
		{rows: &fakeRows{}},
		{rows: &fakeRows{}},
	}}
	if _, err := (&Reads{q: queryer}).Search(context.Background(), `100%_x\y`, 5); err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := `%100\%\_x\\y%`
	if got := queryer.calls[0].args[0]; got != want {
		t.Fatalf("pattern = %q, want %q", got, want)
	}
}

// TestSearchPropagatesQueryError proves a failing version query fails the
// provider so the service can mark the result degraded.
func TestSearchPropagatesQueryError(t *testing.T) {
	queryer := &fakeQueryer{responses: []fakeResponse{
		{err: errors.New("db unavailable")},
	}}
	_, err := (&Reads{q: queryer}).Search(context.Background(), "登录", 5)
	if err == nil || !strings.Contains(err.Error(), "version search reads") {
		t.Fatalf("error = %v, want a wrapped version search reads error", err)
	}
}

// TestSearchPropagatesRequirementError proves a failing requirement query fails
// the provider too.
func TestSearchPropagatesRequirementError(t *testing.T) {
	queryer := &fakeQueryer{responses: []fakeResponse{
		{rows: &fakeRows{}},
		{err: errors.New("db unavailable")},
	}}
	_, err := (&Reads{q: queryer}).Search(context.Background(), "登录", 5)
	if err == nil || !strings.Contains(err.Error(), "requirements") {
		t.Fatalf("error = %v, want a wrapped requirements error", err)
	}
}

// TestSearchNilProvider proves an unwired provider fails closed.
func TestSearchNilProvider(t *testing.T) {
	var reads Reads
	if _, err := reads.Search(context.Background(), "登录", 5); err == nil {
		t.Fatal("nil provider must error")
	}
}

// TestReadsImplementsProvider pins the cross-context port.
func TestReadsImplementsProvider(t *testing.T) {
	var _ searchapp.Provider = (*Reads)(nil)
}
