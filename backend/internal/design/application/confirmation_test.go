package designapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func newConfirmFixture(t *testing.T) (*GraphService, *fakeRepo, *fakePublisher, *fakeAudit) {
	t.Helper()
	repo := newFakeRepo()
	repo.scenarios = []design.Scenario{{
		ID: 5001, VersionID: 1001, RequirementID: 3001, RequirementVersionID: 3002,
		Name: "正常登录",
	}}
	publisher := &fakePublisher{}
	audit := &fakeAudit{}
	service := NewGraphService(repo, &fakeIDs{}, newFixedClock(), audit, publisher, &fakeTeamRoom{})
	return service, repo, publisher, audit
}

func TestConfirmCaseIdempotent(t *testing.T) {
	service, repo, publisher, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusDraft)

	result, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(result.Confirmed) != 1 || len(result.Skipped) != 0 {
		t.Fatalf("confirmed=%+v skipped=%+v", result.Confirmed, result.Skipped)
	}
	if result.ConfirmedAt.IsZero() {
		t.Fatal("confirmed_at is zero")
	}
	if result.BatchID == "" || !strings.HasPrefix(result.BatchID, "batch_") {
		t.Fatalf("batch id = %q", result.BatchID)
	}
	if len(repo.confirmations) != 1 {
		t.Fatalf("confirmation rows = %d", len(repo.confirmations))
	}
	if status := repo.cases[7001].current.Status; status != design.CaseStatusConfirmed {
		t.Fatalf("version status = %q", status)
	}
	if len(publisher.byType("design.case_confirmed")) != 0 {
		// Event is emitted after commit; this call path emits it.
	}
	if len(publisher.byType("design.case_confirmed")) == 0 {
		t.Fatalf("missing design.case_confirmed event: %v", publisher.types())
	}

	eventCount := len(publisher.events)
	second, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	if err != nil {
		t.Fatalf("second Confirm: %v", err)
	}
	if len(second.Confirmed) != 0 || len(second.Skipped) != 1 || second.Skipped[0].Status != design.CaseStatusConfirmed {
		t.Fatalf("idempotent result = %+v", second)
	}
	if len(publisher.events) != eventCount {
		t.Fatalf("idempotent confirm published %v", publisher.types()[eventCount:])
	}
}

func TestConfirmScenarioConfirmsOnlyDrafts(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "用例一", design.CaseStatusDraft)
	seedFakeCase(t, repo, 7002, 5001, "用例二", design.CaseStatusDraft)
	seedFakeCase(t, repo, 7003, 5001, "用例三", design.CaseStatusConfirmed)
	seedFakeCase(t, repo, 7004, 5001, "用例四", design.CaseStatusStale)

	result, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeScenario, TargetID: 5001,
	}, 42)
	if err != nil {
		t.Fatalf("Confirm scenario: %v", err)
	}
	if len(result.Confirmed) != 2 || len(result.Skipped) != 2 {
		t.Fatalf("confirmed=%d skipped=%d", len(result.Confirmed), len(result.Skipped))
	}
	for _, caseID := range []int64{7001, 7002} {
		if status := repo.cases[caseID].current.Status; status != design.CaseStatusConfirmed {
			t.Fatalf("case %d status = %q", caseID, status)
		}
	}
	if status := repo.cases[7003].current.Status; status != design.CaseStatusConfirmed {
		t.Fatalf("confirmed case changed: %q", status)
	}
	if status := repo.cases[7004].current.Status; status != design.CaseStatusStale {
		t.Fatalf("stale case changed: %q", status)
	}
	if len(repo.confirmations) != 2 {
		t.Fatalf("confirmation rows = %d", len(repo.confirmations))
	}
	for _, confirmation := range repo.confirmations {
		if confirmation.Scope != design.ConfirmScopeScenario || confirmation.TargetScopeID != 5001 {
			t.Fatalf("confirmation = %+v", confirmation)
		}
		if len(confirmation.AffectedCaseVersionIDs) != 2 {
			t.Fatalf("affected ids = %v", confirmation.AffectedCaseVersionIDs)
		}
		if confirmation.Snapshot["title"] == nil || confirmation.Snapshot["step_count"] == nil {
			t.Fatalf("snapshot = %v", confirmation.Snapshot)
		}
	}
}

func TestConfirmScenarioRejectsWhenNoDraft(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "用例一", design.CaseStatusConfirmed)
	seedFakeCase(t, repo, 7002, 5001, "用例二", design.CaseStatusStale)

	_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeScenario, TargetID: 5001,
	}, 42)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("err = %v, want state_conflict", err)
	}
}

func TestConfirmRequirementRejectsWhenNoDraft(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "用例一", design.CaseStatusConfirmed)

	_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: 3001,
	}, 42)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("err = %v, want state_conflict", err)
	}
}

func TestConfirmRequirementRequiresClarifiedAndSameReqVersion(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "用例一", design.CaseStatusDraft)

	clarified := repo.requirement
	repo.requirement.Status = "draft"
	_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: 3001,
	}, 42)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
		t.Fatalf("non-clarified err = %v, want invalid_argument", err)
	}

	repo.requirement = clarified
	repo.scenarios[0].RequirementVersionID = 9999
	_, err = service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeScenario, TargetID: 5001,
	}, 42)
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
		t.Fatalf("stale requirement version err = %v, want invalid_argument", err)
	}
}

func TestConfirmSupersedesOlderVersions(t *testing.T) {
	service, repo, publisher, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusConfirmed, design.CaseStatusDraft)

	result, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(result.Confirmed) != 1 {
		t.Fatalf("confirmed = %+v", result.Confirmed)
	}
	history := repo.versions(7001)
	if len(history) != 2 {
		t.Fatalf("history = %+v", history)
	}
	if history[0].Status != design.CaseStatusSuperseded {
		t.Fatalf("old version = %q, want superseded", history[0].Status)
	}
	if history[1].Status != design.CaseStatusConfirmed {
		t.Fatalf("new version = %q, want confirmed", history[1].Status)
	}
	supersededEvents := publisher.byType("design.case_version_superseded")
	if len(supersededEvents) != 1 {
		t.Fatalf("superseded events = %v", publisher.types())
	}
	if supersededEvents[0].Payload["case_version_id"] == nil {
		t.Fatalf("superseded payload = %v", supersededEvents[0].Payload)
	}
}

func TestConfirmRejectsStaleCase(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "登录成功", design.CaseStatusStale)

	_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("err = %v, want state_conflict", err)
	}
}

func TestConfirmValidatesInput(t *testing.T) {
	service, _, _, _ := newConfirmFixture(t)
	for _, input := range []ConfirmInput{
		{Scope: "batch", TargetID: 7001},
		{Scope: design.ConfirmScopeCase, TargetID: 0},
		{Scope: design.ConfirmScopeCase, TargetID: 7001, BatchID: strings.Repeat("b", 65)},
	} {
		if _, err := service.Confirm(context.Background(), 1001, input, 42); err == nil {
			t.Fatalf("input %+v was accepted", input)
		}
	}
	_, err := service.Confirm(context.Background(), 1001, ConfirmInput{Scope: design.ConfirmScopeCase, TargetID: 9999}, 42)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("missing target err = %v, want not_found", err)
	}
}
