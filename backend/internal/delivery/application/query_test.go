package deliveryapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func deliveryP6Int64Ptr(value int64) *int64 { return &value }

// fakeP6DeliveryReader is the delivery-context P6 read source used by the query
// port tests.
type fakeP6DeliveryReader struct {
	validityNum   int
	validityDenom int
	validityErr   error
	validityCalls int
	validityScope *int64

	issueRows  []IssueSearchRow
	issueErr   error
	issueQ     string
	issueLimit int
	issueCalls int

	reportRows  []ReportSearchRow
	reportErr   error
	reportQ     string
	reportLimit int
	reportCalls int

	openRows  []OpenIssueRow
	openErr   error
	openLimit int
	openCalls int
}

func (f *fakeP6DeliveryReader) CountIssueValidity(_ context.Context, versionID *int64) (int, int, error) {
	f.validityCalls++
	f.validityScope = versionID
	return f.validityNum, f.validityDenom, f.validityErr
}

func (f *fakeP6DeliveryReader) SearchIssues(_ context.Context, q string, limit int) ([]IssueSearchRow, error) {
	f.issueCalls++
	f.issueQ, f.issueLimit = q, limit
	return f.issueRows, f.issueErr
}

func (f *fakeP6DeliveryReader) SearchReports(_ context.Context, q string, limit int) ([]ReportSearchRow, error) {
	f.reportCalls++
	f.reportQ, f.reportLimit = q, limit
	return f.reportRows, f.reportErr
}

func (f *fakeP6DeliveryReader) ListOpenIssues(_ context.Context, limit int) ([]OpenIssueRow, error) {
	f.openCalls++
	f.openLimit = limit
	return f.openRows, f.openErr
}

func TestP6CountIssueValidity(t *testing.T) {
	t.Run("empty store returns zero counts", func(t *testing.T) {
		reader := &fakeP6DeliveryReader{}
		service := NewQueryService(reader)

		num, denom, err := service.CountIssueValidity(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountIssueValidity: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
		if reader.validityCalls != 1 || reader.validityScope != nil {
			t.Fatalf("reader calls = %d scope = %v, want one global call", reader.validityCalls, reader.validityScope)
		}
	})

	t.Run("version filter is forwarded", func(t *testing.T) {
		reader := &fakeP6DeliveryReader{validityNum: 2, validityDenom: 3}
		service := NewQueryService(reader)

		num, denom, err := service.CountIssueValidity(context.Background(), deliveryP6Int64Ptr(7))
		if err != nil {
			t.Fatalf("CountIssueValidity: %v", err)
		}
		if num != 2 || denom != 3 {
			t.Fatalf("counts = %d/%d, want 2/3", num, denom)
		}
		if reader.validityScope == nil || *reader.validityScope != 7 {
			t.Fatalf("scope = %v, want 7", reader.validityScope)
		}
	})

	t.Run("zero denominator is reported without error", func(t *testing.T) {
		service := NewQueryService(&fakeP6DeliveryReader{})

		num, denom, err := service.CountIssueValidity(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountIssueValidity: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
	})

	t.Run("non-positive version is rejected", func(t *testing.T) {
		reader := &fakeP6DeliveryReader{}
		service := NewQueryService(reader)

		if _, _, err := service.CountIssueValidity(context.Background(), deliveryP6Int64Ptr(-1)); err == nil {
			t.Fatal("expected invalid_argument for version_id=-1")
		}
		if reader.validityCalls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.validityCalls)
		}
	})

	t.Run("reader error is propagated", func(t *testing.T) {
		want := errors.New("boom")
		service := NewQueryService(&fakeP6DeliveryReader{validityErr: want})

		if _, _, err := service.CountIssueValidity(context.Background(), nil); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})
}

func TestP6SearchIssues(t *testing.T) {
	reader := &fakeP6DeliveryReader{issueRows: []IssueSearchRow{
		{ID: 9, VersionID: 7, VersionTitle: "Login SUT", Title: "Login button 500", Status: "open", Severity: "major"},
	}}
	service := NewQueryService(reader)

	hits, err := service.SearchIssues(context.Background(), "  login ", 50)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if reader.issueQ != "login" {
		t.Fatalf("q = %q, want trimmed login", reader.issueQ)
	}
	if reader.issueLimit != 20 {
		t.Fatalf("limit = %d, want clamp 20", reader.issueLimit)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	hit := hits[0]
	if hit.ID != 9 || hit.Title != "Login button 500" || hit.Status != "open" {
		t.Fatalf("hit = %#v", hit)
	}
	if hit.VersionID != 7 || hit.VersionTitle != "Login SUT" {
		t.Fatalf("version fields = %#v", hit)
	}
	if hit.URL != "/versions/7?tab=issues&issue=9" {
		t.Fatalf("url = %q", hit.URL)
	}

	reader.issueRows = nil
	if _, err := service.SearchIssues(context.Background(), "x", 0); err != nil {
		t.Fatalf("SearchIssues empty: %v", err)
	}
	if reader.issueLimit != 5 {
		t.Fatalf("default limit = %d, want 5", reader.issueLimit)
	}
}

func TestP6SearchReports(t *testing.T) {
	reader := &fakeP6DeliveryReader{reportRows: []ReportSearchRow{
		{ID: 4, VersionID: 7, VersionTitle: "Login SUT", Kind: "wave", Status: "published"},
		{ID: 5, VersionID: 7, VersionTitle: "Login SUT", Kind: "version", Status: "draft"},
	}}
	service := NewQueryService(reader)

	hits, err := service.SearchReports(context.Background(), "report", 0)
	if err != nil {
		t.Fatalf("SearchReports: %v", err)
	}
	if reader.reportLimit != 5 {
		t.Fatalf("limit = %d, want default 5", reader.reportLimit)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Title != "波次报告" || hits[0].Subtitle != "published" || hits[0].Status != "published" {
		t.Fatalf("wave hit = %#v", hits[0])
	}
	if hits[0].URL != "/versions/7?tab=reports&report=4" {
		t.Fatalf("wave url = %q", hits[0].URL)
	}
	if hits[1].Title != "版本报告" || hits[1].Subtitle != "draft" {
		t.Fatalf("version hit = %#v", hits[1])
	}
	if hits[1].URL != "/versions/7?tab=reports&report=5" {
		t.Fatalf("version url = %q", hits[1].URL)
	}
}

func TestP6ListOpenIssuesBuildsTodos(t *testing.T) {
	created := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	reader := &fakeP6DeliveryReader{openRows: []OpenIssueRow{
		{ID: 1, VersionID: 7, VersionTitle: "Login SUT", Title: "major bug", Severity: "major", CreatedAt: created},
		{ID: 2, VersionID: 7, VersionTitle: "Login SUT", Title: "minor bug", Severity: "minor", CreatedAt: created},
		{ID: 3, VersionID: 7, VersionTitle: "Login SUT", Title: "trivial bug", Severity: "trivial", CreatedAt: created},
		{ID: 4, VersionID: 7, VersionTitle: "Login SUT", Title: "unknown severity", Severity: "", CreatedAt: created},
	}}
	service := NewQueryService(reader)

	items, err := service.ListOpenIssues(context.Background(), 999)
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if reader.openLimit != 200 {
		t.Fatalf("limit = %d, want clamp 200", reader.openLimit)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	wantSeverity := []string{"major", "warning", "info", "warning"}
	for i, want := range wantSeverity {
		if items[i].Kind != "issue_confirmation" {
			t.Fatalf("items[%d].kind = %q", i, items[i].Kind)
		}
		if items[i].Severity != want {
			t.Fatalf("items[%d].severity = %q, want %q", i, items[i].Severity, want)
		}
	}
	if items[0].Title != "major bug" || items[0].VersionID != 7 || items[0].VersionTitle != "Login SUT" {
		t.Fatalf("todo = %#v", items[0])
	}
	if !items[0].CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v, want %v", items[0].CreatedAt, created)
	}
	if items[0].URL != "/versions/7?tab=issues&issue=1" {
		t.Fatalf("url = %q", items[0].URL)
	}

	reader.openRows = nil
	items, err = service.ListOpenIssues(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListOpenIssues empty: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, want empty non-nil slice", items)
	}
	if reader.openLimit != 50 {
		t.Fatalf("default limit = %d, want 50", reader.openLimit)
	}
}

func TestP6DeliveryQueryServiceRequiresReader(t *testing.T) {
	service := NewQueryService(nil)
	_, _, err := service.CountIssueValidity(context.Background(), nil)
	if err == nil {
		t.Fatal("expected unavailable error without a reader")
	}
	if platform.AsError(err).Status != 503 {
		t.Fatalf("status = %d, want 503", platform.AsError(err).Status)
	}
}
