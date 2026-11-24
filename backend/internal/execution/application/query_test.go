package executionapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func execInt64Ptr(value int64) *int64 { return &value }

// fakeExecutionQueryReader is the execution-context P6 read source used by the
// query port tests.
type fakeExecutionQueryReader struct {
	passNum   int
	passDenom int
	passErr   error
	passCalls int
	passScope *int64

	evidenceNum   int
	evidenceDenom int
	evidenceErr   error
	evidenceCalls int
	evidenceScope *int64

	humanRows          []executionapp.HumanRequestRow
	humanErr           error
	humanIncludeTakeov bool
	humanLimit         int
	humanCalls         int

	deadRows  []executionapp.DeadWorkItemRow
	deadErr   error
	deadLimit int
	deadCalls int
}

func (f *fakeExecutionQueryReader) CountExecutionPass(_ context.Context, versionID *int64) (int, int, error) {
	f.passCalls++
	f.passScope = versionID
	return f.passNum, f.passDenom, f.passErr
}

func (f *fakeExecutionQueryReader) CountEvidenceCompleteness(_ context.Context, versionID *int64) (int, int, error) {
	f.evidenceCalls++
	f.evidenceScope = versionID
	return f.evidenceNum, f.evidenceDenom, f.evidenceErr
}

func (f *fakeExecutionQueryReader) ListOpenHumanRequests(_ context.Context, includeTakeover bool, limit int) ([]executionapp.HumanRequestRow, error) {
	f.humanCalls++
	f.humanIncludeTakeov, f.humanLimit = includeTakeover, limit
	return f.humanRows, f.humanErr
}

func (f *fakeExecutionQueryReader) ListDeadWorkItems(_ context.Context, limit int) ([]executionapp.DeadWorkItemRow, error) {
	f.deadCalls++
	f.deadLimit = limit
	return f.deadRows, f.deadErr
}

func TestCountExecutionPass(t *testing.T) {
	t.Run("empty store returns zero counts", func(t *testing.T) {
		reader := &fakeExecutionQueryReader{}
		service := executionapp.NewQueryService(reader)

		num, denom, err := service.CountExecutionPass(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountExecutionPass: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
		if reader.passCalls != 1 || reader.passScope != nil {
			t.Fatalf("reader calls = %d scope = %v, want one global call", reader.passCalls, reader.passScope)
		}
	})

	t.Run("version filter is forwarded", func(t *testing.T) {
		reader := &fakeExecutionQueryReader{passNum: 8, passDenom: 10}
		service := executionapp.NewQueryService(reader)

		num, denom, err := service.CountExecutionPass(context.Background(), execInt64Ptr(7))
		if err != nil {
			t.Fatalf("CountExecutionPass: %v", err)
		}
		if num != 8 || denom != 10 {
			t.Fatalf("counts = %d/%d, want 8/10", num, denom)
		}
		if reader.passScope == nil || *reader.passScope != 7 {
			t.Fatalf("scope = %v, want 7", reader.passScope)
		}
	})

	t.Run("zero denominator is reported without error", func(t *testing.T) {
		service := executionapp.NewQueryService(&fakeExecutionQueryReader{})

		num, denom, err := service.CountExecutionPass(context.Background(), nil)
		if err != nil {
			t.Fatalf("CountExecutionPass: %v", err)
		}
		if num != 0 || denom != 0 {
			t.Fatalf("counts = %d/%d, want 0/0", num, denom)
		}
	})

	t.Run("non-positive version is rejected", func(t *testing.T) {
		reader := &fakeExecutionQueryReader{}
		service := executionapp.NewQueryService(reader)

		if _, _, err := service.CountExecutionPass(context.Background(), execInt64Ptr(0)); err == nil {
			t.Fatal("expected invalid_argument for version_id=0")
		}
		if reader.passCalls != 0 {
			t.Fatalf("reader calls = %d, want 0", reader.passCalls)
		}
	})

	t.Run("reader error is propagated", func(t *testing.T) {
		want := errors.New("boom")
		service := executionapp.NewQueryService(&fakeExecutionQueryReader{passErr: want})

		if _, _, err := service.CountExecutionPass(context.Background(), nil); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})
}

func TestCountEvidenceCompleteness(t *testing.T) {
	reader := &fakeExecutionQueryReader{evidenceNum: 6, evidenceDenom: 8}
	service := executionapp.NewQueryService(reader)

	num, denom, err := service.CountEvidenceCompleteness(context.Background(), execInt64Ptr(7))
	if err != nil {
		t.Fatalf("CountEvidenceCompleteness: %v", err)
	}
	if num != 6 || denom != 8 {
		t.Fatalf("counts = %d/%d, want 6/8", num, denom)
	}
	if reader.evidenceScope == nil || *reader.evidenceScope != 7 {
		t.Fatalf("scope = %v, want 7", reader.evidenceScope)
	}

	empty := executionapp.NewQueryService(&fakeExecutionQueryReader{})
	if num, denom, err := empty.CountEvidenceCompleteness(context.Background(), nil); err != nil || num != 0 || denom != 0 {
		t.Fatalf("empty counts = %d/%d err=%v, want 0/0 nil", num, denom, err)
	}
}

func TestOpenHumanRequests(t *testing.T) {
	created := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	expires := created.Add(10 * time.Minute)
	rows := []executionapp.HumanRequestRow{
		{ID: 1, Kind: "login_takeover", VersionID: 7, VersionTitle: "Login SUT", Title: "验证码超时", CreatedAt: created, ExpiresAt: &expires},
		{ID: 2, Kind: "conflict", VersionID: 7, VersionTitle: "Login SUT", Title: "测试账号冲突", CreatedAt: created},
	}

	t.Run("admin receives takeover and conflict", func(t *testing.T) {
		reader := &fakeExecutionQueryReader{humanRows: rows}
		service := executionapp.NewQueryService(reader)

		items, err := service.OpenHumanRequests(context.Background(), platform.Actor{ID: 1, Role: "admin"}, 999)
		if err != nil {
			t.Fatalf("OpenHumanRequests: %v", err)
		}
		if !reader.humanIncludeTakeov {
			t.Fatal("admin read must include login_takeover")
		}
		if reader.humanLimit != 200 {
			t.Fatalf("limit = %d, want clamp 200", reader.humanLimit)
		}
		if len(items) != 2 {
			t.Fatalf("items = %d, want 2", len(items))
		}
		first := items[0]
		if first.Kind != "human_request" || first.Severity != "warning" {
			t.Fatalf("todo = %#v", first)
		}
		if first.Title != "验证码超时" || first.VersionID != 7 || first.VersionTitle != "Login SUT" {
			t.Fatalf("todo = %#v", first)
		}
		if !first.CreatedAt.Equal(created) || first.ExpiresAt == nil || !first.ExpiresAt.Equal(expires) {
			t.Fatalf("times = %v / %v", first.CreatedAt, first.ExpiresAt)
		}
		if first.URL != "/versions/7?tab=execution" {
			t.Fatalf("url = %q", first.URL)
		}
	})

	t.Run("non-admin never receives login takeover", func(t *testing.T) {
		reader := &fakeExecutionQueryReader{humanRows: rows}
		service := executionapp.NewQueryService(reader)

		items, err := service.OpenHumanRequests(context.Background(), platform.Actor{ID: 2, Role: "member"}, 0)
		if err != nil {
			t.Fatalf("OpenHumanRequests: %v", err)
		}
		if reader.humanIncludeTakeov {
			t.Fatal("non-admin read must not ask for login_takeover")
		}
		if reader.humanLimit != 50 {
			t.Fatalf("limit = %d, want default 50", reader.humanLimit)
		}
		if len(items) != 1 || items[0].Title != "测试账号冲突" {
			t.Fatalf("items = %#v, want only the conflict request", items)
		}
	})

	t.Run("empty store returns empty non-nil slice", func(t *testing.T) {
		service := executionapp.NewQueryService(&fakeExecutionQueryReader{})

		items, err := service.OpenHumanRequests(context.Background(), platform.Actor{Role: "admin"}, 10)
		if err != nil {
			t.Fatalf("OpenHumanRequests: %v", err)
		}
		if items == nil || len(items) != 0 {
			t.Fatalf("items = %#v, want empty non-nil slice", items)
		}
	})
}

func TestDeadWorkItems(t *testing.T) {
	created := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	reader := &fakeExecutionQueryReader{deadRows: []executionapp.DeadWorkItemRow{
		{ID: 11, VersionID: 7, VersionTitle: "Login SUT", Title: "execute.case 重试耗尽", CreatedAt: created},
	}}
	service := executionapp.NewQueryService(reader)

	items, err := service.DeadWorkItems(context.Background(), 999)
	if err != nil {
		t.Fatalf("DeadWorkItems: %v", err)
	}
	if reader.deadLimit != 200 {
		t.Fatalf("limit = %d, want clamp 200", reader.deadLimit)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Kind != "dead_work_item" || item.Severity != "major" {
		t.Fatalf("todo = %#v", item)
	}
	if item.Title != "execute.case 重试耗尽" || item.VersionID != 7 || item.VersionTitle != "Login SUT" {
		t.Fatalf("todo = %#v", item)
	}
	if !item.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v, want %v", item.CreatedAt, created)
	}
	if item.URL != "/versions/7?tab=execution" {
		t.Fatalf("url = %q", item.URL)
	}

	reader.deadRows = nil
	items, err = service.DeadWorkItems(context.Background(), 0)
	if err != nil {
		t.Fatalf("DeadWorkItems empty: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("items = %#v, want empty non-nil slice", items)
	}
	if reader.deadLimit != 50 {
		t.Fatalf("default limit = %d, want 50", reader.deadLimit)
	}
}

func TestExecutionQueryServiceRequiresReader(t *testing.T) {
	service := executionapp.NewQueryService(nil)
	_, _, err := service.CountExecutionPass(context.Background(), nil)
	if err == nil {
		t.Fatal("expected unavailable error without a reader")
	}
	if platform.AsError(err).Status != 503 {
		t.Fatalf("status = %d, want 503", platform.AsError(err).Status)
	}
}
