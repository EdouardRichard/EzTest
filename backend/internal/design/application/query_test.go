package designapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeP6DesignReader is the design-context P6 read source used by the query
// port tests. Every call is recorded so normalization and batching are visible.
type fakeP6DesignReader struct {
	countNum   int
	countDenom int
	countErr   error
	countCalls int
	countScope *int64

	caseRows   []CaseSearchRow
	caseErr    error
	caseQ      string
	caseLimit  int
	caseCalls  int
	draftRows  []DraftCaseRow
	draftErr   error
	draftLimit int
	draftCalls int
}

func (f *fakeP6DesignReader) CountCaseConfirmation(_ context.Context, versionID *int64) (int, int, error) {
	f.countCalls++
	f.countScope = versionID
	return f.countNum, f.countDenom, f.countErr
}

func (f *fakeP6DesignReader) SearchCases(_ context.Context, q string, limit int) ([]CaseSearchRow, error) {
	f.caseCalls++
	f.caseQ, f.caseLimit = q, limit
	return f.caseRows, f.caseErr
}

func (f *fakeP6DesignReader) ListDraftCases(_ context.Context, limit int) ([]DraftCaseRow, error) {
	f.draftCalls++
	f.draftLimit = limit
	return f.draftRows, f.draftErr
}

func TestP6CountCaseConfirmation(t *testing.T) {
	t.Run("empty store returns zero counts", func(t *testing.T) {
		reader := &fakeP6DesignReader{}
		service := NewQueryService(reader)

		num, denom, err := service.CountCaseConfirmation(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountCaseConfirmation: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
		if reader.countCalls != 1 || reader.countScope != nil {
			t.Fatalf("reader calls = %d scope = %v, want one global call", reader.countCalls, reader.countScope)
		}
	})

	t.Run("version filter is forwarded", func(t *testing.T) {
		reader := &fakeP6DesignReader{countNum: 4, countDenom: 6}
		service := NewQueryService(reader)

		num, denom, err := service.CountCaseConfirmation(context.Background(), int64Ptr(7))
		if err != nil {
			t.Fatalf("CountCaseConfirmation: %v", err)
		}
		if num != 4 || denom != 6 {
			t.Fatalf("counts = %d/%d, want 4/6", num, denom)
		}
		if reader.countScope == nil || *reader.countScope != 7 {
			t.Fatalf("scope = %v, want 7", reader.countScope)
		}
	})

	t.Run("zero denominator is reported without error", func(t *testing.T) {
		service := NewQueryService(&fakeP6DesignReader{countNum: 0, countDenom: 0})

		num, denom, err := service.CountCaseConfirmation(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountCaseConfirmation: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
	})

	t.Run("non-positive version is rejected", func(t *testing.T) {
		reader := &fakeP6DesignReader{}
		service := NewQueryService(reader)

		if _, _, err := service.CountCaseConfirmation(context.Background(), int64Ptr(0)); err == nil {
			t.Fatal("expected invalid_argument for version_id=0")
		}
		if reader.countCalls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.countCalls)
		}
	})

	t.Run("reader error is propagated", func(t *testing.T) {
		want := errors.New("boom")
		service := NewQueryService(&fakeP6DesignReader{countErr: want})

		if _, _, err := service.CountCaseConfirmation(context.Background(), nil); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})
}

func TestP6SearchCases(t *testing.T) {
	reader := &fakeP6DesignReader{caseRows: []CaseSearchRow{
		{CaseID: 9, CaseVersionID: 19, VersionID: 7, VersionTitle: "Login SUT", Title: "Login success", Status: "confirmed"},
	}}
	service := NewQueryService(reader)

	hits, err := service.SearchCases(context.Background(), "  login ", 50)
	if err != nil {
		t.Fatalf("SearchCases: %v", err)
	}
	if reader.caseQ != "login" {
		t.Fatalf("q = %q, want trimmed login", reader.caseQ)
	}
	if reader.caseLimit != 20 {
		t.Fatalf("limit = %d, want clamp 20", reader.caseLimit)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	hit := hits[0]
	if hit.ID != 9 || hit.Title != "Login success" || hit.Status != "confirmed" {
		t.Fatalf("hit = %#v", hit)
	}
	if hit.VersionID != 7 || hit.VersionTitle != "Login SUT" {
		t.Fatalf("version fields = %#v", hit)
	}
	if hit.URL != "/versions/7?tab=design&case=9" {
		t.Fatalf("url = %q", hit.URL)
	}

	reader.caseRows = nil
	if _, err := service.SearchCases(context.Background(), "x", 0); err != nil {
		t.Fatalf("SearchCases empty: %v", err)
	}
	if reader.caseLimit != 5 {
		t.Fatalf("default limit = %d, want 5", reader.caseLimit)
	}
}

func TestP6ListDraftCasesBuildsTodos(t *testing.T) {
	created := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	reader := &fakeP6DesignReader{draftRows: []DraftCaseRow{
		{CaseID: 9, VersionID: 7, VersionTitle: "Login SUT", Title: "Login success", CreatedAt: created},
	}}
	service := NewQueryService(reader)

	items, err := service.ListDraftCases(context.Background(), 999)
	if err != nil {
		t.Fatalf("ListDraftCases: %v", err)
	}
	if reader.draftLimit != 200 {
		t.Fatalf("limit = %d, want clamp 200", reader.draftLimit)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Kind != "case_confirmation" || item.Severity != "warning" {
		t.Fatalf("todo = %#v", item)
	}
	if item.Title != "Login success" || item.VersionID != 7 || item.VersionTitle != "Login SUT" {
		t.Fatalf("todo = %#v", item)
	}
	if !item.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v, want %v", item.CreatedAt, created)
	}
	if item.URL != "/versions/7?tab=design&case=9" {
		t.Fatalf("url = %q", item.URL)
	}

	reader.draftRows = nil
	items, err = service.ListDraftCases(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDraftCases empty: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, want empty non-nil slice", items)
	}
	if reader.draftLimit != 50 {
		t.Fatalf("default limit = %d, want 50", reader.draftLimit)
	}
}

func TestP6DesignQueryServiceRequiresReader(t *testing.T) {
	service := NewQueryService(nil)
	_, _, err := service.CountCaseConfirmation(context.Background(), nil)
	if err == nil {
		t.Fatal("expected unavailable error without a reader")
	}
	if platform.AsError(err).Status != 503 {
		t.Fatalf("status = %d, want 503", platform.AsError(err).Status)
	}
}
