package designadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/designadapter"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

var adapterTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fakeGraphGateway struct {
	readVersionID int64
	readResult    designapp.GraphView
	readErr       error

	getVersionID     int64
	getCaseID        int64
	getCaseVersionID *int64
	caseResult       designapp.CaseDetailView
	caseErr          error

	proposeVersionID int64
	proposeInput     designapp.ProposeInput
	proposeActorID   int64
	proposeRunID     int64
	proposeResult    designapp.ProposeResult
	proposeErr       error

	reviewVersionID     int64
	reviewRequirementID int64
	reviewRunID         int64
	reviewInput         designapp.ReviewInput
	reviewResult        designapp.ReviewResult
	reviewErr           error

	dataPlanVersionID     int64
	dataPlanCaseVersionID int64
	dataPlanRunID         int64
	dataPlanInput         designapp.DataPlanInput
	dataPlanResult        designapp.DataPlanResult
	dataPlanErr           error
}

func (f *fakeGraphGateway) ReadGraph(_ context.Context, versionID int64) (designapp.GraphView, error) {
	f.readVersionID = versionID
	return f.readResult, f.readErr
}

func (f *fakeGraphGateway) GetCase(_ context.Context, versionID, caseID int64, caseVersionID *int64) (designapp.CaseDetailView, error) {
	f.getVersionID = versionID
	f.getCaseID = caseID
	f.getCaseVersionID = caseVersionID
	return f.caseResult, f.caseErr
}

func (f *fakeGraphGateway) Propose(_ context.Context, versionID int64, in designapp.ProposeInput, actorID, runID int64) (designapp.ProposeResult, error) {
	f.proposeVersionID = versionID
	f.proposeInput = in
	f.proposeActorID = actorID
	f.proposeRunID = runID
	return f.proposeResult, f.proposeErr
}

func (f *fakeGraphGateway) SubmitReview(_ context.Context, versionID, requirementID, runID int64, in designapp.ReviewInput) (designapp.ReviewResult, error) {
	f.reviewVersionID = versionID
	f.reviewRequirementID = requirementID
	f.reviewRunID = runID
	f.reviewInput = in
	return f.reviewResult, f.reviewErr
}

func (f *fakeGraphGateway) SubmitDataPlan(_ context.Context, versionID, caseVersionID, runID int64, in designapp.DataPlanInput) (designapp.DataPlanResult, error) {
	f.dataPlanVersionID = versionID
	f.dataPlanCaseVersionID = caseVersionID
	f.dataPlanRunID = runID
	f.dataPlanInput = in
	return f.dataPlanResult, f.dataPlanErr
}

var _ designadapter.GraphGateway = (*fakeGraphGateway)(nil)
var _ tools.DesignPort = (*designadapter.Adapter)(nil)

func sampleGraphView() designapp.GraphView {
	defaultSUT := int64(9001)
	return designapp.GraphView{
		VersionID:                 1001,
		GeneratedAt:               adapterTime,
		GraphHash:                 strings.Repeat("a", 64),
		PendingDesignCount:        2,
		AutoDesignNewRequirements: true,
		Stats: designapp.GraphStatsView{
			Requirements: 2, Scenarios: 3, Cases: 4, Draft: 2, Confirmed: 1, Stale: 1,
		},
		Requirements: []designapp.RequirementGraphView{
			{
				RequirementID:        3001,
				RequirementVersionID: 3002,
				Revision:             3,
				Title:                "登录",
				Status:               "clarified",
				DesignStatus:         "draft",
				TargetVersionSUTIDs:  []int64{9001},
				Scenarios: []designapp.ScenarioGraphView{
					{
						ID:                   5001,
						RequirementVersionID: 3002,
						Name:                 "正常登录",
						Description:          "手机号密码登录",
						VersionSUTIDs:        []int64{9001},
						PageIDs:              []int64{9101},
						APIIDs:               []int64{9201},
						SortOrder:            0,
						Cases: []designapp.CaseCardView{
							{
								CaseID:              7001,
								CurrentVersionID:    7002,
								Revision:            2,
								Title:               "账号密码登录成功",
								Priority:            "P1",
								Status:              "draft",
								DefaultVersionSUTID: &defaultSUT,
								VersionSUTIDs:       []int64{9001},
								PageIDs:             []int64{9101},
								APIIDs:              []int64{9201},
								StepCount:           3,
								AssertionCount:      2,
								CreatedAt:           adapterTime,
							},
						},
					},
				},
			},
			{
				RequirementID:        3101,
				RequirementVersionID: 3102,
				Revision:             1,
				Title:                "退款",
				Status:               "clarified",
				TargetVersionSUTIDs:  []int64{9001},
			},
		},
	}
}

func sampleCaseDetail() designapp.CaseDetailView {
	defaultSUT := int64(9001)
	pageID := int64(9101)
	line := 12
	return designapp.CaseDetailView{
		CaseID:               7001,
		Status:               "draft",
		CurrentVersionID:     int64Ptr(7002),
		RequirementID:        3001,
		RequirementVersionID: 3002,
		ScenarioID:           5001,
		ScenarioName:         "正常登录",
		Versions: []designapp.CaseVersionSummaryView{
			{CaseVersionID: 7002, Revision: 2, Status: "draft", Title: "账号密码登录成功", CreatedAt: adapterTime},
			{CaseVersionID: 7000, Revision: 1, Status: "confirmed", Title: "账号密码登录成功", CreatedAt: adapterTime, ConfirmedAt: &adapterTime},
		},
		Selected: designapp.CaseVersionDetailView{
			CaseVersionID:       7002,
			Revision:            2,
			Title:               "账号密码登录成功",
			Description:         "使用有效账号登录",
			Preconditions:       "已注册账号",
			TestData:            json.RawMessage(`{"phone":"13800000000"}`),
			Priority:            "P1",
			DefaultVersionSUTID: &defaultSUT,
			VersionSUTIDs:       []int64{9001},
			PageIDs:             []int64{9101},
			APIIDs:              []int64{9201},
			Status:              "draft",
			ContentHash:         strings.Repeat("b", 64),
			CreatedAt:           adapterTime,
			Steps: []designapp.CaseStepView{
				{Seq: 1, Action: "打开登录页", Expected: "显示表单", TargetVersionSUTID: &defaultSUT, TargetPageID: &pageID, AccountRole: "admin", Data: json.RawMessage(`{}`)},
			},
			Assertions: []designapp.CaseAssertionView{
				{StepSeq: 1, Kind: "ui", Target: "表单", Expected: "可见"},
			},
			Sources: []designapp.CaseSourceView{
				{Kind: "requirement", RefID: "3002", Line: &line},
			},
		},
		Confirmable: true,
	}
}

func int64Ptr(value int64) *int64 { return &value }

func TestReadGraphRendersSnakeCaseAndFiltersRequirement(t *testing.T) {
	gateway := &fakeGraphGateway{readResult: sampleGraphView()}
	adapter := designadapter.New(gateway)

	result, err := adapter.ReadGraph(context.Background(), 1001, 3001, 0)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	if gateway.readVersionID != 1001 {
		t.Fatalf("version id = %d", gateway.readVersionID)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", result)
	}
	if payload["version_id"] != "1001" || payload["graph_hash"] != strings.Repeat("a", 64) {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["pending_design_count"] != 2 || payload["auto_design_new_requirements"] != true {
		t.Fatalf("payload = %#v", payload)
	}
	stats, _ := payload["stats"].(map[string]any)
	if stats["requirements"] != 2 || stats["stale"] != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	requirements, _ := payload["requirements"].([]any)
	if len(requirements) != 1 {
		t.Fatalf("requirements = %#v", requirements)
	}
	requirement := requirements[0].(map[string]any)
	if requirement["requirement_id"] != "3001" || requirement["requirement_version_id"] != "3002" || requirement["revision"] != 3 {
		t.Fatalf("requirement = %#v", requirement)
	}
	scenarios := requirement["scenarios"].([]any)
	if len(scenarios) != 1 {
		t.Fatalf("scenarios = %#v", scenarios)
	}
	scenario := scenarios[0].(map[string]any)
	if scenario["id"] != "5001" || scenario["name"] != "正常登录" || scenario["sort"] != 0 {
		t.Fatalf("scenario = %#v", scenario)
	}
	if suts, _ := scenario["version_sut_ids"].([]string); len(suts) != 1 || suts[0] != "9001" {
		t.Fatalf("scenario suts = %#v", scenario["version_sut_ids"])
	}
	cases := scenario["cases"].([]any)
	card := cases[0].(map[string]any)
	if card["case_id"] != "7001" || card["current_version_id"] != "7002" || card["status"] != "draft" {
		t.Fatalf("case card = %#v", card)
	}
	if card["default_version_sut_id"] != "9001" || card["step_count"] != 3 || card["assertion_count"] != 2 {
		t.Fatalf("case card = %#v", card)
	}
}

func TestReadGraphWithCaseIDReturnsCaseDetail(t *testing.T) {
	gateway := &fakeGraphGateway{caseResult: sampleCaseDetail()}
	adapter := designadapter.New(gateway)

	result, err := adapter.ReadGraph(context.Background(), 1001, 0, 7001)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	if gateway.getVersionID != 1001 || gateway.getCaseID != 7001 || gateway.getCaseVersionID != nil {
		t.Fatalf("get case args = %#v", gateway)
	}
	payload := result.(map[string]any)
	if payload["case_id"] != "7001" || payload["requirement_id"] != "3001" || payload["confirmable"] != true {
		t.Fatalf("payload = %#v", payload)
	}
	versions, _ := payload["versions"].([]any)
	if len(versions) != 2 || versions[0].(map[string]any)["case_version_id"] != "7002" {
		t.Fatalf("versions = %#v", versions)
	}
	selected := payload["selected"].(map[string]any)
	if selected["case_version_id"] != "7002" || selected["status"] != "draft" || selected["content_hash"] != strings.Repeat("b", 64) {
		t.Fatalf("selected = %#v", selected)
	}
	steps := selected["steps"].([]any)
	step := steps[0].(map[string]any)
	if step["seq"] != 1 || step["action"] != "打开登录页" || step["target_page_id"] != "9101" || step["account_role"] != "admin" {
		t.Fatalf("step = %#v", step)
	}
	assertions := selected["assertions"].([]any)
	if assertions[0].(map[string]any)["kind"] != "ui" {
		t.Fatalf("assertions = %#v", assertions)
	}
	sources := selected["sources"].([]any)
	if sources[0].(map[string]any)["line"] != 12 {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestProposeMapsToolsProposalToDesignInput(t *testing.T) {
	gateway := &fakeGraphGateway{proposeResult: designapp.ProposeResult{
		ScenarioIDs: []int64{5001}, CaseIDs: []int64{7001}, CaseVersionIDs: []int64{7002}, GraphHash: strings.Repeat("c", 64),
	}}
	adapter := designadapter.New(gateway)
	updateCaseID := int64(7001)
	proposal := tools.GraphProposal{
		RequirementID:        3001,
		RequirementVersionID: 3002,
		BaseGraphHash:        strings.Repeat("a", 64),
		Scenarios: []tools.ScenarioProposal{
			{Ref: "s1", ID: 5001, Name: "正常登录", Description: "d", VersionSUTIDs: []int64{9001}, PageIDs: []int64{9101}, APIIDs: []int64{9201}, SortOrder: 2},
			{Ref: "s2", Name: "异常登录", VersionSUTIDs: []int64{9001}},
		},
		Cases: []tools.CaseProposal{
			{
				Ref: "c1", ID: updateCaseID, ScenarioRef: "s1", Title: "账号密码登录成功",
				Description: "d", Preconditions: "p", TestData: json.RawMessage(`{"a":1}`), Priority: "P1",
				DefaultVersionSUTID: 9001, VersionSUTIDs: []int64{9001}, PageIDs: []int64{9101}, APIIDs: []int64{9201},
				Steps: []tools.StepProposal{
					{Seq: 1, Action: "打开", Expected: "显示", TargetVersionSUTID: 9001, TargetPageID: 9101, AccountRole: "admin", Data: json.RawMessage(`{}`)},
					{Seq: 2, Action: "提交"},
				},
				Assertions: []tools.AssertionProposal{
					{StepSeq: 1, Kind: "ui", Target: "表单", Expected: "可见", TargetVersionSUTID: 9001, DatabaseID: 9301},
				},
				Sources: []tools.SourceProposal{
					{Kind: "requirement", RefID: "3002", Path: "p", Line: intPtr(3), SHA: "s"},
				},
			},
		},
	}

	result, err := adapter.ProposeGraph(context.Background(), 1001, proposal, 0, 77)
	if err != nil {
		t.Fatalf("ProposeGraph: %v", err)
	}
	if gateway.proposeVersionID != 1001 || gateway.proposeActorID != 0 || gateway.proposeRunID != 77 {
		t.Fatalf("propose args = %#v", gateway)
	}
	input := gateway.proposeInput
	if input.RequirementID != 3001 || input.RequirementVersionID != 3002 || input.BaseGraphHash != strings.Repeat("a", 64) {
		t.Fatalf("input = %#v", input)
	}
	if len(input.Scenarios) != 2 || input.Scenarios[0].ID == nil || *input.Scenarios[0].ID != 5001 || input.Scenarios[1].ID != nil {
		t.Fatalf("scenarios = %#v", input.Scenarios)
	}
	if input.Scenarios[0].SortOrder != 2 || len(input.Scenarios[0].PageIDs) != 1 {
		t.Fatalf("scenario = %#v", input.Scenarios[0])
	}
	if len(input.Cases) != 1 {
		t.Fatalf("cases = %#v", input.Cases)
	}
	item := input.Cases[0]
	if item.ID == nil || *item.ID != 7001 || item.DefaultVersionSUTID == nil || *item.DefaultVersionSUTID != 9001 {
		t.Fatalf("case = %#v", item)
	}
	if item.Steps[0].TargetVersionSUTID == nil || *item.Steps[0].TargetVersionSUTID != 9001 ||
		item.Steps[0].TargetPageID == nil || *item.Steps[0].TargetPageID != 9101 {
		t.Fatalf("step = %#v", item.Steps[0])
	}
	if item.Steps[1].TargetVersionSUTID != nil || item.Steps[1].TargetPageID != nil {
		t.Fatalf("nullable step targets = %#v", item.Steps[1])
	}
	if item.Assertions[0].DatabaseID == nil || *item.Assertions[0].DatabaseID != 9301 {
		t.Fatalf("assertion = %#v", item.Assertions[0])
	}
	if item.Sources[0].Line == nil || *item.Sources[0].Line != 3 {
		t.Fatalf("source = %#v", item.Sources[0])
	}

	payload := result.(map[string]any)
	if payload["graph_hash"] != strings.Repeat("c", 64) {
		t.Fatalf("payload = %#v", payload)
	}
	if ids, _ := payload["case_version_ids"].([]string); len(ids) != 1 || ids[0] != "7002" {
		t.Fatalf("case version ids = %#v", payload["case_version_ids"])
	}
}

func TestSubmitReviewMapsFindings(t *testing.T) {
	gateway := &fakeGraphGateway{reviewResult: designapp.ReviewResult{Posted: true, Count: 1}}
	adapter := designadapter.New(gateway)
	requirementID := int64(3001)
	caseID := int64(7001)

	result, err := adapter.SubmitReview(context.Background(), 1001, 3001, 77, tools.ReviewInput{
		Summary: "整体可用",
		Findings: []tools.ReviewFinding{
			{Severity: "minor", Message: "补充断言", RequirementID: requirementID, CaseID: caseID},
			{Severity: "important", Message: "缺少异常步骤"},
		},
	})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if gateway.reviewVersionID != 1001 || gateway.reviewRequirementID != 3001 || gateway.reviewRunID != 77 {
		t.Fatalf("review args = %#v", gateway)
	}
	if gateway.reviewInput.Summary != "整体可用" || len(gateway.reviewInput.Findings) != 2 {
		t.Fatalf("review input = %#v", gateway.reviewInput)
	}
	if gateway.reviewInput.Findings[0].RequirementID == nil || *gateway.reviewInput.Findings[0].RequirementID != 3001 ||
		gateway.reviewInput.Findings[0].CaseID == nil || *gateway.reviewInput.Findings[0].CaseID != 7001 {
		t.Fatalf("finding = %#v", gateway.reviewInput.Findings[0])
	}
	if gateway.reviewInput.Findings[1].RequirementID != nil || gateway.reviewInput.Findings[1].CaseID != nil {
		t.Fatalf("nullable finding ids = %#v", gateway.reviewInput.Findings[1])
	}
	payload := result.(map[string]any)
	if payload["posted"] != true || payload["count"] != 1 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSubmitDataPlanMapsInputs(t *testing.T) {
	gateway := &fakeGraphGateway{dataPlanResult: designapp.DataPlanResult{CaseID: 7001, CaseVersionID: 7005, Revision: 3}}
	adapter := designadapter.New(gateway)
	preconditions := "已登录"

	result, err := adapter.SubmitDataPlan(context.Background(), 1001, 8001, 77, tools.DataPlanInput{
		Preconditions: &preconditions,
		TestData:      json.RawMessage(`{"account":"admin"}`),
	})
	if err != nil {
		t.Fatalf("SubmitDataPlan: %v", err)
	}
	if gateway.dataPlanVersionID != 1001 || gateway.dataPlanCaseVersionID != 8001 || gateway.dataPlanRunID != 77 {
		t.Fatalf("data plan args = %#v", gateway)
	}
	if gateway.dataPlanInput.Preconditions == nil || *gateway.dataPlanInput.Preconditions != preconditions {
		t.Fatalf("input = %#v", gateway.dataPlanInput)
	}
	if string(gateway.dataPlanInput.TestData) != `{"account":"admin"}` {
		t.Fatalf("test data = %s", gateway.dataPlanInput.TestData)
	}
	payload := result.(map[string]any)
	if payload["case_id"] != "7001" || payload["case_version_id"] != "7005" || payload["revision"] != 3 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestNilGatewayFailsClosed(t *testing.T) {
	adapter := designadapter.New(nil)
	if _, err := adapter.ReadGraph(context.Background(), 1001, 0, 0); err == nil {
		t.Fatal("nil design gateway read succeeded")
	}
	if _, err := adapter.ProposeGraph(context.Background(), 1001, tools.GraphProposal{}, 0, 0); err == nil {
		t.Fatal("nil design gateway propose succeeded")
	}
	if _, err := adapter.SubmitReview(context.Background(), 1001, 3001, 0, tools.ReviewInput{}); err == nil {
		t.Fatal("nil design gateway review succeeded")
	}
	if _, err := adapter.SubmitDataPlan(context.Background(), 1001, 8001, 0, tools.DataPlanInput{}); err == nil {
		t.Fatal("nil design gateway data plan succeeded")
	}
}

func TestBusinessErrorsStayPlatformErrors(t *testing.T) {
	notFound := platform.NotFound("case")
	conflict := platform.StateConflict("only the current draft can be updated")
	cases := []struct {
		name string
		call func(*designadapter.Adapter) error
	}{
		{"read graph", func(adapter *designadapter.Adapter) error {
			_, err := adapter.ReadGraph(context.Background(), 1001, 0, 0)
			return err
		}},
		{"read case", func(adapter *designadapter.Adapter) error {
			_, err := adapter.ReadGraph(context.Background(), 1001, 0, 7001)
			return err
		}},
		{"propose", func(adapter *designadapter.Adapter) error {
			_, err := adapter.ProposeGraph(context.Background(), 1001, tools.GraphProposal{}, 0, 0)
			return err
		}},
		{"review", func(adapter *designadapter.Adapter) error {
			_, err := adapter.SubmitReview(context.Background(), 1001, 3001, 0, tools.ReviewInput{})
			return err
		}},
		{"data plan", func(adapter *designadapter.Adapter) error {
			_, err := adapter.SubmitDataPlan(context.Background(), 1001, 8001, 0, tools.DataPlanInput{})
			return err
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gateway := &fakeGraphGateway{
				readErr:     notFound,
				caseErr:     notFound,
				proposeErr:  notFound,
				reviewErr:   conflict,
				dataPlanErr: conflict,
			}
			err := testCase.call(designadapter.New(gateway))
			if err == nil {
				t.Fatal("expected a business error")
			}
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not a platform error; the tools layer could not map it", err)
			}
		})
	}
}

func intPtr(value int) *int { return &value }
