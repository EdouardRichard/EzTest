package providers

import (
	"context"
	"errors"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// fakeDeliveryQueries implements DeliveryQueries without touching the delivery
// context internals.
type fakeDeliveryQueries struct {
	issueHits  []deliveryapp.SearchHit
	reportHits []deliveryapp.SearchHit
	issueErr   error
	reportErr  error

	issueCalls  int
	reportCalls int
	issueQ      string
	reportQ     string
	issueLimit  int
	reportLimit int
}

func (f *fakeDeliveryQueries) SearchIssues(_ context.Context, q string, limit int) ([]deliveryapp.SearchHit, error) {
	f.issueCalls++
	f.issueQ = q
	f.issueLimit = limit
	return f.issueHits, f.issueErr
}

func (f *fakeDeliveryQueries) SearchReports(_ context.Context, q string, limit int) ([]deliveryapp.SearchHit, error) {
	f.reportCalls++
	f.reportQ = q
	f.reportLimit = limit
	return f.reportHits, f.reportErr
}

func TestIssueSearcherMapsOwnedHits(t *testing.T) {
	query := &fakeDeliveryQueries{issueHits: []deliveryapp.SearchHit{{
		ID:           400,
		Title:        "登录失败",
		Subtitle:     "",
		VersionID:    11,
		VersionTitle: "版本甲",
		Status:       "open",
		URL:          "/versions/11?tab=issues&issue=400",
	}}}
	searcher := NewIssueSearcher(query)

	items, err := searcher.Search(context.Background(), "登录", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if query.issueCalls != 1 || query.reportCalls != 0 {
		t.Fatalf("calls = issue:%d report:%d, want 1/0", query.issueCalls, query.reportCalls)
	}
	if query.issueQ != "登录" || query.issueLimit != 2 {
		t.Fatalf("forwarded q/limit = %q/%d, want 登录/2", query.issueQ, query.issueLimit)
	}
	want := searchapp.Item{
		ID:           "400",
		Title:        "登录失败",
		Subtitle:     "",
		VersionID:    "11",
		VersionTitle: "版本甲",
		Status:       "open",
		URL:          "/versions/11?tab=issues&issue=400",
	}
	if len(items) != 1 || items[0] != want {
		t.Fatalf("items = %+v, want %+v", items, want)
	}
}

func TestReportSearcherMapsOwnedHits(t *testing.T) {
	query := &fakeDeliveryQueries{reportHits: []deliveryapp.SearchHit{{
		ID:           500,
		Title:        "波次报告",
		Subtitle:     "stale",
		VersionID:    11,
		VersionTitle: "版本甲",
		Status:       "stale",
		URL:          "/versions/11?tab=reports&report=500",
	}}}
	searcher := NewReportSearcher(query)

	items, err := searcher.Search(context.Background(), "报告", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if query.issueCalls != 0 || query.reportCalls != 1 {
		t.Fatalf("calls = issue:%d report:%d, want 0/1", query.issueCalls, query.reportCalls)
	}
	want := searchapp.Item{
		ID:           "500",
		Title:        "波次报告",
		Subtitle:     "stale",
		VersionID:    "11",
		VersionTitle: "版本甲",
		Status:       "stale",
		URL:          "/versions/11?tab=reports&report=500",
	}
	if len(items) != 1 || items[0] != want {
		t.Fatalf("items = %+v, want %+v", items, want)
	}
}

func TestReportSearcherPropagatesErrors(t *testing.T) {
	cause := errors.New("report query failed")
	searcher := NewReportSearcher(&fakeDeliveryQueries{reportErr: cause})

	if _, err := searcher.Search(context.Background(), "report", 5); !errors.Is(err, cause) {
		t.Fatalf("error = %v, want %v", err, cause)
	}
}

func TestIssueSearcherWithoutQueryFails(t *testing.T) {
	searcher := NewIssueSearcher(nil)
	if _, err := searcher.Search(context.Background(), "login", 5); err == nil {
		t.Fatal("Search with a nil query service returned nil error")
	}
}
