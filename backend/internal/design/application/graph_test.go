package designapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func newGraphFixture(t *testing.T) (*GraphService, *fakeRepo, *fakePublisher, *fakeAudit) {
	t.Helper()
	repo := newFakeRepo()
	publisher := &fakePublisher{}
	audit := &fakeAudit{}
	service := NewGraphService(repo, &fakeIDs{}, newFixedClock(), audit, publisher, &fakeTeamRoom{})
	return service, repo, publisher, audit
}

func validProposeInput() ProposeInput {
	return ProposeInput{
		RequirementID:        3001,
		RequirementVersionID: 3002,
		Scenarios: []ScenarioInput{{
			Ref: "s1", Name: "正常登录", Description: "正常路径",
			VersionSUTIDs: []int64{9001, 9002}, PageIDs: []int64{6001}, APIIDs: []int64{4001},
		}},
		Cases: []CaseInput{{
			Ref: "c1", ScenarioRef: "s1", Title: "账号密码登录成功", Description: "使用有效账号登录",
			Preconditions: "已有账号", TestData: json.RawMessage(`{"username":"admin"}`),
			Priority: "P1", DefaultVersionSUTID: int64Ptr(9001),
			VersionSUTIDs: []int64{9001, 9002}, PageIDs: []int64{6001, 6002},
			Steps: []StepInput{
				{Seq: 1, Action: "打开登录页", Expected: "显示表单", TargetPageID: int64Ptr(6001), AccountRole: "admin", Data: json.RawMessage(`{}`)},
				{Seq: 2, Action: "提交登录", Expected: "登录成功", TargetVersionSUTID: int64Ptr(9002), TargetAPIID: int64Ptr(4002), Data: json.RawMessage(`{"username":"admin"}`)},
			},
			Assertions: []AssertionInput{
				{StepSeq: 1, Kind: "ui", Target: "欢迎文案", Expected: "欢迎回来", TargetVersionSUTID: int64Ptr(9001)},
				{StepSeq: 2, Kind: "api", Target: "POST /login", Expected: "200", TargetVersionSUTID: int64Ptr(9002)},
			},
			Sources: []SourceInput{{Kind: "requirement", RefID: "3002"}},
		}},
	}
}

func int64Ptr(value int64) *int64 { return &value }

func TestProposeCreatesScenarioCaseAndSteps(t *testing.T) {
	service, repo, publisher, audit := newGraphFixture(t)
	result, err := service.Propose(context.Background(), 1001, validProposeInput(), 42, 7)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(result.ScenarioIDs) != 1 || len(result.CaseIDs) != 1 || len(result.CaseVersionIDs) != 1 {
		t.Fatalf("result ids = %+v", result)
	}
	if len(result.GraphHash) != 64 {
		t.Fatalf("graph hash = %q", result.GraphHash)
	}
	if len(repo.scenarios) != 1 || len(repo.cases) != 1 {
		t.Fatalf("repo scenarios=%d cases=%d", len(repo.scenarios), len(repo.cases))
	}
	item := repo.cases[result.CaseIDs[0]]
	if item == nil || item.current == nil {
		t.Fatalf("case not stored: %+v", item)
	}
	if item.current.Revision != 1 || item.current.Status != design.CaseStatusDraft {
		t.Fatalf("current = %+v", item.current)
	}
	if len(item.current.Steps) != 2 || len(item.current.Assertions) != 2 || len(item.current.Sources) != 1 {
		t.Fatalf("children = %d/%d/%d", len(item.current.Steps), len(item.current.Assertions), len(item.current.Sources))
	}
	if item.current.ContentHash == "" || len(item.current.ContentHash) != 64 {
		t.Fatalf("content hash = %q", item.current.ContentHash)
	}
	if item.entity.Status != design.CaseStatusDraft {
		t.Fatalf("entity status = %q", item.entity.Status)
	}

	graphDelta := publisher.byType("design.graph_delta")
	if len(graphDelta) != 1 {
		t.Fatalf("graph_delta events = %d (%v)", len(graphDelta), publisher.types())
	}
	if graphDelta[0].Payload["op"] != "propose" {
		t.Fatalf("graph delta payload = %v", graphDelta[0].Payload)
	}
	if len(publisher.byType("design.case_version_created")) != 1 {
		t.Fatalf("case_version_created events = %v", publisher.types())
	}
	foundAudit := false
	for _, event := range audit.events {
		if event.Action == "case.created" {
			foundAudit = true
		}
	}
	if !foundAudit {
		t.Fatalf("audit events = %+v", audit.events)
	}
}

func TestProposeRejectsBaseHashMismatch(t *testing.T) {
	service, repo, publisher, _ := newGraphFixture(t)
	in := validProposeInput()
	in.BaseGraphHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err := service.Propose(context.Background(), 1001, in, 1, 1)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("err = %v, want state_conflict", err)
	}
	if repo.lockCalls != 0 {
		t.Fatalf("lockCalls = %d, want 0", repo.lockCalls)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("events published on rejection: %v", publisher.types())
	}
}

func TestProposeRejectsRequirementVersionMismatch(t *testing.T) {
	service, _, _, _ := newGraphFixture(t)
	in := validProposeInput()
	in.RequirementVersionID = 9999
	_, err := service.Propose(context.Background(), 1001, in, 1, 1)
	if err == nil {
		t.Fatal("expected state conflict for stale requirement version")
	}
}

func TestProposeIsIdempotentForSameContent(t *testing.T) {
	service, repo, publisher, _ := newGraphFixture(t)
	first, err := service.Propose(context.Background(), 1001, validProposeInput(), 1, 1)
	if err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	eventCount := len(publisher.events)
	second, err := service.Propose(context.Background(), 1001, validProposeInput(), 1, 1)
	if err != nil {
		t.Fatalf("second Propose: %v", err)
	}
	if second.CaseVersionIDs[0] != first.CaseVersionIDs[0] || second.CaseIDs[0] != first.CaseIDs[0] {
		t.Fatalf("idempotent result changed: first=%+v second=%+v", first, second)
	}
	if len(repo.cases) != 1 || repo.cases[first.CaseIDs[0]].current.Revision != 1 {
		t.Fatalf("case history grew on idempotent propose")
	}
	if len(publisher.events) != eventCount {
		t.Fatalf("idempotent propose published %v", publisher.types()[eventCount:])
	}
}

func TestProposeUpdatesNaturalKeyInsteadOfDuplicating(t *testing.T) {
	service, repo, _, _ := newGraphFixture(t)
	first, err := service.Propose(context.Background(), 1001, validProposeInput(), 1, 1)
	if err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	updated := validProposeInput()
	updated.Cases[0].Steps[0].Action = "打开登录页面"
	second, err := service.Propose(context.Background(), 1001, updated, 1, 1)
	if err != nil {
		t.Fatalf("second Propose: %v", err)
	}
	if len(repo.cases) != 1 || len(repo.scenarios) != 1 {
		t.Fatalf("natural key duplicated: scenarios=%d cases=%d", len(repo.scenarios), len(repo.cases))
	}
	if second.CaseIDs[0] != first.CaseIDs[0] || second.ScenarioIDs[0] != first.ScenarioIDs[0] {
		t.Fatalf("natural key resolution changed IDs: %+v vs %+v", first, second)
	}
	if second.CaseVersionIDs[0] == first.CaseVersionIDs[0] {
		t.Fatal("changed content reused the old version id")
	}
	current := repo.cases[second.CaseIDs[0]].current
	if current.Revision != 2 || current.Steps[0].Action != "打开登录页面" {
		t.Fatalf("current version = %+v", current)
	}
}

func TestProposeUpdatesConfirmedCaseAsNewDraft(t *testing.T) {
	service, repo, _, _ := newGraphFixture(t)
	first, err := service.Propose(context.Background(), 1001, validProposeInput(), 1, 1)
	if err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	// Simulate the user confirming the first version.
	item := repo.cases[first.CaseIDs[0]]
	item.current.Status = design.CaseStatusConfirmed
	item.entity.Status = design.CaseStatusConfirmed
	for index := range item.versions {
		if item.versions[index].ID == first.CaseVersionIDs[0] {
			item.versions[index].Status = design.CaseStatusConfirmed
		}
	}

	updated := validProposeInput()
	updated.Cases[0].Steps[0].Expected = "显示登录表单"
	second, err := service.Propose(context.Background(), 1001, updated, 1, 1)
	if err != nil {
		t.Fatalf("second Propose: %v", err)
	}
	if second.CaseVersionIDs[0] == first.CaseVersionIDs[0] {
		t.Fatal("new draft revision reused the confirmed version id")
	}
	if repo.cases[first.CaseIDs[0]].entity.Status != design.CaseStatusDraft {
		t.Fatalf("entity status = %q, want draft", repo.cases[first.CaseIDs[0]].entity.Status)
	}
	history := repo.versions(first.CaseIDs[0])
	if len(history) != 2 {
		t.Fatalf("history = %d versions", len(history))
	}
	if history[0].Status != design.CaseStatusConfirmed {
		t.Fatalf("old version status = %q, want confirmed", history[0].Status)
	}
	if history[1].Status != design.CaseStatusDraft || history[1].Revision != 2 {
		t.Fatalf("new version = %+v", history[1])
	}
}

func TestReadGraphIncludesStaleAndCounts(t *testing.T) {
	service, repo, _, _ := newGraphFixture(t)
	repo.scenarios = []design.Scenario{{
		ID: 5001, VersionID: 1001, RequirementID: 3001, RequirementVersionID: 3002,
		Name: "正常登录", VersionSUTIDs: []int64{9001}, PageIDs: []int64{6001}, APIIDs: []int64{},
	}}
	confirmed := design.CaseVersion{ID: 7002, CaseID: 7001, Revision: 1, Title: "登录成功", Status: design.CaseStatusConfirmed, ContentHash: "a"}
	repo.cases[7001] = &fakeCase{
		entity:  design.CaseEntity{ID: 7001, ScenarioID: 5001, CurrentVersionID: int64Ptr(7002), Status: design.CaseStatusConfirmed},
		current: &confirmed,
	}
	stale := design.CaseVersion{ID: 7004, CaseID: 7003, Revision: 1, Title: "旧用例", Status: design.CaseStatusStale, ContentHash: "b"}
	repo.cases[7003] = &fakeCase{
		entity:  design.CaseEntity{ID: 7003, ScenarioID: 5001, CurrentVersionID: int64Ptr(7004), Status: design.CaseStatusStale},
		current: &stale,
	}
	repo.pending = 2

	view, err := service.ReadGraph(context.Background(), 1001)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	if view.VersionID != 1001 || len(view.GraphHash) != 64 {
		t.Fatalf("view = %+v", view)
	}
	if view.Stats.Cases != 2 || view.Stats.Confirmed != 1 || view.Stats.Stale != 1 {
		t.Fatalf("stats = %+v", view.Stats)
	}
	if view.PendingDesignCount != 2 {
		t.Fatalf("pending = %d", view.PendingDesignCount)
	}
	if len(view.Requirements) != 1 || len(view.Requirements[0].Scenarios) != 1 {
		t.Fatalf("requirements = %+v", view.Requirements)
	}
	cases := view.Requirements[0].Scenarios[0].Cases
	if len(cases) != 2 || cases[1].Status != design.CaseStatusStale {
		t.Fatalf("cases = %+v", cases)
	}
	if view.GeneratedAt.IsZero() {
		t.Fatal("generated_at is zero")
	}
}

func TestGetCaseAndOverview(t *testing.T) {
	repo := newFakeRepo()
	repo.caseData = design.CaseData{
		Case:        design.CaseEntity{ID: 7001, ScenarioID: 5001, CurrentVersionID: int64Ptr(7002), Status: design.CaseStatusDraft},
		Scenario:    design.Scenario{ID: 5001, Name: "正常登录"},
		Requirement: repo.requirement,
		Versions:    []design.CaseVersionSummary{{ID: 7002, Revision: 1, Status: design.CaseStatusDraft, Title: "登录成功"}},
		Selected: design.CaseVersion{
			ID: 7002, CaseID: 7001, Revision: 1, Title: "登录成功", Status: design.CaseStatusDraft,
			Priority: "P2", Steps: []design.CaseStep{{Seq: 1, Action: "打开登录页"}},
		},
	}
	repo.overview = design.OverviewData{
		RoundCount:    1,
		ActiveRound:   &design.Round{ID: 8001, ConversationID: 9001, Activation: design.ActivationManual, RequirementIDs: []int64{3001}},
		ActiveRoundNo: 1,
	}
	service := NewGraphService(repo, &fakeIDs{}, newFixedClock(), nil, nil, &fakeTeamRoom{})
	detail, err := service.GetCase(context.Background(), 1001, 7001, nil)
	if err != nil {
		t.Fatalf("GetCase: %v", err)
	}
	if detail.CaseID != 7001 || !detail.Confirmable {
		t.Fatalf("detail = %+v", detail)
	}
	if len(detail.Versions) != 1 || len(detail.Selected.Steps) != 1 {
		t.Fatalf("detail children = %+v", detail)
	}
	overview, err := service.Overview(context.Background(), 1001)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if overview.ActiveRound == nil || overview.ActiveRound.ID != 8001 || overview.ActiveRound.RoundNo != 1 {
		t.Fatalf("overview = %+v", overview)
	}
}

func TestProposeCrossSUTStepsAllowed(t *testing.T) {
	service, _, _, _ := newGraphFixture(t)
	in := validProposeInput()
	result, err := service.Propose(context.Background(), 1001, in, 1, 1)
	if err != nil {
		t.Fatalf("cross-SUT Propose: %v", err)
	}
	if len(result.CaseVersionIDs) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestProposeMapsRepositoryNotFound(t *testing.T) {
	service, repo, _, _ := newGraphFixture(t)
	repo.requirement = design.RequirementRef{}
	_, err := service.Propose(context.Background(), 1001, validProposeInput(), 1, 1)
	if err == nil {
		t.Fatal("expected error for missing requirement")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}

var _ = time.Now
