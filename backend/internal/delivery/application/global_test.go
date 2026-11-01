package deliveryapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var errGlobalTestBoom = errors.New("global test boom")

var globalTestTime = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)

// fakeGlobalIssueReader records the filter of every cross-version issue read.
type fakeGlobalIssueReader struct {
	result []delivery.Issue
	err    error
	calls  []GlobalIssueFilter
}

func (f *fakeGlobalIssueReader) ListGlobalIssues(_ context.Context, filter GlobalIssueFilter) ([]delivery.Issue, error) {
	f.calls = append(f.calls, filter)
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// fakeGlobalReportReader records the filter of every cross-version report read.
type fakeGlobalReportReader struct {
	result []GlobalReportRow
	err    error
	calls  []GlobalReportFilter
}

func (f *fakeGlobalReportReader) ListGlobalReports(_ context.Context, filter GlobalReportFilter) ([]GlobalReportRow, error) {
	f.calls = append(f.calls, filter)
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// fakeVersionTitles is the version application VersionTitles port.
type fakeVersionTitles struct {
	titles map[int64]string
	err    error
	calls  [][]int64
}

func (f *fakeVersionTitles) VersionTitles(_ context.Context, ids []int64) (map[int64]string, error) {
	f.calls = append(f.calls, append([]int64(nil), ids...))
	if f.err != nil {
		return nil, f.err
	}
	return f.titles, nil
}

func globalIssue(id, versionID int64) delivery.Issue {
	return delivery.Issue{
		ID:               id,
		VersionID:        versionID,
		Title:            "结算失败",
		Summary:          "expected 200, got 500",
		Status:           delivery.IssueStatusOpen,
		Category:         "api",
		Severity:         "major",
		Source:           delivery.IssueCandidateSource,
		ExpectedRevision: 1,
		ReopenCount:      2,
		CreatedAt:        globalTestTime,
		UpdatedAt:        globalTestTime,
	}
}

func globalReport(id, versionID int64, status delivery.ReportStatus) delivery.Report {
	return delivery.Report{
		ID:        id,
		VersionID: versionID,
		Kind:      delivery.ReportKindVersion,
		CurrentRevisionID: func() *int64 {
			revisionID := id + 1000
			return &revisionID
		}(),
		Status:           status,
		InputFingerprint: strings.Repeat("a", 64),
		CreatedAt:        globalTestTime,
		UpdatedAt:        globalTestTime,
	}
}

func globalRevision(reportID int64, revision int32) delivery.ReportRevision {
	return delivery.ReportRevision{
		ID:               reportID + 1000,
		ReportID:         reportID,
		Revision:         revision,
		Status:           delivery.ReportStatusPublished,
		HTMLRef:          func() *int64 { ref := int64(500); return &ref }(),
		InputFingerprint: strings.Repeat("b", 64),
		AsOf:             globalTestTime,
		TemplateVersion:  "1",
		RenderStatus:     "done",
		CreatedAt:        globalTestTime,
		UpdatedAt:        globalTestTime,
	}
}

func globalTitles() *fakeVersionTitles {
	return &fakeVersionTitles{titles: map[int64]string{
		42: "版本 42",
		43: "版本 43",
	}}
}

/* --------------------------------- issues --------------------------------- */

func TestListGlobalIssuesProjectsMergedChain(t *testing.T) {
	merged := globalIssue(103, 42)
	merged.Status = delivery.IssueStatusMerged
	merged.MergedIntoID = issueServiceTestInt64(104)
	repo := &fakeIssueRepository{
		getIssues: map[int64]delivery.Issue{
			103: merged,
			104: {ID: 104, VersionID: 42, Status: delivery.IssueStatusOpen},
		},
		getErrByID: map[int64]error{},
	}
	service := NewIssueService(repo, nil, nil, nil, nil, nil).
		WithGlobalReader(&fakeGlobalIssueReader{result: []delivery.Issue{merged}}, globalTitles())

	views, _, err := service.ListGlobal(context.Background(), GlobalIssueFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListGlobal() error = %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	if chain := views[0].MergedChain; len(chain) != 1 || chain[0] != 104 {
		t.Fatalf("MergedChain = %v, want [104]", chain)
	}
}

func TestListGlobalIssuesWithoutRepositorySkipsMergedChain(t *testing.T) {
	// Older wirings may resolve global rows without an IssueRepository; the
	// optional chain simply stays absent instead of failing the page.
	merged := globalIssue(103, 42)
	merged.Status = delivery.IssueStatusMerged
	merged.MergedIntoID = issueServiceTestInt64(104)
	service := NewIssueService(nil, nil, nil, nil, nil, nil).
		WithGlobalReader(&fakeGlobalIssueReader{result: []delivery.Issue{merged}}, globalTitles())

	views, _, err := service.ListGlobal(context.Background(), GlobalIssueFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListGlobal() error = %v", err)
	}
	if len(views) != 1 || len(views[0].MergedChain) != 0 {
		t.Fatalf("views = %+v, want no merged chain", views)
	}
}

func TestGlobalIssueListPaginatesAndResolvesVersionTitles(t *testing.T) {
	reader := &fakeGlobalIssueReader{result: []delivery.Issue{
		globalIssue(103, 42), globalIssue(102, 43), globalIssue(101, 42),
	}}
	titles := globalTitles()
	service := NewIssueService(nil, nil, nil, nil, nil, nil).
		WithGlobalReader(reader, titles)

	views, hasMore, err := service.ListGlobal(context.Background(), GlobalIssueFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListGlobal: %v", err)
	}
	if !hasMore {
		t.Fatal("hasMore = false, want true for a full page plus one probe row")
	}
	if len(views) != 2 {
		t.Fatalf("views = %d, want 2 (the probe row must be trimmed)", len(views))
	}
	if len(reader.calls) != 1 || reader.calls[0].Limit != 3 {
		t.Fatalf("reader limit calls = %#v, want one read with Limit=3", reader.calls)
	}
	if views[0].VersionTitle != "版本 42" || views[1].VersionTitle != "版本 43" {
		t.Fatalf("version titles = %q/%q", views[0].VersionTitle, views[1].VersionTitle)
	}
	if views[0].URL != "/versions/42?tab=issues&issue=103" ||
		views[1].URL != "/versions/43?tab=issues&issue=102" {
		t.Fatalf("URLs = %q/%q", views[0].URL, views[1].URL)
	}
	if views[0].Issue.ReopenCount != 2 {
		t.Fatalf("reopen count = %d, want 2", views[0].Issue.ReopenCount)
	}
	// Version titles are one deduplicated batch for the page.
	if len(titles.calls) != 1 {
		t.Fatalf("VersionTitles calls = %d, want 1", len(titles.calls))
	}
	got := titles.calls[0]
	if len(got) != 2 || got[0] != 42 || got[1] != 43 {
		t.Fatalf("version title ids = %v, want [42 43]", got)
	}
}

func TestGlobalIssueListForwardsFiltersAndNormalizesQuery(t *testing.T) {
	reader := &fakeGlobalIssueReader{}
	service := NewIssueService(nil, nil, nil, nil, nil, nil).
		WithGlobalReader(reader, globalTitles())

	versionID := int64(42)
	_, _, err := service.ListGlobal(context.Background(), GlobalIssueFilter{
		VersionID: &versionID,
		Status:    "merged",
		Severity:  "minor",
		Category:  "ui",
		Q:         "  结算  ",
		Before:    900,
		Limit:     50,
	})
	if err != nil {
		t.Fatalf("ListGlobal: %v", err)
	}
	if len(reader.calls) != 1 {
		t.Fatalf("reader calls = %d, want 1", len(reader.calls))
	}
	got := reader.calls[0]
	if got.VersionID == nil || *got.VersionID != 42 || got.Status != "merged" ||
		got.Severity != "minor" || got.Category != "ui" || got.Q != "结算" ||
		got.Before != 900 || got.Limit != 51 {
		t.Fatalf("forwarded filter = %#v", got)
	}
}

func TestGlobalIssueListEmptyPageDoesNotResolveTitles(t *testing.T) {
	titles := globalTitles()
	service := NewIssueService(nil, nil, nil, nil, nil, nil).
		WithGlobalReader(&fakeGlobalIssueReader{}, titles)

	views, hasMore, err := service.ListGlobal(context.Background(), GlobalIssueFilter{})
	if err != nil {
		t.Fatalf("ListGlobal: %v", err)
	}
	if len(views) != 0 || hasMore {
		t.Fatalf("empty page = %d views, hasMore=%v", len(views), hasMore)
	}
	if len(titles.calls) != 0 {
		t.Fatalf("VersionTitles calls = %d, want 0 for an empty page", len(titles.calls))
	}
}

func TestGlobalIssueListRejectsInvalidFilters(t *testing.T) {
	versionID := int64(0)
	cases := []struct {
		name   string
		filter GlobalIssueFilter
	}{
		{name: "version", filter: GlobalIssueFilter{VersionID: &versionID}},
		{name: "status", filter: GlobalIssueFilter{Status: "bogus"}},
		{name: "category", filter: GlobalIssueFilter{Category: "security"}},
		{name: "severity", filter: GlobalIssueFilter{Severity: "critical"}},
		{name: "query", filter: GlobalIssueFilter{Q: strings.Repeat("结", 101)}},
		{name: "before", filter: GlobalIssueFilter{Before: -1}},
		{name: "limit", filter: GlobalIssueFilter{Limit: -1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeGlobalIssueReader{}
			service := NewIssueService(nil, nil, nil, nil, nil, nil).
				WithGlobalReader(reader, globalTitles())
			if _, _, err := service.ListGlobal(context.Background(), test.filter); err == nil {
				t.Fatal("expected an invalid argument error")
			}
			if len(reader.calls) != 0 {
				t.Fatalf("reader calls = %d, want 0", len(reader.calls))
			}
		})
	}
}

func TestGlobalIssueListSurfacesReaderAndTitleErrors(t *testing.T) {
	t.Run("reader", func(t *testing.T) {
		reader := &fakeGlobalIssueReader{err: errGlobalTestBoom}
		service := NewIssueService(nil, nil, nil, nil, nil, nil).
			WithGlobalReader(reader, globalTitles())
		if _, _, err := service.ListGlobal(context.Background(), GlobalIssueFilter{Limit: 5}); !errors.Is(err, errGlobalTestBoom) {
			t.Fatalf("error = %v, want the reader error", err)
		}
	})

	t.Run("titles", func(t *testing.T) {
		titles := &fakeVersionTitles{err: errGlobalTestBoom}
		reader := &fakeGlobalIssueReader{result: []delivery.Issue{globalIssue(101, 42)}}
		service := NewIssueService(nil, nil, nil, nil, nil, nil).
			WithGlobalReader(reader, titles)
		if _, _, err := service.ListGlobal(context.Background(), GlobalIssueFilter{Limit: 5}); !errors.Is(err, errGlobalTestBoom) {
			t.Fatalf("error = %v, want the title reader error", err)
		}
	})

	t.Run("missing title", func(t *testing.T) {
		reader := &fakeGlobalIssueReader{result: []delivery.Issue{globalIssue(101, 99)}}
		service := NewIssueService(nil, nil, nil, nil, nil, nil).
			WithGlobalReader(reader, globalTitles())
		views, _, err := service.ListGlobal(context.Background(), GlobalIssueFilter{Limit: 5})
		if err != nil {
			t.Fatalf("ListGlobal: %v", err)
		}
		if len(views) != 1 || views[0].VersionTitle != "" {
			t.Fatalf("missing title view = %#v", views)
		}
	})
}

func TestGlobalIssueListRequiresWiring(t *testing.T) {
	if _, _, err := NewIssueService(nil, nil, nil, nil, nil, nil).
		ListGlobal(context.Background(), GlobalIssueFilter{}); err == nil {
		t.Fatal("expected an unavailable error without a global reader")
	}
	if _, _, err := NewIssueService(nil, nil, nil, nil, nil, nil).
		WithGlobalReader(&fakeGlobalIssueReader{}, nil).
		ListGlobal(context.Background(), GlobalIssueFilter{}); err == nil {
		t.Fatal("expected an unavailable error without a version title reader")
	}
	var unavailable *platform.Error
	if _, _, err := NewIssueService(nil, nil, nil, nil, nil, nil).
		ListGlobal(context.Background(), GlobalIssueFilter{}); !errors.As(err, &unavailable) ||
		unavailable.Status != 503 {
		t.Fatalf("missing wiring error = %v, want a 503 platform error", err)
	}
}

/* --------------------------------- reports -------------------------------- */

func TestGlobalReportListProjectsCurrentRevisionAndTitles(t *testing.T) {
	report := globalReport(701, 42, delivery.ReportStatusPublished)
	revision := globalRevision(701, 3)
	reader := &fakeGlobalReportReader{result: []GlobalReportRow{
		{Report: report, CurrentRevision: &revision},
		{Report: globalReport(702, 43, delivery.ReportStatusDraft)},
		{Report: globalReport(703, 42, delivery.ReportStatusStale)},
	}}
	titles := globalTitles()
	service := NewReportService(nil, nil, nil, nil, nil, nil, nil, nil, delivery.SectionOptions{}).
		WithGlobalReader(reader, titles)

	views, hasMore, err := service.ListGlobal(context.Background(), GlobalReportFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListGlobal: %v", err)
	}
	if !hasMore || len(views) != 2 {
		t.Fatalf("page = %d views, hasMore=%v", len(views), hasMore)
	}
	if len(reader.calls) != 1 || reader.calls[0].Limit != 3 {
		t.Fatalf("reader calls = %#v", reader.calls)
	}
	if views[0].CurrentRevision == nil || views[0].CurrentRevision.Revision != 3 {
		t.Fatalf("current revision = %#v", views[0].CurrentRevision)
	}
	if views[1].CurrentRevision != nil {
		t.Fatalf("missing current revision = %#v, want nil", views[1].CurrentRevision)
	}
	if views[0].VersionTitle != "版本 42" || views[0].URL != "/versions/42?tab=reports&report=701" {
		t.Fatalf("view = %#v", views[0])
	}
	if len(titles.calls) != 1 || len(titles.calls[0]) != 2 {
		t.Fatalf("title batch = %#v, want one call with 2 IDs", titles.calls)
	}
}

func TestGlobalReportListForwardsFilters(t *testing.T) {
	reader := &fakeGlobalReportReader{}
	service := NewReportService(nil, nil, nil, nil, nil, nil, nil, nil, delivery.SectionOptions{}).
		WithGlobalReader(reader, globalTitles())

	versionID := int64(43)
	_, _, err := service.ListGlobal(context.Background(), GlobalReportFilter{
		VersionID: &versionID,
		Status:    "stale",
		Kind:      "wave",
		Before:    800,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListGlobal: %v", err)
	}
	got := reader.calls[0]
	if got.VersionID == nil || *got.VersionID != 43 || got.Status != "stale" ||
		got.Kind != "wave" || got.Before != 800 || got.Limit != 11 {
		t.Fatalf("forwarded filter = %#v", got)
	}
}

func TestGlobalReportListRejectsInvalidFilters(t *testing.T) {
	versionID := int64(0)
	cases := []struct {
		name   string
		filter GlobalReportFilter
	}{
		{name: "version", filter: GlobalReportFilter{VersionID: &versionID}},
		{name: "status", filter: GlobalReportFilter{Status: "deleted"}},
		{name: "kind", filter: GlobalReportFilter{Kind: "snapshot"}},
		{name: "before", filter: GlobalReportFilter{Before: -1}},
		{name: "limit", filter: GlobalReportFilter{Limit: -1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeGlobalReportReader{}
			service := NewReportService(nil, nil, nil, nil, nil, nil, nil, nil, delivery.SectionOptions{}).
				WithGlobalReader(reader, globalTitles())
			if _, _, err := service.ListGlobal(context.Background(), test.filter); err == nil {
				t.Fatal("expected an invalid argument error")
			}
			if len(reader.calls) != 0 {
				t.Fatalf("reader calls = %d, want 0", len(reader.calls))
			}
		})
	}
}

func TestGlobalReportListEmptyAndErrors(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		titles := globalTitles()
		service := NewReportService(nil, nil, nil, nil, nil, nil, nil, nil, delivery.SectionOptions{}).
			WithGlobalReader(&fakeGlobalReportReader{}, titles)
		views, hasMore, err := service.ListGlobal(context.Background(), GlobalReportFilter{})
		if err != nil {
			t.Fatalf("ListGlobal: %v", err)
		}
		if len(views) != 0 || hasMore {
			t.Fatalf("empty page = %d views, hasMore=%v", len(views), hasMore)
		}
		if len(titles.calls) != 0 {
			t.Fatalf("title calls = %d, want 0", len(titles.calls))
		}
	})

	t.Run("reader", func(t *testing.T) {
		service := NewReportService(nil, nil, nil, nil, nil, nil, nil, nil, delivery.SectionOptions{}).
			WithGlobalReader(&fakeGlobalReportReader{err: errGlobalTestBoom}, globalTitles())
		if _, _, err := service.ListGlobal(context.Background(), GlobalReportFilter{Limit: 5}); !errors.Is(err, errGlobalTestBoom) {
			t.Fatalf("error = %v, want the reader error", err)
		}
	})

	t.Run("wiring", func(t *testing.T) {
		service := NewReportService(nil, nil, nil, nil, nil, nil, nil, nil, delivery.SectionOptions{})
		_, _, err := service.ListGlobal(context.Background(), GlobalReportFilter{})
		var unavailable *platform.Error
		if !errors.As(err, &unavailable) || unavailable.Status != 503 {
			t.Fatalf("error = %v, want a 503 platform error", err)
		}
	})
}
