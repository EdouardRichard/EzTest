package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// globalRepoFakeQuerier scripts the global reader queries. Query calls are
// answered in order (page first, refs second); QueryRow answers the cursor
// created_at lookup.
type globalRepoFakeQuerier struct {
	querySQLs []string
	queryArgs [][]any
	results   []pgx.Rows
	queryErrs []error

	rowSQLs   []string
	rowArgs   [][]any
	cursorAt  time.Time
	cursorErr error
}

func (q *globalRepoFakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("global reader must not write")
}

func (q *globalRepoFakeQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	index := len(q.querySQLs)
	q.querySQLs = append(q.querySQLs, sql)
	q.queryArgs = append(q.queryArgs, args)
	if index < len(q.queryErrs) && q.queryErrs[index] != nil {
		return nil, q.queryErrs[index]
	}
	if index < len(q.results) {
		return q.results[index], nil
	}
	return &issueRepoFakeRows{}, nil
}

func (q *globalRepoFakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.rowSQLs = append(q.rowSQLs, sql)
	q.rowArgs = append(q.rowArgs, args)
	return issueRepoFakeRow{values: []any{q.cursorAt}, err: q.cursorErr}
}

// globalIssueRowValues builds one issue column row for the fake rows.
func globalIssueRowValues(id, versionID int64, createdAt time.Time) []any {
	return issueArgs(domain.Issue{
		ID: id, VersionID: versionID, Source: domain.SourceAI, Category: domain.CategoryDefect,
		Severity: domain.SeverityMajor, Status: domain.StatusOpen, Confidence: domain.ConfidenceHigh,
		Title: "问题", CreatedBy: 1, UpdatedBy: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	})
}

// globalReportRowValues builds one report list projection row plus batchNo.
func globalReportRowValues(id, versionID, batchID int64, batchNo int, createdAt time.Time) []any {
	return []any{
		id, versionID, batchID, 1, domain.ReportStatusPublished, "报告",
		nil, strings.Repeat("a", 64), "versions/1/reports/x.md", nil, int64(0), "", nil, nil,
		createdAt, createdAt,
		batchNo,
	}
}

// TestGlobalIssueListCrossVersionQuery proves the cross-version page query uses
// only the delivery issue tables (no version join), forwards every filter to SQL,
// paginates with the opaque created_at/id keyset cursor, trims the limit+1 probe
// row and attaches the issue refs.
func TestGlobalIssueListCrossVersionQuery(t *testing.T) {
	later := issueRepoTestTime
	earlier := issueRepoTestTime.Add(-time.Hour)
	page := &issueRepoFakeRows{rows: [][]any{
		globalIssueRowValues(9002, 101, later),
		globalIssueRowValues(9001, 202, earlier),
		globalIssueRowValues(9000, 202, earlier.Add(-time.Hour)),
	}}
	refs := &issueRepoFakeRows{rows: [][]any{
		{int64(9002), domain.RefKindBatch, "7001"},
		{int64(9002), domain.RefKindCaseNode, "800"},
		{int64(9001), domain.RefKindRequirement, "55"},
	}}
	q := &globalRepoFakeQuerier{results: []pgx.Rows{page, refs}}
	repo := &GlobalReadRepository{q: q}

	rows, next, hasMore, err := repo.ListGlobalIssues(context.Background(), application.GlobalIssueFilter{
		VersionID: globalRepoPtr(101), Status: domain.StatusOpen,
		Severity: domain.SeverityMajor, Category: domain.CategoryDefect,
		BatchID: globalRepoPtr(7001), CaseRunID: globalRepoPtr(7101), CaseNodeID: globalRepoPtr(7102),
		Query: "登录", Limit: 2,
	})
	if err != nil {
		t.Fatalf("ListGlobalIssues = %v", err)
	}
	if len(q.querySQLs) != 2 {
		t.Fatalf("queries=%d, want page plus refs", len(q.querySQLs))
	}
	sql := q.querySQLs[0]
	for _, want := range []string{"FROM issue", "ORDER BY created_at DESC, id DESC", "LIMIT $12", "$8::text = '' OR title ILIKE"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("issue SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "JOIN version") || strings.Contains(sql, "FROM version") {
		t.Fatalf("global issue SQL must not join the version table:\n%s", sql)
	}
	wantArgs := []any{
		int64(101), domain.StatusOpen, domain.SeverityMajor, domain.CategoryDefect,
		int64(7001), int64(7101), int64(7102), "登录",
		false, time.Time{}, int64(0), 3,
	}
	if !reflect.DeepEqual(q.queryArgs[0], wantArgs) {
		t.Fatalf("issue args=%#v, want %#v", q.queryArgs[0], wantArgs)
	}
	if len(rows) != 2 || rows[0].Issue.ID != 9002 || rows[1].Issue.ID != 9001 || !hasMore {
		t.Fatalf("rows=%+v more=%v, want [9002 9001]/true", rows, hasMore)
	}
	if next != platform.EncodeCursor(9001) {
		t.Fatalf("next=%q, want cursor of the last returned row", next)
	}
	if len(rows[0].Refs) != 2 || rows[0].Refs[0].RefKind != domain.RefKindBatch ||
		len(rows[1].Refs) != 1 || rows[1].Refs[0].RefID != "55" {
		t.Fatalf("refs not attached: %+v / %+v", rows[0].Refs, rows[1].Refs)
	}
	refSQL := q.querySQLs[1]
	if !strings.Contains(refSQL, "FROM issue_ref") || !strings.Contains(refSQL, "issue_id = ANY($1)") {
		t.Fatalf("refs SQL:\n%s", refSQL)
	}
	if len(q.queryArgs[1]) != 1 || !reflect.DeepEqual(q.queryArgs[1][0], []int64{9002, 9001}) {
		t.Fatalf("refs args=%#v, want the page issue ids", q.queryArgs[1])
	}
	if rows[0].VersionName != "" {
		t.Fatalf("repository must leave VersionName to the service, got %q", rows[0].VersionName)
	}
}

// TestGlobalIssueListCursor proves the opaque cursor is resolved to its
// created_at keyset tuple and that a missing cursor row yields an empty page
// without running the page query.
func TestGlobalIssueListCursor(t *testing.T) {
	cursorID := int64(9000)
	cursorAt := issueRepoTestTime.Add(-30 * time.Minute)
	page := &issueRepoFakeRows{rows: [][]any{globalIssueRowValues(8999, 101, cursorAt)}}
	q := &globalRepoFakeQuerier{results: []pgx.Rows{page}, cursorAt: cursorAt}
	repo := &GlobalReadRepository{q: q}

	rows, _, _, err := repo.ListGlobalIssues(context.Background(), application.GlobalIssueFilter{
		Cursor: platform.EncodeCursor(cursorID), Limit: 5,
	})
	if err != nil {
		t.Fatalf("ListGlobalIssues = %v", err)
	}
	if len(q.rowSQLs) != 1 || q.rowSQLs[0] != "SELECT created_at FROM issue WHERE id = $1" {
		t.Fatalf("cursor SQL=%v", q.rowSQLs)
	}
	if len(q.rowArgs[0]) != 1 || q.rowArgs[0][0] != cursorID {
		t.Fatalf("cursor args=%#v", q.rowArgs[0])
	}
	args := q.queryArgs[0]
	if len(args) != 12 || args[8] != true || args[9] != cursorAt || args[10] != cursorID {
		t.Fatalf("page args=%#v, want hasCursor/created_at/id at 9-11", args)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}

	missing := &globalRepoFakeQuerier{cursorErr: pgx.ErrNoRows}
	rows, next, hasMore, err := (&GlobalReadRepository{q: missing}).ListGlobalIssues(context.Background(), application.GlobalIssueFilter{
		Cursor: platform.EncodeCursor(cursorID), Limit: 5,
	})
	if err != nil {
		t.Fatalf("missing cursor = %v", err)
	}
	if len(rows) != 0 || next != "" || hasMore || len(missing.querySQLs) != 0 {
		t.Fatalf("missing cursor rows=%d next=%q more=%v queries=%d", len(rows), next, hasMore, len(missing.querySQLs))
	}
}

// TestGlobalIssueListErrors proves a page or refs query failure is wrapped and
// returned instead of a partial page.
func TestGlobalIssueListErrors(t *testing.T) {
	pageErr := errors.New("page query failed")
	q := &globalRepoFakeQuerier{queryErrs: []error{pageErr}}
	if _, _, _, err := (&GlobalReadRepository{q: q}).ListGlobalIssues(context.Background(),
		application.GlobalIssueFilter{Limit: 2}); !errors.Is(err, pageErr) {
		t.Fatalf("page error=%v, want %v", err, pageErr)
	}

	refsErr := errors.New("refs query failed")
	page := &issueRepoFakeRows{rows: [][]any{globalIssueRowValues(9001, 101, issueRepoTestTime)}}
	q = &globalRepoFakeQuerier{results: []pgx.Rows{page}, queryErrs: []error{nil, refsErr}}
	if _, _, _, err := (&GlobalReadRepository{q: q}).ListGlobalIssues(context.Background(),
		application.GlobalIssueFilter{Limit: 2}); !errors.Is(err, refsErr) {
		t.Fatalf("refs error=%v, want %v", err, refsErr)
	}
}

// TestGlobalReportListCrossVersionQuery proves the report page query reads only
// the report table, derives batchNo from the stored content snapshot, forwards
// the version/status filters and paginates on created_at/id.
func TestGlobalReportListCrossVersionQuery(t *testing.T) {
	later := reportTestTime
	earlier := reportTestTime.Add(-time.Hour)
	page := &issueRepoFakeRows{rows: [][]any{
		globalReportRowValues(9102, 101, 7002, 2, later),
		globalReportRowValues(9101, 202, 7001, 5, earlier),
		globalReportRowValues(9100, 202, 7001, 4, earlier.Add(-time.Hour)),
	}}
	q := &globalRepoFakeQuerier{results: []pgx.Rows{page}}
	repo := &GlobalReadRepository{q: q}

	rows, next, hasMore, err := repo.ListGlobalReports(context.Background(), application.GlobalReportFilter{
		VersionID: globalRepoPtr(101), Status: domain.ReportStatusPublished, Limit: 2,
	})
	if err != nil {
		t.Fatalf("ListGlobalReports = %v", err)
	}
	sql := q.querySQLs[0]
	for _, want := range []string{"FROM report", "content->'batch'->>'batchNo'", "ORDER BY created_at DESC, id DESC", "LIMIT $6"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("report SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "JOIN version") || strings.Contains(sql, "FROM version") {
		t.Fatalf("global report SQL must not join the version table:\n%s", sql)
	}
	wantArgs := []any{int64(101), domain.ReportStatusPublished, false, time.Time{}, int64(0), 3}
	if !reflect.DeepEqual(q.queryArgs[0], wantArgs) {
		t.Fatalf("report args=%#v, want %#v", q.queryArgs[0], wantArgs)
	}
	if len(rows) != 2 || rows[0].Report.ID != 9102 || rows[1].Report.ID != 9101 || !hasMore {
		t.Fatalf("rows=%+v more=%v, want [9102 9101]/true", rows, hasMore)
	}
	if rows[0].BatchNo != 2 || rows[1].BatchNo != 5 {
		t.Fatalf("batchNo=%d/%d, want 2/5", rows[0].BatchNo, rows[1].BatchNo)
	}
	if next != platform.EncodeCursor(9101) {
		t.Fatalf("next=%q, want cursor of the last returned row", next)
	}
	if rows[0].Report.Content.Schema != "" {
		t.Fatalf("list projection must not load report content: %+v", rows[0].Report.Content)
	}
	if rows[0].VersionName != "" {
		t.Fatalf("repository must leave VersionName to the service, got %q", rows[0].VersionName)
	}
}

// TestGlobalReportListCursorAndErrors proves the report cursor lookup and the
// missing-cursor/empty-page and query-error paths.
func TestGlobalReportListCursorAndErrors(t *testing.T) {
	cursorID := int64(9100)
	cursorAt := reportTestTime.Add(-30 * time.Minute)
	page := &issueRepoFakeRows{rows: [][]any{globalReportRowValues(9099, 101, 7001, 1, cursorAt)}}
	q := &globalRepoFakeQuerier{results: []pgx.Rows{page}, cursorAt: cursorAt}
	rows, _, _, err := (&GlobalReadRepository{q: q}).ListGlobalReports(context.Background(), application.GlobalReportFilter{
		Cursor: platform.EncodeCursor(cursorID), Limit: 5,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("cursor page = %v rows=%d", err, len(rows))
	}
	if q.rowSQLs[0] != "SELECT created_at FROM report WHERE id = $1" || q.rowArgs[0][0] != cursorID {
		t.Fatalf("report cursor SQL=%v args=%#v", q.rowSQLs, q.rowArgs)
	}
	if args := q.queryArgs[0]; args[2] != true || args[3] != cursorAt || args[4] != cursorID {
		t.Fatalf("report page args=%#v", args)
	}

	missing := &globalRepoFakeQuerier{cursorErr: pgx.ErrNoRows}
	rows, next, hasMore, err := (&GlobalReadRepository{q: missing}).ListGlobalReports(context.Background(),
		application.GlobalReportFilter{Cursor: platform.EncodeCursor(cursorID), Limit: 5})
	if err != nil || len(rows) != 0 || next != "" || hasMore || len(missing.querySQLs) != 0 {
		t.Fatalf("missing cursor = rows %d next %q more %v err %v queries %d",
			len(rows), next, hasMore, err, len(missing.querySQLs))
	}

	queryErr := errors.New("report query failed")
	if _, _, _, err := (&GlobalReadRepository{q: &globalRepoFakeQuerier{queryErrs: []error{queryErr}}}).
		ListGlobalReports(context.Background(), application.GlobalReportFilter{Limit: 2}); !errors.Is(err, queryErr) {
		t.Fatalf("report error=%v, want %v", err, queryErr)
	}
}

// globalRepoPtr returns a pointer to a literal id for the optional filters.
func globalRepoPtr(id int64) *int64 {
	return &id
}
