package workspaceproviders_test

import (
	"context"
	"errors"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
	workspaceproviders "github.com/EdouardRichard/EzTest/backend/internal/workspace/infrastructure/providers"
)

// --- version fake reader -------------------------------------------------------

type versionReader struct {
	draftRows  []versionapp.DraftRequirementRow
	draftErr   error
	draftLimit int
	draftCalls int

	pendingRows  []versionapp.PendingDesignVersionRow
	pendingErr   error
	pendingCalls int
}

func (f *versionReader) CountRequirementClosure(context.Context, *int64) (int, int, error) {
	return 0, 0, nil
}

func (f *versionReader) SearchVersions(context.Context, string, int) ([]versionapp.VersionSearchRow, error) {
	return nil, nil
}

func (f *versionReader) SearchRequirements(context.Context, string, int) ([]versionapp.RequirementSearchRow, error) {
	return nil, nil
}

func (f *versionReader) VersionTitles(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{}, nil
}

func (f *versionReader) ListPendingDesignVersions(context.Context) ([]versionapp.PendingDesignVersionRow, error) {
	f.pendingCalls++
	return f.pendingRows, f.pendingErr
}

func (f *versionReader) ListDraftRequirements(_ context.Context, limit int) ([]versionapp.DraftRequirementRow, error) {
	f.draftCalls++
	f.draftLimit = limit
	return f.draftRows, f.draftErr
}

// --- design fake reader --------------------------------------------------------

type designReader struct {
	draftRows  []designapp.DraftCaseRow
	draftErr   error
	draftLimit int
	draftCalls int
}

func (f *designReader) CountCaseConfirmation(context.Context, *int64) (int, int, error) {
	return 0, 0, nil
}

func (f *designReader) SearchCases(context.Context, string, int) ([]designapp.CaseSearchRow, error) {
	return nil, nil
}

func (f *designReader) ListDraftCases(_ context.Context, limit int) ([]designapp.DraftCaseRow, error) {
	f.draftCalls++
	f.draftLimit = limit
	return f.draftRows, f.draftErr
}

func (f *designReader) PendingDesignCounts(context.Context, []int64) (map[int64]int, error) {
	return map[int64]int{}, nil
}

// --- execution fake reader -----------------------------------------------------

type executionReader struct {
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

func (f *executionReader) CountExecutionPass(context.Context, *int64) (int, int, error) {
	return 0, 0, nil
}

func (f *executionReader) CountEvidenceCompleteness(context.Context, *int64) (int, int, error) {
	return 0, 0, nil
}

func (f *executionReader) ListOpenHumanRequests(_ context.Context, includeTakeover bool, limit int) ([]executionapp.HumanRequestRow, error) {
	f.humanCalls++
	f.humanIncludeTakeov, f.humanLimit = includeTakeover, limit
	return f.humanRows, f.humanErr
}

func (f *executionReader) ListDeadWorkItems(_ context.Context, limit int) ([]executionapp.DeadWorkItemRow, error) {
	f.deadCalls++
	f.deadLimit = limit
	return f.deadRows, f.deadErr
}

// --- delivery fake reader ------------------------------------------------------

type deliveryReader struct {
	openRows  []deliveryapp.OpenIssueRow
	openErr   error
	openLimit int
	openCalls int
}

func (f *deliveryReader) CountIssueValidity(context.Context, *int64) (int, int, error) {
	return 0, 0, nil
}

func (f *deliveryReader) SearchIssues(context.Context, string, int) ([]deliveryapp.IssueSearchRow, error) {
	return nil, nil
}

func (f *deliveryReader) SearchReports(context.Context, string, int) ([]deliveryapp.ReportSearchRow, error) {
	return nil, nil
}

func (f *deliveryReader) ListOpenIssues(_ context.Context, limit int) ([]deliveryapp.OpenIssueRow, error) {
	f.openCalls++
	f.openLimit = limit
	return f.openRows, f.openErr
}

// --- tests ---------------------------------------------------------------------

func TestVersionProviderAggregatesDraftRequirementsAndPendingDesign(t *testing.T) {
	createdAt := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	reader := &versionReader{
		draftRows: []versionapp.DraftRequirementRow{
			{ID: 4001, VersionID: 2001, VersionTitle: "订单中心", Title: "需求 A", CreatedAt: createdAt},
		},
		pendingRows: []versionapp.PendingDesignVersionRow{
			{VersionID: 2001, VersionTitle: "订单中心", PendingCount: 2},
		},
	}
	provider := workspaceproviders.NewVersionProvider(versionapp.NewQueryService(reader))

	items, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if reader.draftLimit != workspaceapp.MaxLimit {
		t.Fatalf("draft limit = %d, want %d", reader.draftLimit, workspaceapp.MaxLimit)
	}
	if items[0].Kind != workspaceapp.KindRequirementConfirmation || items[0].Title != "需求 A" {
		t.Fatalf("requirement item = %+v", items[0])
	}
	if items[0].VersionID != 2001 || items[0].VersionTitle != "订单中心" ||
		items[0].URL != "/versions/2001?tab=requirements&requirement=4001" ||
		!items[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("requirement item = %+v, want version context, deep link and createdAt", items[0])
	}
	if items[1].Kind != workspaceapp.KindDesignPending || items[1].Title != "2 条待设计" ||
		items[1].URL != "/versions/2001?tab=design" {
		t.Fatalf("pending design item = %+v", items[1])
	}
}

func TestVersionProviderPropagatesErrors(t *testing.T) {
	sentinel := errors.New("version reader failed")
	provider := workspaceproviders.NewVersionProvider(
		versionapp.NewQueryService(&versionReader{draftErr: sentinel}))

	_, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Items error = %v, want %v", err, sentinel)
	}
}

func TestDesignProviderProjectsDraftCases(t *testing.T) {
	createdAt := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	reader := &designReader{draftRows: []designapp.DraftCaseRow{
		{CaseID: 4101, VersionID: 2001, VersionTitle: "订单中心", Title: "用例 A", CreatedAt: createdAt},
	}}
	provider := workspaceproviders.NewDesignProvider(designapp.NewQueryService(reader))

	items, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if reader.draftLimit != workspaceapp.MaxLimit {
		t.Fatalf("limit = %d, want %d", reader.draftLimit, workspaceapp.MaxLimit)
	}
	if items[0].Kind != workspaceapp.KindCaseConfirmation || items[0].Title != "用例 A" ||
		items[0].URL != "/versions/2001?tab=design&case=4101" ||
		!items[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("case item = %+v", items[0])
	}
}

func TestDesignProviderPropagatesErrors(t *testing.T) {
	sentinel := errors.New("design reader failed")
	provider := workspaceproviders.NewDesignProvider(
		designapp.NewQueryService(&designReader{draftErr: sentinel}))

	_, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Items error = %v, want %v", err, sentinel)
	}
}

func TestDeliveryProviderProjectsOpenIssues(t *testing.T) {
	createdAt := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	reader := &deliveryReader{openRows: []deliveryapp.OpenIssueRow{
		{ID: 4201, VersionID: 2001, VersionTitle: "订单中心", Title: "问题 A", Severity: "trivial", CreatedAt: createdAt},
	}}
	provider := workspaceproviders.NewDeliveryProvider(deliveryapp.NewQueryService(reader))

	items, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if reader.openLimit != workspaceapp.MaxLimit {
		t.Fatalf("limit = %d, want %d", reader.openLimit, workspaceapp.MaxLimit)
	}
	if items[0].Kind != workspaceapp.KindIssueConfirmation || items[0].Severity != workspaceapp.SeverityInfo ||
		items[0].URL != "/versions/2001?tab=issues&issue=4201" {
		t.Fatalf("issue item = %+v", items[0])
	}
}

func TestDeliveryProviderPropagatesErrors(t *testing.T) {
	sentinel := errors.New("delivery reader failed")
	provider := workspaceproviders.NewDeliveryProvider(
		deliveryapp.NewQueryService(&deliveryReader{openErr: sentinel}))

	_, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Items error = %v, want %v", err, sentinel)
	}
}

func TestExecutionProviderAdminIncludesTakeoverAndDeadWorkItems(t *testing.T) {
	expiresAt := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	reader := &executionReader{
		humanRows: []executionapp.HumanRequestRow{
			{ID: 4301, Kind: "login_takeover", VersionID: 2001, VersionTitle: "订单中心", Title: "接管请求", ExpiresAt: &expiresAt},
			{ID: 4302, Kind: "conflict", VersionID: 2001, VersionTitle: "订单中心", Title: "冲突请求", ExpiresAt: &expiresAt},
		},
		deadRows: []executionapp.DeadWorkItemRow{
			{ID: 4401, VersionID: 2001, VersionTitle: "订单中心", Title: "死任务"},
		},
	}
	provider := workspaceproviders.NewExecutionProvider(executionapp.NewQueryService(reader))

	items, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if !reader.humanIncludeTakeov {
		t.Fatal("admin provider must ask the execution reader for takeover requests")
	}
	if reader.humanLimit != workspaceapp.MaxLimit || reader.deadLimit != workspaceapp.MaxLimit {
		t.Fatalf("limits = %d/%d, want %d", reader.humanLimit, reader.deadLimit, workspaceapp.MaxLimit)
	}
	if reader.deadCalls != 1 {
		t.Fatalf("dead calls = %d, want 1 for an admin", reader.deadCalls)
	}
	if items[0].Kind != workspaceapp.KindHumanRequest || items[0].ExpiresAt == nil {
		t.Fatalf("human item = %+v, want kind and expiry", items[0])
	}
	if items[2].Kind != workspaceapp.KindDeadWorkItem || items[2].Severity != workspaceapp.SeverityMajor {
		t.Fatalf("dead item = %+v", items[2])
	}
}

func TestExecutionProviderRegularUserNeverQueriesDeadWorkItems(t *testing.T) {
	reader := &executionReader{
		humanRows: []executionapp.HumanRequestRow{
			{ID: 4301, Kind: "login_takeover", VersionID: 2001, VersionTitle: "订单中心", Title: "接管请求"},
			{ID: 4302, Kind: "conflict", VersionID: 2001, VersionTitle: "订单中心", Title: "冲突请求"},
		},
		deadRows: []executionapp.DeadWorkItemRow{
			{ID: 4401, VersionID: 2001, VersionTitle: "订单中心", Title: "死任务"},
		},
	}
	provider := workspaceproviders.NewExecutionProvider(executionapp.NewQueryService(reader))

	items, err := provider.Items(context.Background(), platform.Actor{ID: 9001, Role: "user"})
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if reader.humanIncludeTakeov {
		t.Fatal("regular user provider must not ask for takeover requests")
	}
	if reader.deadCalls != 0 {
		t.Fatalf("dead calls = %d, want 0 for a regular user", reader.deadCalls)
	}
	if len(items) != 1 || items[0].Title != "冲突请求" || items[0].Kind != workspaceapp.KindHumanRequest {
		t.Fatalf("items = %+v, want only the conflict human request", items)
	}
}

func TestExecutionProviderPropagatesHumanRequestErrors(t *testing.T) {
	sentinel := errors.New("execution reader failed")
	provider := workspaceproviders.NewExecutionProvider(
		executionapp.NewQueryService(&executionReader{humanErr: sentinel}))

	_, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Items error = %v, want %v", err, sentinel)
	}
}

func TestExecutionProviderPropagatesDeadWorkItemErrors(t *testing.T) {
	sentinel := errors.New("dead work item reader failed")
	provider := workspaceproviders.NewExecutionProvider(
		executionapp.NewQueryService(&executionReader{deadErr: sentinel}))

	_, err := provider.Items(context.Background(), platform.Actor{ID: 1, Role: "admin"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Items error = %v, want %v", err, sentinel)
	}
}
