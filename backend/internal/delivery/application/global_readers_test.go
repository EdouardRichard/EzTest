package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// --- test doubles -----------------------------------------------------------

// globalIssueStoreFake records the normalized filter it receives and returns the
// scripted page. VersionName is intentionally never set: enriching it is the
// service's job.
type globalIssueStoreFake struct {
	rows    []GlobalIssueRow
	next    string
	hasMore bool
	err     error

	calls     int
	gotFilter *GlobalIssueFilter
}

func (f *globalIssueStoreFake) ListGlobalIssues(_ context.Context, filter GlobalIssueFilter) ([]GlobalIssueRow, string, bool, error) {
	f.calls++
	f.gotFilter = &filter
	if f.err != nil {
		return nil, "", false, f.err
	}
	return f.rows, f.next, f.hasMore, nil
}

// globalReportStoreFake mirrors globalIssueStoreFake for the report centre.
type globalReportStoreFake struct {
	rows    []GlobalReportRow
	next    string
	hasMore bool
	err     error

	calls     int
	gotFilter *GlobalReportFilter
}

func (f *globalReportStoreFake) ListGlobalReports(_ context.Context, filter GlobalReportFilter) ([]GlobalReportRow, string, bool, error) {
	f.calls++
	f.gotFilter = &filter
	if f.err != nil {
		return nil, "", false, f.err
	}
	return f.rows, f.next, f.hasMore, nil
}

// globalVersionNamesFake returns scripted version names and records the ids it
// was asked to resolve.
type globalVersionNamesFake struct {
	names  map[int64]string
	err    error
	calls  int
	gotIDs []int64
}

func (f *globalVersionNamesFake) VersionNames(_ context.Context, versionIDs []int64) (map[int64]string, error) {
	f.calls++
	f.gotIDs = append([]int64(nil), versionIDs...)
	if f.err != nil {
		return nil, f.err
	}
	return f.names, nil
}

// globalTestIssue builds one row of the scripted store.
func globalTestIssue(id, versionID int64) GlobalIssueRow {
	return GlobalIssueRow{
		Issue: domain.Issue{
			ID: id, VersionID: versionID, Title: "问题", Status: domain.StatusOpen,
			Severity: domain.SeverityMajor, Category: domain.CategoryDefect,
		},
		Refs: []domain.IssueRef{{IssueID: id, RefKind: domain.RefKindCaseNode, RefID: "800"}},
	}
}

// globalTestReport builds one row of the scripted report store.
func globalTestReport(id, versionID int64, batchNo int) GlobalReportRow {
	return GlobalReportRow{
		Report: domain.Report{
			ID: id, VersionID: versionID, BatchID: 10, Revision: 1,
			Status: domain.ReportStatusPublished, Title: "报告",
		},
		BatchNo: batchNo,
	}
}

// --- issue centre -----------------------------------------------------------

// TestGlobalIssueListNormalizesEnrichesAndPages proves the service clamps the
// page size to the shared 1–100 bound, fills versionName from the version-name
// port (one lookup per distinct version) and passes the opaque cursor page
// through unchanged.
func TestGlobalIssueListNormalizesEnrichesAndPages(t *testing.T) {
	issues := &globalIssueStoreFake{
		rows:    []GlobalIssueRow{globalTestIssue(9001, 101), globalTestIssue(9002, 202)},
		next:    platform.EncodeCursor(9002),
		hasMore: true,
	}
	versions := &globalVersionNamesFake{names: map[int64]string{101: "checkout v1", 202: "reports v2"}}
	service := NewGlobalReaderService(GlobalReaderDeps{Issues: issues, Versions: versions})

	rows, next, hasMore, err := service.ListGlobalIssues(context.Background(), GlobalIssueFilter{Limit: 0})
	if err != nil {
		t.Fatalf("ListGlobalIssues = %v", err)
	}
	if issues.calls != 1 || issues.gotFilter == nil {
		t.Fatalf("store calls=%d filter=%+v", issues.calls, issues.gotFilter)
	}
	if issues.gotFilter.Limit != platform.DefaultLimit {
		t.Fatalf("limit=%d, want the default %d", issues.gotFilter.Limit, platform.DefaultLimit)
	}
	if !reflect.DeepEqual(versions.gotIDs, []int64{101, 202}) {
		t.Fatalf("version name ids=%v, want one lookup per distinct version", versions.gotIDs)
	}
	if len(rows) != 2 || rows[0].VersionName != "checkout v1" || rows[1].VersionName != "reports v2" {
		t.Fatalf("rows=%+v, want enriched version names", rows)
	}
	if rows[0].Issue.ID != 9001 || len(rows[0].Refs) != 1 {
		t.Fatalf("row issue/refs lost: %+v", rows[0])
	}
	if next != platform.EncodeCursor(9002) || !hasMore {
		t.Fatalf("next=%q hasMore=%v", next, hasMore)
	}
}

// TestGlobalIssueListClampsLimit proves an oversized page size is clamped to the
// hard bound instead of being forwarded to SQL.
func TestGlobalIssueListClampsLimit(t *testing.T) {
	issues := &globalIssueStoreFake{}
	service := NewGlobalReaderService(GlobalReaderDeps{
		Issues:   issues,
		Versions: &globalVersionNamesFake{},
	})
	if _, _, _, err := service.ListGlobalIssues(context.Background(), GlobalIssueFilter{Limit: platform.MaxLimit + 1}); err != nil {
		t.Fatalf("ListGlobalIssues = %v", err)
	}
	if issues.gotFilter == nil || issues.gotFilter.Limit != platform.MaxLimit {
		t.Fatalf("limit=%+v, want %d", issues.gotFilter, platform.MaxLimit)
	}
}

// TestGlobalIssueListValidatesFilter proves every enum bound and positive-id
// rule is enforced before the store sees the filter.
func TestGlobalIssueListValidatesFilter(t *testing.T) {
	zero := int64(0)
	negative := int64(-1)
	cases := map[string]struct {
		filter GlobalIssueFilter
		field  string
	}{
		"status":     {GlobalIssueFilter{Status: "closed"}, "status"},
		"severity":   {GlobalIssueFilter{Severity: "critical"}, "severity"},
		"category":   {GlobalIssueFilter{Category: "bug"}, "category"},
		"query":      {GlobalIssueFilter{Query: strings.Repeat("界", domain.MaxIssueQueryRunes+1)}, "q"},
		"version id": {GlobalIssueFilter{VersionID: &zero}, "versionId"},
		"batch id":   {GlobalIssueFilter{BatchID: &negative}, "batchId"},
		"case run":   {GlobalIssueFilter{CaseRunID: &zero}, "caseRunId"},
		"case node":  {GlobalIssueFilter{CaseNodeID: &negative}, "caseNodeId"},
		"cursor":     {GlobalIssueFilter{Cursor: "not-a-cursor"}, "cursor"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			issues := &globalIssueStoreFake{}
			service := NewGlobalReaderService(GlobalReaderDeps{
				Issues:   issues,
				Versions: &globalVersionNamesFake{},
			})
			_, _, _, err := service.ListGlobalIssues(context.Background(), test.filter)
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindInvalid ||
				domainErr.Field != test.field {
				t.Fatalf("error=%v, want invalid field %q", err, test.field)
			}
			if issues.calls != 0 {
				t.Fatalf("store called %d times for an invalid filter", issues.calls)
			}
		})
	}
}

// TestGlobalIssueListEmptySkipsVersionNames proves an empty page is returned
// without a version-name lookup (an empty centre must not depend on the version
// reader).
func TestGlobalIssueListEmptySkipsVersionNames(t *testing.T) {
	versions := &globalVersionNamesFake{err: errors.New("must not be called")}
	service := NewGlobalReaderService(GlobalReaderDeps{
		Issues:   &globalIssueStoreFake{rows: []GlobalIssueRow{}},
		Versions: versions,
	})
	rows, next, hasMore, err := service.ListGlobalIssues(context.Background(), GlobalIssueFilter{})
	if err != nil {
		t.Fatalf("ListGlobalIssues = %v", err)
	}
	if len(rows) != 0 || next != "" || hasMore || versions.calls != 0 {
		t.Fatalf("rows=%d next=%q hasMore=%v versionCalls=%d", len(rows), next, hasMore, versions.calls)
	}
}

// TestGlobalIssueListPropagatesErrors proves store and version-name failures are
// returned (mapped to 500 by HTTP), never swallowed into an empty page.
func TestGlobalIssueListPropagatesErrors(t *testing.T) {
	storeErr := errors.New("store down")
	service := NewGlobalReaderService(GlobalReaderDeps{
		Issues:   &globalIssueStoreFake{err: storeErr},
		Versions: &globalVersionNamesFake{},
	})
	if _, _, _, err := service.ListGlobalIssues(context.Background(), GlobalIssueFilter{}); !errors.Is(err, storeErr) {
		t.Fatalf("store error=%v, want %v", err, storeErr)
	}

	nameErr := errors.New("version lookup down")
	service = NewGlobalReaderService(GlobalReaderDeps{
		Issues:   &globalIssueStoreFake{rows: []GlobalIssueRow{globalTestIssue(1, 7)}},
		Versions: &globalVersionNamesFake{err: nameErr},
	})
	if _, _, _, err := service.ListGlobalIssues(context.Background(), GlobalIssueFilter{}); !errors.Is(err, nameErr) {
		t.Fatalf("version name error=%v, want %v", err, nameErr)
	}

	service = NewGlobalReaderService(GlobalReaderDeps{Issues: &globalIssueStoreFake{rows: []GlobalIssueRow{globalTestIssue(1, 7)}}})
	if _, _, _, err := service.ListGlobalIssues(context.Background(), GlobalIssueFilter{}); err == nil {
		t.Fatal("a nil version-name port must fail closed on a non-empty page")
	}
}

// TestGlobalIssueListRequiresStore proves a mis-wired service fails instead of
// panicking.
func TestGlobalIssueListRequiresStore(t *testing.T) {
	service := NewGlobalReaderService(GlobalReaderDeps{})
	if _, _, _, err := service.ListGlobalIssues(context.Background(), GlobalIssueFilter{}); err == nil {
		t.Fatal("ListGlobalIssues without a store must fail")
	}
}

// --- report centre ----------------------------------------------------------

// TestGlobalReportListNormalizesEnrichesAndPages mirrors the issue-centre test:
// filters are validated, the page size is clamped/defaulted, versionName is
// enriched once per distinct version and batchNo survives the mapping.
func TestGlobalReportListNormalizesEnrichesAndPages(t *testing.T) {
	reports := &globalReportStoreFake{
		rows:    []GlobalReportRow{globalTestReport(9101, 101, 2), globalTestReport(9102, 202, 5)},
		next:    platform.EncodeCursor(9102),
		hasMore: true,
	}
	versions := &globalVersionNamesFake{names: map[int64]string{101: "checkout v1", 202: "reports v2"}}
	service := NewGlobalReaderService(GlobalReaderDeps{Reports: reports, Versions: versions})

	rows, next, hasMore, err := service.ListGlobalReports(context.Background(), GlobalReportFilter{
		Status: domain.ReportStatusPublished,
	})
	if err != nil {
		t.Fatalf("ListGlobalReports = %v", err)
	}
	if reports.calls != 1 || reports.gotFilter == nil {
		t.Fatalf("store calls=%d filter=%+v", reports.calls, reports.gotFilter)
	}
	if reports.gotFilter.Limit != platform.DefaultLimit {
		t.Fatalf("limit=%d, want the default %d", reports.gotFilter.Limit, platform.DefaultLimit)
	}
	if !reflect.DeepEqual(versions.gotIDs, []int64{101, 202}) {
		t.Fatalf("version name ids=%v", versions.gotIDs)
	}
	if len(rows) != 2 || rows[0].VersionName != "checkout v1" || rows[1].VersionName != "reports v2" {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[0].BatchNo != 2 || rows[1].BatchNo != 5 {
		t.Fatalf("batchNo=%d/%d, want 2/5", rows[0].BatchNo, rows[1].BatchNo)
	}
	if rows[0].Report.ID != 9101 || next != platform.EncodeCursor(9102) || !hasMore {
		t.Fatalf("row id=%d next=%q hasMore=%v", rows[0].Report.ID, next, hasMore)
	}
}

// TestGlobalReportListValidatesFilter proves the report status/version/cursor
// rules are enforced before the store sees the filter.
func TestGlobalReportListValidatesFilter(t *testing.T) {
	zero := int64(0)
	cases := map[string]struct {
		filter GlobalReportFilter
		field  string
	}{
		"status":     {GlobalReportFilter{Status: "archived"}, "status"},
		"version id": {GlobalReportFilter{VersionID: &zero}, "versionId"},
		"cursor":     {GlobalReportFilter{Cursor: "not-a-cursor"}, "cursor"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			reports := &globalReportStoreFake{}
			service := NewGlobalReaderService(GlobalReaderDeps{
				Reports:  reports,
				Versions: &globalVersionNamesFake{},
			})
			_, _, _, err := service.ListGlobalReports(context.Background(), test.filter)
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindInvalid ||
				domainErr.Field != test.field {
				t.Fatalf("error=%v, want invalid field %q", err, test.field)
			}
			if reports.calls != 0 {
				t.Fatalf("store called %d times for an invalid filter", reports.calls)
			}
		})
	}
}

// TestGlobalReportListEmptySkipsVersionNames proves an empty report centre is
// served without a version-name lookup.
func TestGlobalReportListEmptySkipsVersionNames(t *testing.T) {
	versions := &globalVersionNamesFake{err: errors.New("must not be called")}
	service := NewGlobalReaderService(GlobalReaderDeps{
		Reports:  &globalReportStoreFake{rows: []GlobalReportRow{}},
		Versions: versions,
	})
	rows, next, hasMore, err := service.ListGlobalReports(context.Background(), GlobalReportFilter{})
	if err != nil {
		t.Fatalf("ListGlobalReports = %v", err)
	}
	if len(rows) != 0 || next != "" || hasMore || versions.calls != 0 {
		t.Fatalf("rows=%d next=%q hasMore=%v versionCalls=%d", len(rows), next, hasMore, versions.calls)
	}
}

// TestGlobalReportListPropagatesErrors proves a store failure is returned.
func TestGlobalReportListPropagatesErrors(t *testing.T) {
	storeErr := errors.New("store down")
	service := NewGlobalReaderService(GlobalReaderDeps{
		Reports:  &globalReportStoreFake{err: storeErr},
		Versions: &globalVersionNamesFake{},
	})
	if _, _, _, err := service.ListGlobalReports(context.Background(), GlobalReportFilter{}); !errors.Is(err, storeErr) {
		t.Fatalf("store error=%v, want %v", err, storeErr)
	}

	service = NewGlobalReaderService(GlobalReaderDeps{Reports: &globalReportStoreFake{rows: []GlobalReportRow{globalTestReport(1, 7, 1)}}})
	if _, _, _, err := service.ListGlobalReports(context.Background(), GlobalReportFilter{}); err == nil {
		t.Fatal("a nil version-name port must fail closed on a non-empty page")
	}
}

// TestGlobalReportListRequiresStore proves a mis-wired service fails instead of
// panicking.
func TestGlobalReportListRequiresStore(t *testing.T) {
	service := NewGlobalReaderService(GlobalReaderDeps{})
	if _, _, _, err := service.ListGlobalReports(context.Background(), GlobalReportFilter{}); err == nil {
		t.Fatal("ListGlobalReports without a store must fail")
	}
}
