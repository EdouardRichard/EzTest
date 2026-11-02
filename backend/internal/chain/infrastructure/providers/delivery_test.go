package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type stubDeliveryReader struct{}

func (stubDeliveryReader) List(context.Context, int64, deliveryapp.IssueFilter) ([]delivery.Issue, error) {
	panic("not used")
}

func (stubDeliveryReader) ListRefs(context.Context, int64) ([]delivery.IssueRef, error) {
	panic("not used")
}

func (stubDeliveryReader) ListReports(context.Context, int64, deliveryapp.ReportFilter) ([]delivery.Report, error) {
	panic("not used")
}

func (stubDeliveryReader) ListRevisions(context.Context, int64) ([]delivery.ReportRevision, error) {
	panic("not used")
}

type stubReportReader struct{}

func (stubReportReader) ListReports(context.Context, int64, deliveryapp.ReportFilter) ([]delivery.Report, error) {
	panic("not used")
}

func (stubReportReader) ListRevisions(context.Context, int64) ([]delivery.ReportRevision, error) {
	panic("not used")
}

func (stubReportReader) ListRefs(context.Context, int64) ([]delivery.ReportRef, error) {
	panic("not used")
}

type stubRequirementLister struct {
	views      []versionapp.RequirementView
	err        error
	versionIDs []int64
}

func (s *stubRequirementLister) List(_ context.Context, versionID int64, _ string, _ int, _ *int64) ([]versionapp.RequirementView, bool, error) {
	s.versionIDs = append(s.versionIDs, versionID)
	if s.err != nil {
		return nil, false, s.err
	}
	return s.views, false, nil
}

type stubClosureSummaryReader struct {
	summaries map[int64]versionapp.ClosureSummary
	err       error
	versionID int64
	requests  [][]int64
	callCount int
}

func (s *stubClosureSummaryReader) SummariesByRequirementIDs(_ context.Context, versionID int64, requirementIDs []int64) (map[int64]versionapp.ClosureSummary, error) {
	s.callCount++
	s.versionID = versionID
	s.requests = append(s.requests, append([]int64(nil), requirementIDs...))
	if s.err != nil {
		return nil, s.err
	}
	return s.summaries, nil
}

func TestDeliveryProviderClosuresUseVersionApplicationSummaries(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	summaries := &stubClosureSummaryReader{summaries: map[int64]versionapp.ClosureSummary{
		1001: {
			ID: 3001, RequirementID: 1001, CloseRound: 2,
			Status: versionapp.ClosureSummaryStatusClosed, ClosedAt: now.Add(-2 * time.Hour),
		},
		1002: {
			ID: 3002, RequirementID: 1002, CloseRound: 1,
			Status: versionapp.ClosureSummaryStatusReopened, ReopenCount: 2,
			ClosedAt: now.Add(-time.Hour),
		},
	}}
	provider := NewDeliveryProvider(
		stubDeliveryReader{},
		stubReportReader{},
		&stubRequirementLister{views: []versionapp.RequirementView{
			{ID: 1001}, {ID: 1002}, {ID: 1003},
		}},
		summaries,
	)

	nodes, err := provider.Closures(context.Background(), 42)
	if err != nil {
		t.Fatalf("Closures: %v", err)
	}
	if summaries.callCount != 1 {
		t.Fatalf("closure summary reader calls = %d, want 1", summaries.callCount)
	}
	if summaries.versionID != 42 {
		t.Errorf("summary version = %d, want 42", summaries.versionID)
	}
	if len(summaries.requests) != 1 || len(summaries.requests[0]) != 3 {
		t.Fatalf("summary requests = %v, want one request for three requirements", summaries.requests)
	}
	for index, want := range []int64{1001, 1002, 1003} {
		if summaries.requests[0][index] != want {
			t.Errorf("summary request[%d] = %d, want %d", index, summaries.requests[0][index], want)
		}
	}
	if len(nodes) != 2 {
		t.Fatalf("closure nodes = %+v, want 2", nodes)
	}

	closed := nodes[0]
	if closed.ID != "closure:3001" || closed.Kind != chain.NodeClosure || closed.RefID != "3001" {
		t.Errorf("closed node identity = %+v", closed)
	}
	if closed.Status != versionapp.ClosureSummaryStatusClosed {
		t.Errorf("closed node status = %q, want closed", closed.Status)
	}
	if closed.RequirementID == nil || *closed.RequirementID != 1001 {
		t.Errorf("closed node requirement = %v, want 1001", closed.RequirementID)
	}
	if !closed.CreatedAt.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("closed node created_at = %v", closed.CreatedAt)
	}
	if closed.Meta["close_round"] != 2 || closed.Meta["reopen_count"] != 0 {
		t.Errorf("closed node meta = %v", closed.Meta)
	}
	if closedAt, ok := closed.Meta["closed_at"].(time.Time); !ok || !closedAt.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("closed node meta closed_at = %v", closed.Meta["closed_at"])
	}

	reopened := nodes[1]
	if reopened.Status != versionapp.ClosureSummaryStatusReopened {
		t.Errorf("reopened node status = %q, want reopened", reopened.Status)
	}
	if reopened.Meta["reopen_count"] != 2 {
		t.Errorf("reopened node meta = %v", reopened.Meta)
	}
}

func TestDeliveryProviderPropagatesClosureSummaryErrors(t *testing.T) {
	boom := errors.New("closure read down")
	provider := NewDeliveryProvider(
		stubDeliveryReader{},
		stubReportReader{},
		&stubRequirementLister{views: []versionapp.RequirementView{{ID: 1001}}},
		&stubClosureSummaryReader{err: boom},
	)

	if _, err := provider.Closures(context.Background(), 42); !errors.Is(err, boom) {
		t.Fatalf("Closures error = %v, want the summary reader error", err)
	}
}
