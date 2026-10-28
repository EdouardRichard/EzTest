package postgres

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

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// issueRepoTestTime is a lossless TIMESTAMPTZ reading.
var issueRepoTestTime = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// issueRepoScanRow assigns scripted values to scan destinations, so the column
// order can be pinned without a database.
type issueRepoScanRow struct{ values []any }

func (r issueRepoScanRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("issue scan row: %d destinations for %d values", len(dest), len(r.values))
	}
	for index := range dest {
		if err := issueRepoAssignScanValue(dest[index], r.values[index]); err != nil {
			return fmt.Errorf("issue scan row column %d: %w", index+1, err)
		}
	}
	return nil
}

// issueRepoAssignScanValue mimics the pgx codecs: nil clears the destination and
// an optional column (pointer destination) is allocated with the value.
func issueRepoAssignScanValue(dest, value any) error {
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

// issueRepoFakeQuerier is a querier whose row/exec/query answers are scripted.
type issueRepoFakeQuerier struct {
	execSQLs     []string
	execArgs     [][]any
	execErr      error
	querySQL     string
	queryArgs    []any
	queryRows    pgx.Rows
	queryRowsErr error

	updateValues []any
	updateErr    error
	rereadValues []any
	rereadErr    error
	selectValues []any
	selectErr    error
	queryRowSQLs []string
	queryRowArgs [][]any
}

// Exec implements issueQuerier.
func (q *issueRepoFakeQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.execSQLs = append(q.execSQLs, sql)
	q.execArgs = append(q.execArgs, args)
	if q.execErr != nil {
		return pgconn.CommandTag{}, q.execErr
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

// Query implements issueQuerier.
func (q *issueRepoFakeQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.querySQL = sql
	q.queryArgs = args
	if q.queryRowsErr != nil {
		return nil, q.queryRowsErr
	}
	return q.queryRows, nil
}

// QueryRow implements issueQuerier and routes by statement shape.
func (q *issueRepoFakeQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.queryRowSQLs = append(q.queryRowSQLs, sql)
	q.queryRowArgs = append(q.queryRowArgs, args)
	trimmed := strings.TrimSpace(sql)
	switch {
	case strings.HasPrefix(trimmed, "UPDATE issue"):
		return issueRepoFakeRow{values: q.updateValues, err: q.updateErr}
	case strings.HasPrefix(trimmed, "SELECT id FROM issue"):
		return issueRepoFakeRow{values: q.rereadValues, err: q.rereadErr}
	default:
		return issueRepoFakeRow{values: q.selectValues, err: q.selectErr}
	}
}

// issueRepoFakeRow implements pgx.Row.
type issueRepoFakeRow struct {
	values []any
	err    error
}

// Scan implements pgx.Row.
func (r issueRepoFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return issueRepoScanRow{values: r.values}.Scan(dest...)
}

// issueRepoFakeRows implements pgx.Rows.
type issueRepoFakeRows struct {
	rows  [][]any
	index int
	err   error
}

func (r *issueRepoFakeRows) Close()                                       {}
func (r *issueRepoFakeRows) Err() error                                   { return r.err }
func (r *issueRepoFakeRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("SELECT") }
func (r *issueRepoFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *issueRepoFakeRows) RawValues() [][]byte                          { return nil }
func (r *issueRepoFakeRows) Conn() *pgx.Conn                              { return nil }

func (r *issueRepoFakeRows) Next() bool {
	if r.index >= len(r.rows) {
		return false
	}
	r.index++
	return true
}

func (r *issueRepoFakeRows) Scan(dest ...any) error {
	if r.index == 0 || r.index > len(r.rows) {
		return errors.New("issue fake rows: Scan called without Next")
	}
	return issueRepoScanRow{values: r.rows[r.index-1]}.Scan(dest...)
}

func (r *issueRepoFakeRows) Values() ([]any, error) {
	if r.index == 0 || r.index > len(r.rows) {
		return nil, errors.New("issue fake rows: Values called without Next")
	}
	return r.rows[r.index-1], nil
}

// issueRepoTestIssue is a fully populated issue row used by the mapping tests.
func issueRepoTestIssue() domain.Issue {
	batchID := int64(600)
	caseRunID := int64(700)
	caseNodeID := int64(800)
	stepResultID := int64(900)
	confirmedBy := int64(41)
	dismissedBy := int64(42)
	confirmedAt := issueRepoTestTime.Add(-3 * time.Minute)
	dismissedAt := issueRepoTestTime.Add(-time.Minute)
	return domain.Issue{
		ID: 11, VersionID: 7,
		BatchID: &batchID, CaseRunID: &caseRunID, CaseNodeID: &caseNodeID, StepResultID: &stepResultID,
		Source: domain.SourceAI, Category: domain.CategoryDefect, Severity: domain.SeverityMajor,
		Status: domain.StatusDismissed, Confidence: domain.ConfidenceHigh,
		FailureClass: domain.FailureClassApplication, DedupeKey: strings.Repeat("a", 64),
		Title: "登录按钮无响应", Description: "描述", ReproSteps: "步骤", Expected: "期望", Actual: "实际",
		RerunSuggestion: "重跑建议", ExternalRef: "EXT-1",
		RecurrenceCount: 2, Revision: 5,
		CreatedBy: 40, UpdatedBy: 41, ConfirmedBy: &confirmedBy, DismissedBy: &dismissedBy,
		ConfirmedAt: &confirmedAt, DismissedAt: &dismissedAt,
		DismissedReason: "环境问题",
		CreatedAt:       issueRepoTestTime.Add(-time.Hour), UpdatedAt: issueRepoTestTime,
	}
}

// TestIssueArgsColumnOrder pins the 31 persisted columns in issueColumnList
// order, including the SQL NULL rendering of optional columns.
func TestIssueArgsColumnOrder(t *testing.T) {
	issue := issueRepoTestIssue()
	args := issueArgs(issue)
	if len(args) != 31 {
		t.Fatalf("issueArgs returned %d values, want the 31 issue columns", len(args))
	}
	want := []any{
		issue.ID, issue.VersionID,
		*issue.BatchID, *issue.CaseRunID, *issue.CaseNodeID, *issue.StepResultID,
		issue.Source, issue.Category, issue.Severity, issue.Status, issue.Confidence,
		issue.FailureClass, issue.DedupeKey,
		issue.Title, issue.Description, issue.ReproSteps, issue.Expected, issue.Actual,
		issue.RerunSuggestion, issue.ExternalRef,
		issue.RecurrenceCount, issue.Revision, issue.DismissedReason,
		issue.CreatedBy, issue.UpdatedBy,
		*issue.ConfirmedBy, *issue.ConfirmedAt, *issue.DismissedBy, *issue.DismissedAt,
		issue.CreatedAt, issue.UpdatedAt,
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("issueArgs = %#v, want %#v", args, want)
	}

	minimal := issueArgs(domain.Issue{
		ID: 1, VersionID: 2, Source: domain.SourceHuman, Category: domain.CategoryOther,
		Severity: domain.SeverityInfo, Status: domain.StatusOpen, Confidence: domain.ConfidenceLow,
		Title: "t", CreatedBy: 1, UpdatedBy: 1, CreatedAt: issueRepoTestTime, UpdatedAt: issueRepoTestTime,
	})
	for _, index := range []int{2, 3, 4, 5, 11, 12, 25, 26, 27, 28} {
		if minimal[index] != nil {
			t.Errorf("optional column %d = %#v, want SQL NULL", index+1, minimal[index])
		}
	}
}

// TestScanIssueRoundTrip pins scanIssue against the issueColumnList order.
func TestScanIssueRoundTrip(t *testing.T) {
	issue := issueRepoTestIssue()
	scanned, err := scanIssue(issueRepoScanRow{values: issueArgs(issue)})
	if err != nil {
		t.Fatalf("scanIssue: %v", err)
	}
	if !reflect.DeepEqual(scanned, issue) {
		t.Fatalf("scanned issue = %#v, want %#v", scanned, issue)
	}

	minimal := domain.Issue{
		ID: 1, VersionID: 2, Source: domain.SourceHuman, Category: domain.CategoryOther,
		Severity: domain.SeverityInfo, Status: domain.StatusOpen, Confidence: domain.ConfidenceLow,
		Title: "t", CreatedBy: 1, UpdatedBy: 1, CreatedAt: issueRepoTestTime, UpdatedAt: issueRepoTestTime,
	}
	scanned, err = scanIssue(issueRepoScanRow{values: issueArgs(minimal)})
	if err != nil {
		t.Fatalf("scanIssue(minimal): %v", err)
	}
	if scanned.BatchID != nil || scanned.CaseRunID != nil || scanned.CaseNodeID != nil ||
		scanned.StepResultID != nil || scanned.ConfirmedBy != nil || scanned.DismissedBy != nil ||
		scanned.ConfirmedAt != nil || scanned.DismissedAt != nil {
		t.Fatalf("NULL columns were not cleared: %+v", scanned)
	}
	if scanned.FailureClass != "" || scanned.DedupeKey != "" {
		t.Fatalf("NULL text columns = %q/%q, want empty", scanned.FailureClass, scanned.DedupeKey)
	}
	if !reflect.DeepEqual(scanned, minimal) {
		t.Fatalf("scanned minimal issue = %#v, want %#v", scanned, minimal)
	}
}

// TestListCursorSubqueriesAreVersionScoped pins the keyset predicate: the cursor
// row's severity rank is resolved inside the same version, so a cursor id can
// never leak another version's ranking.
func TestListCursorSubqueriesAreVersionScoped(t *testing.T) {
	rows := &issueRepoFakeRows{rows: [][]any{
		issueArgs(domain.Issue{ID: 30, VersionID: 7, Severity: domain.SeverityMajor, Title: "c"}),
	}}
	q := &issueRepoFakeQuerier{queryRows: rows}
	repo := &IssueRepository{q: q}

	issues, hasMore, err := repo.List(context.Background(), 7, application.IssueFilter{
		Status: domain.StatusOpen, Severity: domain.SeverityMajor,
		Query: "登录", Cursor: 900, Limit: 2,
	})
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	if len(issues) != 1 || issues[0].ID != 30 || hasMore {
		t.Fatalf("List = %+v more=%v, want issue 30/no more", issues, hasMore)
	}
	if got := strings.Count(q.querySQL, "FROM issue WHERE id = $9 AND version_id = $1"); got != 2 {
		t.Fatalf("cursor subqueries = %d, want 2 version-scoped severity lookups\n%s", got, q.querySQL)
	}
	if !strings.Contains(q.querySQL, "LIMIT $10") {
		t.Fatalf("list SQL does not bind the limit+1 argument:\n%s", q.querySQL)
	}
	if len(q.queryArgs) != 10 {
		t.Fatalf("list args = %d, want 10", len(q.queryArgs))
	}
	wantArgs := []any{
		int64(7), domain.StatusOpen, domain.SeverityMajor, "", nil, nil, nil, "登录", int64(900), 3,
	}
	if !reflect.DeepEqual(q.queryArgs, wantArgs) {
		t.Fatalf("list args = %#v, want %#v", q.queryArgs, wantArgs)
	}
}

// TestListTrimsProbeRow pins the limit+1 probe: the repository drops the extra
// row and reports hasMore.
func TestListTrimsProbeRow(t *testing.T) {
	rows := &issueRepoFakeRows{rows: [][]any{
		issueArgs(domain.Issue{ID: 30, VersionID: 7, Severity: domain.SeverityMajor, Title: "a"}),
		issueArgs(domain.Issue{ID: 20, VersionID: 7, Severity: domain.SeverityMajor, Title: "b"}),
		issueArgs(domain.Issue{ID: 10, VersionID: 7, Severity: domain.SeverityMajor, Title: "c"}),
	}}
	repo := &IssueRepository{q: &issueRepoFakeQuerier{queryRows: rows}}
	issues, hasMore, err := repo.List(context.Background(), 7, application.IssueFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	if len(issues) != 2 || issues[0].ID != 30 || issues[1].ID != 20 || !hasMore {
		t.Fatalf("List = %+v more=%v, want [30 20]/true", issues, hasMore)
	}
}

// TestUpdateCASFailureDisambiguation pins the no-row split: an existing row with
// a stale revision is issue_state_conflict, a missing/foreign row is
// issue_not_found, and the CAS binds the expected revision as parameter 32.
func TestUpdateCASFailureDisambiguation(t *testing.T) {
	issue := issueRepoTestIssue()

	stale := &issueRepoFakeQuerier{
		updateErr:    pgx.ErrNoRows,
		rereadValues: []any{issue.ID},
	}
	_, err := (&IssueRepository{q: stale}).Update(context.Background(), issue, issue.Revision-1)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindStateConflict ||
		domainErr.Reason != domain.ReasonIssueStateConflict {
		t.Fatalf("stale CAS error = %v, want issue_state_conflict", err)
	}
	if len(stale.queryRowSQLs) != 2 {
		t.Fatalf("CAS queries = %d, want UPDATE plus one re-read", len(stale.queryRowSQLs))
	}
	if !strings.Contains(stale.queryRowSQLs[0], "revision = $32") {
		t.Fatalf("update SQL does not CAS on $32:\n%s", stale.queryRowSQLs[0])
	}
	updateArgs := stale.queryRowArgs[0]
	if len(updateArgs) != 32 || updateArgs[31] != issue.Revision-1 {
		t.Fatalf("update args = %d, expectedRevision arg = %v", len(updateArgs), updateArgs[len(updateArgs)-1])
	}

	missing := &issueRepoFakeQuerier{updateErr: pgx.ErrNoRows, rereadErr: pgx.ErrNoRows}
	_, err = (&IssueRepository{q: missing}).Update(context.Background(), issue, issue.Revision)
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindNotFound ||
		domainErr.Reason != "issue_not_found" {
		t.Fatalf("missing CAS error = %v, want issue_not_found", err)
	}
}

// TestUpdateSuccessReturnsScannedRow pins the UPDATE ... RETURNING path.
func TestUpdateSuccessReturnsScannedRow(t *testing.T) {
	issue := issueRepoTestIssue()
	q := &issueRepoFakeQuerier{updateValues: issueArgs(issue)}
	updated, err := (&IssueRepository{q: q}).Update(context.Background(), issue, issue.Revision-1)
	if err != nil {
		t.Fatalf("Update = %v", err)
	}
	if !reflect.DeepEqual(updated, issue) {
		t.Fatalf("updated = %+v, want the returned row", updated)
	}
	if len(q.queryRowSQLs) != 1 {
		t.Fatalf("CAS queries = %d, want only the UPDATE", len(q.queryRowSQLs))
	}
}

// TestCreateMapsDedupeUniqueViolation pins spec §6.9: the repository surfaces
// the lost (version_id, dedupe_key) race as the internal retry signal, never a
// raw 409 error.
func TestCreateMapsDedupeUniqueViolation(t *testing.T) {
	issue := issueRepoTestIssue()
	q := &issueRepoFakeQuerier{execErr: &pgconn.PgError{
		Code: "23505", ConstraintName: "uk_issue_version_dedupe",
	}}
	_, err := (&IssueRepository{q: q}).Create(context.Background(), issue, nil)
	if !errors.Is(err, application.ErrIssueDedupeConflict) {
		t.Fatalf("Create error = %v, want ErrIssueDedupeConflict", err)
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		t.Fatalf("dedupe race leaked a domain error: %+v", domainErr)
	}

	other := &issueRepoFakeQuerier{execErr: &pgconn.PgError{Code: "23505", ConstraintName: "uk_other"}}
	if _, err := (&IssueRepository{q: other}).Create(context.Background(), issue, nil); err == nil ||
		errors.Is(err, application.ErrIssueDedupeConflict) {
		t.Fatalf("unrelated unique violation = %v, want a wrapped store error", err)
	}
}

// TestCreateInsertsIssueAndRefs pins the insert/ref statements of one create.
func TestCreateInsertsIssueAndRefs(t *testing.T) {
	issue := issueRepoTestIssue()
	q := &issueRepoFakeQuerier{}
	_, err := (&IssueRepository{q: q}).Create(context.Background(), issue, []domain.IssueRef{
		{RefKind: domain.RefKindCaseNode, RefID: "800"},
	})
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	if len(q.execSQLs) != 2 {
		t.Fatalf("exec statements = %d, want issue insert plus ref insert", len(q.execSQLs))
	}
	if !strings.Contains(q.execSQLs[0], "INSERT INTO issue (") {
		t.Fatalf("first statement is not the issue insert:\n%s", q.execSQLs[0])
	}
	if !strings.Contains(q.execSQLs[1], "ON CONFLICT (issue_id, ref_kind, ref_id) DO NOTHING") {
		t.Fatalf("ref insert is not idempotent:\n%s", q.execSQLs[1])
	}
	if len(q.execArgs[0]) != 31 {
		t.Fatalf("insert args = %d, want 31", len(q.execArgs[0]))
	}
}

// TestFindByDedupeKeyEmptySkipsQuery pins the human-issue path: a null dedupe key
// never queries the unique lookup.
func TestFindByDedupeKeyEmptySkipsQuery(t *testing.T) {
	q := &issueRepoFakeQuerier{}
	_, found, err := (&IssueRepository{q: q}).FindByDedupeKey(context.Background(), 7, "")
	if err != nil || found {
		t.Fatalf("FindByDedupeKey(\"\") = found %v err %v, want false/nil", found, err)
	}
	if len(q.queryRowSQLs) != 0 {
		t.Fatalf("empty dedupe key still queried: %v", q.queryRowSQLs)
	}
}
