package versionapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// fakeP6QueryReader is the version-context P6 read source used by the query
// port tests. Every call is recorded so the tests can assert trimming, limit
// normalization and batching.
type fakeP6QueryReader struct {
	closureNum   int
	closureDenom int
	closureErr   error
	closureCalls int
	closureScope *int64

	versionRows      []versionapp.VersionSearchRow
	versionErr       error
	versionQ         string
	versionLimit     int
	versionCalls     int
	requirementRows  []versionapp.RequirementSearchRow
	requirementErr   error
	requirementQ     string
	requirementLimit int
	requirementCalls int

	titleIDs     []int64
	titleResult  map[int64]string
	titleErr     error
	titleCalls   int
	pendingRows  []versionapp.PendingDesignVersionRow
	pendingErr   error
	pendingCalls int
	draftRows    []versionapp.DraftRequirementRow
	draftErr     error
	draftLimit   int
	draftCalls   int
}

func (f *fakeP6QueryReader) CountRequirementClosure(_ context.Context, versionID *int64) (int, int, error) {
	f.closureCalls++
	f.closureScope = versionID
	return f.closureNum, f.closureDenom, f.closureErr
}

func (f *fakeP6QueryReader) SearchVersions(_ context.Context, q string, limit int) ([]versionapp.VersionSearchRow, error) {
	f.versionCalls++
	f.versionQ, f.versionLimit = q, limit
	return f.versionRows, f.versionErr
}

func (f *fakeP6QueryReader) SearchRequirements(_ context.Context, q string, limit int) ([]versionapp.RequirementSearchRow, error) {
	f.requirementCalls++
	f.requirementQ, f.requirementLimit = q, limit
	return f.requirementRows, f.requirementErr
}

func (f *fakeP6QueryReader) VersionTitles(_ context.Context, ids []int64) (map[int64]string, error) {
	f.titleCalls++
	f.titleIDs = append([]int64(nil), ids...)
	return f.titleResult, f.titleErr
}

func (f *fakeP6QueryReader) ListPendingDesignVersions(context.Context) ([]versionapp.PendingDesignVersionRow, error) {
	f.pendingCalls++
	return f.pendingRows, f.pendingErr
}

func (f *fakeP6QueryReader) ListDraftRequirements(_ context.Context, limit int) ([]versionapp.DraftRequirementRow, error) {
	f.draftCalls++
	f.draftLimit = limit
	return f.draftRows, f.draftErr
}

func TestP6CountRequirementClosure(t *testing.T) {
	t.Run("empty store returns zero counts", func(t *testing.T) {
		reader := &fakeP6QueryReader{}
		service := versionapp.NewQueryService(reader)

		num, denom, err := service.CountRequirementClosure(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountRequirementClosure: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
		if reader.closureCalls != 1 || reader.closureScope != nil {
			t.Fatalf("reader calls = %d scope = %v, want one global call", reader.closureCalls, reader.closureScope)
		}
	})

	t.Run("version filter is forwarded", func(t *testing.T) {
		reader := &fakeP6QueryReader{closureNum: 3, closureDenom: 5}
		service := versionapp.NewQueryService(reader)
		versionID := int64(7)

		num, denom, err := service.CountRequirementClosure(context.Background(), &versionID)
		if err != nil {
			t.Fatalf("CountRequirementClosure: %v", err)
		}
		if num != 3 || denom != 5 {
			t.Fatalf("counts = %d/%d, want 3/5", num, denom)
		}
		if reader.closureScope == nil || *reader.closureScope != 7 {
			t.Fatalf("scope = %v, want 7", reader.closureScope)
		}
	})

	t.Run("zero denominator is reported without error", func(t *testing.T) {
		service := versionapp.NewQueryService(&fakeP6QueryReader{closureNum: 0, closureDenom: 0})

		num, denom, err := service.CountRequirementClosure(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountRequirementClosure: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
	})

	t.Run("non-positive version is rejected", func(t *testing.T) {
		reader := &fakeP6QueryReader{}
		service := versionapp.NewQueryService(reader)
		versionID := int64(0)

		if _, _, err := service.CountRequirementClosure(context.Background(), &versionID); err == nil {
			t.Fatal("expected invalid_argument for version_id=0")
		}
		if reader.closureCalls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.closureCalls)
		}
	})

	t.Run("reader error is propagated", func(t *testing.T) {
		want := errors.New("boom")
		service := versionapp.NewQueryService(&fakeP6QueryReader{closureErr: want})

		if _, _, err := service.CountRequirementClosure(context.Background(), nil); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})
}

func TestP6SearchVersions(t *testing.T) {
	reader := &fakeP6QueryReader{versionRows: []versionapp.VersionSearchRow{
		{ID: 7, Title: "Login SUT", Status: "running"},
		{ID: 3, Title: "Checkout SUT", Status: "completed"},
	}}
	service := versionapp.NewQueryService(reader)

	hits, err := service.SearchVersions(context.Background(), "  login  ", 99)
	if err != nil {
		t.Fatalf("SearchVersions: %v", err)
	}
	if reader.versionQ != "login" {
		t.Fatalf("q = %q, want trimmed login", reader.versionQ)
	}
	if reader.versionLimit != 20 {
		t.Fatalf("limit = %d, want clamp 20", reader.versionLimit)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	hit := hits[0]
	if hit.ID != 7 || hit.Title != "Login SUT" || hit.Status != "running" {
		t.Fatalf("hit = %#v", hit)
	}
	if hit.VersionID != 7 || hit.VersionTitle != "Login SUT" {
		t.Fatalf("version fields = %#v", hit)
	}
	if hit.URL != "/versions/7?tab=overview" {
		t.Fatalf("url = %q", hit.URL)
	}

	reader.versionRows = nil
	if _, err := service.SearchVersions(context.Background(), "x", 0); err != nil {
		t.Fatalf("SearchVersions empty: %v", err)
	}
	if reader.versionLimit != 5 {
		t.Fatalf("default limit = %d, want 5", reader.versionLimit)
	}
}

func TestP6SearchRequirements(t *testing.T) {
	reader := &fakeP6QueryReader{requirementRows: []versionapp.RequirementSearchRow{
		{ID: 9, VersionID: 7, VersionTitle: "Login SUT", Title: "Login with email", Status: "clarified"},
	}}
	service := versionapp.NewQueryService(reader)

	hits, err := service.SearchRequirements(context.Background(), "login", 0)
	if err != nil {
		t.Fatalf("SearchRequirements: %v", err)
	}
	if reader.requirementLimit != 5 {
		t.Fatalf("limit = %d, want default 5", reader.requirementLimit)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	hit := hits[0]
	if hit.ID != 9 || hit.Title != "Login with email" || hit.Status != "clarified" {
		t.Fatalf("hit = %#v", hit)
	}
	if hit.VersionID != 7 || hit.VersionTitle != "Login SUT" {
		t.Fatalf("version fields = %#v", hit)
	}
	wantURL := "/versions/7?tab=requirements&requirement=9"
	if hit.URL != wantURL {
		t.Fatalf("url = %q, want %q", hit.URL, wantURL)
	}
}

func TestP6VersionTitlesBatchesAndDeduplicates(t *testing.T) {
	reader := &fakeP6QueryReader{titleResult: map[int64]string{3: "三", 4: "四"}}
	service := versionapp.NewQueryService(reader)

	titles, err := service.VersionTitles(context.Background(), []int64{3, 3, 0, -1, 4})
	if err != nil {
		t.Fatalf("VersionTitles: %v", err)
	}
	if reader.titleCalls != 1 {
		t.Fatalf("reader calls = %d, want 1 (batch)", reader.titleCalls)
	}
	if len(reader.titleIDs) != 2 || reader.titleIDs[0] != 3 || reader.titleIDs[1] != 4 {
		t.Fatalf("ids = %v, want [3 4]", reader.titleIDs)
	}
	if titles[3] != "三" || titles[4] != "四" {
		t.Fatalf("titles = %#v", titles)
	}

	readsBefore := reader.titleCalls
	titles, err = service.VersionTitles(context.Background(), nil)
	if err != nil {
		t.Fatalf("VersionTitles nil: %v", err)
	}
	if len(titles) != 0 {
		t.Fatalf("titles = %#v, want empty", titles)
	}
	if reader.titleCalls != readsBefore {
		t.Fatalf("reader calls = %d, want no call for empty input", reader.titleCalls)
	}
}

func TestP6ListPendingDesignVersionsBuildsTodos(t *testing.T) {
	reader := &fakeP6QueryReader{pendingRows: []versionapp.PendingDesignVersionRow{
		{VersionID: 7, VersionTitle: "Login SUT", PendingCount: 2},
	}}
	service := versionapp.NewQueryService(reader)

	items, err := service.ListPendingDesignVersions(context.Background())
	if err != nil {
		t.Fatalf("ListPendingDesignVersions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Kind != "design_pending" {
		t.Fatalf("kind = %q", item.Kind)
	}
	if item.Title != "2 条待设计" {
		t.Fatalf("title = %q, want \"2 条待设计\"", item.Title)
	}
	if item.Severity != "warning" {
		t.Fatalf("severity = %q, want warning", item.Severity)
	}
	if item.VersionID != 7 || item.VersionTitle != "Login SUT" {
		t.Fatalf("version fields = %#v", item)
	}
	if item.URL != "/versions/7?tab=design" {
		t.Fatalf("url = %q", item.URL)
	}

	reader.pendingRows = nil
	items, err = service.ListPendingDesignVersions(context.Background())
	if err != nil {
		t.Fatalf("ListPendingDesignVersions empty: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, want empty non-nil slice", items)
	}
}

func TestP6ListDraftRequirementsBuildsTodos(t *testing.T) {
	created := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	reader := &fakeP6QueryReader{draftRows: []versionapp.DraftRequirementRow{
		{ID: 9, VersionID: 7, VersionTitle: "Login SUT", Title: "Login with email", CreatedAt: created},
	}}
	service := versionapp.NewQueryService(reader)

	items, err := service.ListDraftRequirements(context.Background(), 999)
	if err != nil {
		t.Fatalf("ListDraftRequirements: %v", err)
	}
	if reader.draftLimit != 200 {
		t.Fatalf("limit = %d, want clamp 200", reader.draftLimit)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Kind != "requirement_confirmation" || item.Severity != "warning" {
		t.Fatalf("todo = %#v", item)
	}
	if item.Title != "Login with email" || item.VersionID != 7 || item.VersionTitle != "Login SUT" {
		t.Fatalf("todo = %#v", item)
	}
	if !item.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v, want %v", item.CreatedAt, created)
	}
	wantURL := "/versions/7?tab=requirements&requirement=9"
	if item.URL != wantURL {
		t.Fatalf("url = %q, want %q", item.URL, wantURL)
	}

	reader.draftRows = nil
	items, err = service.ListDraftRequirements(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDraftRequirements empty: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, want empty non-nil slice", items)
	}
	if reader.draftLimit != 50 {
		t.Fatalf("default limit = %d, want 50", reader.draftLimit)
	}
}

func TestP6QueryServiceRequiresReader(t *testing.T) {
	service := versionapp.NewQueryService(nil)
	_, _, err := service.CountRequirementClosure(context.Background(), nil)
	if err == nil {
		t.Fatal("expected unavailable error without a reader")
	}
	if platform.AsError(err).Status != 503 {
		t.Fatalf("status = %d, want 503", platform.AsError(err).Status)
	}
}
