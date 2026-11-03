package designapp

import (
	"context"
	"errors"
	"net/http"
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

func TestConfirmCaseRejectsOldRequirementVersionDraft(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "旧草案", design.CaseStatusDraft)
	repo.requirement.CurrentVersionID = 3003

	_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: 7001,
	}, 42)
	if err == nil {
		t.Fatal("Confirm accepted a case bound to an older requirement version")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
		t.Fatalf("Confirm error = %v, want invalid_argument", err)
	}
	if len(repo.confirmations) != 0 {
		t.Fatalf("confirmation rows = %d, want 0", len(repo.confirmations))
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
		if confirmation.Snapshot["title"] == nil || confirmation.Snapshot["step_count"] != 1 {
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

// TestConfirmCasesByIDsConfirmsOnlySelected pins the explicit-selection gate
// path: only the requested current case versions are frozen, the rest stay
// draft, and an already-confirmed case is idempotent.
func TestConfirmCasesByIDsConfirmsOnlySelected(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "用例一", design.CaseStatusDraft)
	seedFakeCase(t, repo, 7002, 5001, "用例二", design.CaseStatusDraft)
	seedFakeCase(t, repo, 7003, 5001, "用例三", design.CaseStatusConfirmed)

	if err := service.ConfirmCasesByIDs(context.Background(), 1001, []int64{7001}); err != nil {
		t.Fatalf("ConfirmCasesByIDs: %v", err)
	}
	if status := repo.cases[7001].current.Status; status != design.CaseStatusConfirmed {
		t.Fatalf("case 7001 status = %q, want confirmed", status)
	}
	if status := repo.cases[7002].current.Status; status != design.CaseStatusDraft {
		t.Fatalf("case 7002 status = %q, want draft", status)
	}
	if status := repo.cases[7003].current.Status; status != design.CaseStatusConfirmed {
		t.Fatalf("case 7003 status = %q, want confirmed", status)
	}
	if len(repo.confirmations) != 1 {
		t.Fatalf("confirmation rows = %d, want 1", len(repo.confirmations))
	}
	if repo.confirmations[0].Scope != design.ConfirmScopeCase || repo.confirmations[0].TargetScopeID != 7001 {
		t.Fatalf("confirmation = %+v", repo.confirmations[0])
	}

	// Confirming an already-confirmed case is idempotent.
	before := len(repo.confirmations)
	if err := service.ConfirmCasesByIDs(context.Background(), 1001, []int64{7003}); err != nil {
		t.Fatalf("idempotent ConfirmCasesByIDs: %v", err)
	}
	if len(repo.confirmations) != before {
		t.Fatalf("idempotent confirmation wrote %d rows", len(repo.confirmations)-before)
	}
}

// TestConfirmCasesByIDsAllConfirmsEveryDraft pins the empty-list meaning: all
// current confirmed-eligible cases of the version are confirmed.
func TestConfirmCasesByIDsAllConfirmsEveryDraft(t *testing.T) {
	for _, selection := range [][]int64{nil, {}} {
		service, repo, _, _ := newConfirmFixture(t)
		seedFakeCase(t, repo, 7001, 5001, "用例一", design.CaseStatusDraft)
		seedFakeCase(t, repo, 7002, 5001, "用例二", design.CaseStatusDraft)
		seedFakeCase(t, repo, 7003, 5001, "用例三", design.CaseStatusConfirmed)

		if err := service.ConfirmCasesByIDs(context.Background(), 1001, selection); err != nil {
			t.Fatalf("ConfirmCasesByIDs(%#v): %v", selection, err)
		}
		for _, caseID := range []int64{7001, 7002, 7003} {
			if status := repo.cases[caseID].current.Status; status != design.CaseStatusConfirmed {
				t.Fatalf("selection %#v: case %d status = %q, want confirmed", selection, caseID, status)
			}
		}
		if len(repo.confirmations) != 2 {
			t.Fatalf("selection %#v: confirmation rows = %d, want 2", selection, len(repo.confirmations))
		}
	}
}

// TestConfirmCasesByIDsRejectsInvalidAndForeign pins the typed validation: a
// zero ID is a 400 and an unknown/foreign case ID is a 404, both without
// confirming anything.
func TestConfirmCasesByIDsRejectsInvalidAndForeign(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7001, 5001, "用例一", design.CaseStatusDraft)

	var apiErr *platform.Error
	err := service.ConfirmCasesByIDs(context.Background(), 1001, []int64{0})
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
		t.Fatalf("zero id err = %v, want invalid_argument", err)
	}

	err = service.ConfirmCasesByIDs(context.Background(), 1001, []int64{9999})
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("foreign case err = %v, want not_found", err)
	}

	err = service.ConfirmCasesByIDs(context.Background(), 1002, []int64{7001})
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("foreign version err = %v, want not_found", err)
	}

	if status := repo.cases[7001].current.Status; status != design.CaseStatusDraft {
		t.Fatalf("failed selection confirmed case 7001: %q", status)
	}
	if len(repo.confirmations) != 0 {
		t.Fatalf("failed selection wrote confirmations: %#v", repo.confirmations)
	}
}

// TestConfirmCasesEmptyNoCases pins the spec §5.3 entry condition for executing
// ("有已确认用例"): confirming "all" over a version with no draft case must not
// report success; the gate answers a typed 409 no_cases_to_confirm and writes
// no confirmation, event or audit.
func TestConfirmCasesEmptyNoCases(t *testing.T) {
	service, repo, publisher, audit := newConfirmFixture(t)

	for _, selection := range [][]int64{nil, {}} {
		err := service.ConfirmCasesByIDs(context.Background(), 1001, selection)
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("selection %#v: error = %v, want a typed *platform.Error", selection, err)
		}
		if apiErr.Status != http.StatusConflict || apiErr.Code != "no_cases_to_confirm" {
			t.Fatalf("selection %#v: error = %d/%s, want 409/no_cases_to_confirm",
				selection, apiErr.Status, apiErr.Code)
		}
	}
	if len(repo.confirmations) != 0 {
		t.Fatalf("confirmation rows = %d, want 0", len(repo.confirmations))
	}
	if len(publisher.events) != 0 || len(audit.events) != 0 {
		t.Fatalf("failed confirm-all published events=%#v audit=%#v", publisher.events, audit.events)
	}
}

// TestConfirmedCaseVersionKeepsStepsAndAssertions pins the Task 95 / PO O8 fix:
// the confirmation finalize path must freeze only a complete case version. The
// PO's AC-FR-011-01 was confirmed and executed from case_version
// 375974303872585728 with 1 step / 0 assertions although the design reported 5
// steps / 3 assertions, and the run ended inconclusive(incomplete). A current
// draft that lost its assertions is now refused with a typed
// incomplete_case_version conflict before anything is frozen; after the design
// repairs the revision, confirmation keeps every designed step and assertion.
func TestConfirmedCaseVersionKeepsStepsAndAssertions(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCaseVersions(t, repo, 7001, 5001, "AC-FR-011-01 登录成功（正向）",
		fakeCaseVersionSpec{steps: 5, assertions: 3, status: design.CaseStatusDraft},
		fakeCaseVersionSpec{steps: 1, assertions: 0, status: design.CaseStatusDraft},
	)

	_, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: 3001,
	}, 42)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("Confirm incomplete version error = %v, want a typed *platform.Error", err)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != "incomplete_case_version" {
		t.Fatalf("error = %d/%s, want 409/incomplete_case_version", apiErr.Status, apiErr.Code)
	}
	if got, _ := apiErr.Details["case_version_id"].(string); got != "70012" {
		t.Fatalf("details case_version_id = %v, want 70012", apiErr.Details["case_version_id"])
	}
	if got, _ := apiErr.Details["step_count"].(int); got != 1 {
		t.Fatalf("details step_count = %v, want 1", apiErr.Details["step_count"])
	}
	if got, _ := apiErr.Details["assertion_count"].(int); got != 0 {
		t.Fatalf("details assertion_count = %v, want 0", apiErr.Details["assertion_count"])
	}
	if next, _ := apiErr.Details["next_action"].(string); next == "" {
		t.Fatalf("details = %#v, want repair next_action guidance", apiErr.Details)
	}
	if len(repo.confirmations) != 0 {
		t.Fatalf("incomplete version wrote %d confirmation row(s), want 0", len(repo.confirmations))
	}
	if status := repo.cases[7001].current.Status; status != design.CaseStatusDraft {
		t.Fatalf("incomplete version status = %q, want draft (never confirmed)", status)
	}

	// The design repair restores the complete content (5 steps / 3 assertions);
	// confirmation must then freeze that repaired version with its content.
	repaired := appendFakeCaseVersion(t, repo, 7001, 5, 3, design.CaseStatusDraft)
	result, err := service.Confirm(context.Background(), 1001, ConfirmInput{
		Scope: design.ConfirmScopeRequirement, TargetID: 3001,
	}, 42)
	if err != nil {
		t.Fatalf("Confirm repaired version: %v", err)
	}
	if len(result.Confirmed) != 1 || result.Confirmed[0].CaseVersionID != repaired.ID {
		t.Fatalf("confirmed = %+v, want case version %d", result.Confirmed, repaired.ID)
	}
	if len(repo.confirmations) != 1 {
		t.Fatalf("confirmation rows = %d, want 1", len(repo.confirmations))
	}
	if got := repo.confirmations[0].Snapshot["step_count"]; got != 5 {
		t.Fatalf("confirmed snapshot step_count = %v, want 5", got)
	}
	if got := repo.confirmations[0].Snapshot["assertion_count"]; got != 3 {
		t.Fatalf("confirmed snapshot assertion_count = %v, want 3", got)
	}
	current := repo.cases[7001].current
	if status := current.Status; status != design.CaseStatusConfirmed {
		t.Fatalf("repaired version status = %q, want confirmed", status)
	}
	if len(current.Steps) != 5 || len(current.Assertions) != 3 {
		t.Fatalf("confirmed version content = %d steps / %d assertions, want 5/3",
			len(current.Steps), len(current.Assertions))
	}
}

// TestConfirmCasesIncompleteSelectionIsAtomic pins the explicit-selection case
// gate: when the selected batch contains an incomplete version, the typed block
// must fire before any complete sibling is frozen. Otherwise the user would be
// left with a partially confirmed selection after a failed batch confirm.
func TestConfirmCasesIncompleteSelectionIsAtomic(t *testing.T) {
	service, repo, _, _ := newConfirmFixture(t)
	seedFakeCase(t, repo, 7002, 5001, "完整用例", design.CaseStatusDraft)
	seedFakeCaseVersions(t, repo, 7001, 5001, "不完整用例",
		fakeCaseVersionSpec{steps: 1, assertions: 0, status: design.CaseStatusDraft},
	)

	err := service.ConfirmCasesByIDs(context.Background(), 1001, []int64{7002, 7001})
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("ConfirmCasesByIDs error = %v, want a typed *platform.Error", err)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != "incomplete_case_version" {
		t.Fatalf("error = %d/%s, want 409/incomplete_case_version", apiErr.Status, apiErr.Code)
	}
	if len(repo.confirmations) != 0 {
		t.Fatalf("partially confirmed selection wrote %d row(s), want 0", len(repo.confirmations))
	}
	for _, caseID := range []int64{7001, 7002} {
		if status := repo.cases[caseID].current.Status; status != design.CaseStatusDraft {
			t.Fatalf("case %d status = %q after blocked selection, want draft", caseID, status)
		}
	}
}
