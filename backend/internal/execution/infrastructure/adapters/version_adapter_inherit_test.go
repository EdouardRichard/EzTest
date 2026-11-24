package adapters

import (
	"reflect"
	"testing"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
)

// TestMapExecutableCaseProjectsNaturalLanguageTargets pins the v0.2.4-B design
// read contract at the execution boundary.
//
// This file used to pin the P2 target inheritance (case_step.target_sut_id
// empty inherits case_version.default_sut_id, and a case with no default
// inherits the version's sole binding). T9's design_0004 dropped every one of
// those columns — case_version.default_version_sut_id,
// case_version.version_sut_ids, case_step.target_version_sut_id /
// target_page_id / target_api_id and case_assertion.target_version_sut_id /
// database_id — in favour of prose (sut_description / target_description) plus
// covered_acs. So mapExecutableCase is now a plain 1:1 projection and there is
// nothing left to inherit here.
//
// What must still hold, and what QA-F1 depends on, is that the projection never
// invents or requires a SUT id: DefaultSUTID/SUTIDs stay zero/empty on a fresh
// read and are resolved from the version binding by executionSUTBindings during
// Prepare (pinned by TestCaseRunPersistsStepsEvidence in the application
// package). The one id design still supplies is case_assertion.sut_id, which
// only a db assertion carries and which design validates for existence only —
// execution checks it against the bindings.
func TestMapExecutableCaseProjectsNaturalLanguageTargets(t *testing.T) {
	view := designapp.ExecutableCaseView{
		CaseID:               51,
		CaseVersionID:        41,
		RequirementID:        501,
		RequirementVersionID: 601,
		Title:                "登录流程",
		Description:          "用户使用账号密码登录",
		SutDescription:       "订单中心预发环境",
		Preconditions:        "已创建测试账号",
		CoveredACs:           []string{"AC-3", "AC-7"},
		Priority:             "P0",
		Steps: []designapp.ExecutableStepView{
			{Seq: 1, Action: "打开登录页", Expected: "出现登录表单", TargetDescription: "登录页", AccountRole: "anonymous"},
			{Seq: 2, Action: "提交", Expected: "进入首页", TargetDescription: "登录表单", AccountRole: "admin"},
		},
		Assertions: []designapp.ExecutableAssertionView{
			{CaseAssertionID: 900, StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "表单可见"},
			{CaseAssertionID: 901, StepSeq: 2, AssertionSeq: 1, Kind: "db", Expected: "登录记录写入", SUTID: 9002},
		},
	}

	snapshot := mapExecutableCase(view)

	if snapshot.SUTDescription != "订单中心预发环境" {
		t.Fatalf("sut_description = %q, want the design prose", snapshot.SUTDescription)
	}
	if want := []string{"AC-3", "AC-7"}; !reflect.DeepEqual(snapshot.CoveredACs, want) {
		t.Fatalf("covered_acs = %v, want %v", snapshot.CoveredACs, want)
	}
	// A fresh read resolves no SUT: that is Prepare's job, from the binding.
	if snapshot.DefaultSUTID != 0 || len(snapshot.SUTIDs) != 0 {
		t.Fatalf("default/sut ids = %d/%v, want none before Prepare resolves them",
			snapshot.DefaultSUTID, snapshot.SUTIDs)
	}
	if len(snapshot.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(snapshot.Steps))
	}
	if got := snapshot.Steps[1].TargetDescription; got != "登录表单" {
		t.Fatalf("step 2 target_description = %q, want the design prose", got)
	}
	if got := snapshot.Steps[0].AccountRole; got != "anonymous" {
		t.Fatalf("step 1 account_role = %q, want anonymous", got)
	}
	// The db assertion keeps the only SUT id design still supplies; the ui
	// assertion carries none.
	if got := snapshot.Assertions[1].SUTID; got != 9002 {
		t.Fatalf("db assertion sut_id = %d, want 9002", got)
	}
	if got := snapshot.Assertions[0].SUTID; got != 0 {
		t.Fatalf("ui assertion sut_id = %d, want 0", got)
	}
	if got := snapshot.Assertions[0].CaseAssertionID; got != 900 {
		t.Fatalf("assertion id = %d, want the design case_assertion.id 900", got)
	}
	// covered_acs must be copied, not aliased: a caller mutating the design view
	// must not be able to move the frozen execution input.
	view.CoveredACs[0] = "AC-99"
	if snapshot.CoveredACs[0] != "AC-3" {
		t.Fatalf("covered_acs aliases the design view: %v", snapshot.CoveredACs)
	}
}
