package adapters

import (
	"context"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// v0.2.4-F T12 (SC-F1): the natural-language design contract fields already
// live on the domain snapshot (CaseSnapshot.SUTDescription/CoveredACs,
// CaseStep.TargetDescription). The adapter is the single mapping path into the
// agent task-context DTO — it must carry them, not drop them, and must not
// invent a second source.

// TestTaskProviderMapsNLFields pins the adapter leg of the NL chain: every
// natural-language field on the domain snapshot survives mapTaskSnapshot, and
// the SUT catalog carries intro/environment_id.
func TestTaskProviderMapsNLFields(t *testing.T) {
	snapshot := execution.TaskSnapshot{
		Case: execution.CaseSnapshot{
			CaseVersionID:  41,
			Title:          "登录后创建订单",
			Description:    "desc",
			SUTDescription: "订单中心 Web 端，面向内部运营人员",
			Preconditions:  "数据库中已存在运营账号 seed_user",
			CoveredACs:     []string{"AC-1", "AC-2"},
			Priority:       "P1",
			DefaultSUTID:   7,
			Steps: []execution.CaseStep{{
				Seq: 1, Action: "打开订单页", Expected: "订单表单可见",
				TargetDescription: "订单列表页右上角的新建订单入口", AccountRole: "admin",
			}},
			Assertions: []execution.CaseAssertion{{
				StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "订单表单可见", SUTID: 7,
			}},
		},
		CaseRun:       execution.CaseRun{ID: 3001, WaveID: 2001, IntentID: 1001},
		EnvironmentID: 3,
		SUTs: []execution.SUTBinding{{
			SUTID: 7, SUTName: "orders", Intro: "订单中心：下单、查询与审核",
			EnvironmentID: 3, EnvironmentName: "test", BaseURL: "http://127.0.0.1:13335",
			AccountRoles: []string{"admin"},
		}},
	}
	task := mapTaskSnapshot(snapshot)
	if task.SutDescription != snapshot.Case.SUTDescription {
		t.Fatalf("SutDescription = %q, want %q (the domain snapshot is the single source)", task.SutDescription, snapshot.Case.SUTDescription)
	}
	if len(task.CoveredACs) != 2 || task.CoveredACs[0] != "AC-1" || task.CoveredACs[1] != "AC-2" {
		t.Fatalf("CoveredACs = %v, want [AC-1 AC-2]", task.CoveredACs)
	}
	if task.Preconditions != snapshot.Case.Preconditions {
		t.Fatalf("Preconditions = %q, want %q", task.Preconditions, snapshot.Case.Preconditions)
	}
	if task.EnvironmentID != 3 {
		t.Fatalf("EnvironmentID = %d, want 3", task.EnvironmentID)
	}
	if len(task.Steps) != 1 || task.Steps[0].TargetDescription != snapshot.Case.Steps[0].TargetDescription {
		t.Fatalf("steps = %+v, want the prose target_description mapped", task.Steps)
	}
	if len(task.SUTs) != 1 || task.SUTs[0].Intro != snapshot.SUTs[0].Intro || task.SUTs[0].EnvironmentID != 3 {
		t.Fatalf("suts = %+v, want intro and environment_id mapped", task.SUTs)
	}
	// CoveredACs must be a copy: mutating the DTO must not write back into the
	// immutable domain snapshot.
	task.CoveredACs[0] = "AC-9"
	if snapshot.Case.CoveredACs[0] != "AC-1" {
		t.Fatal("CoveredACs aliases the domain snapshot slice; the mapping must copy it")
	}
}

// TestResolveBindingCarriesIntroAndEnvironmentID pins the producer leg: the
// version adapter resolves the SUT catalog from the bound environment and must
// carry the SUT intro (identity/label prose) and the environment id into the
// execution SUTBinding, so the task context can render the catalog the design
// contract promises (name/intro/base_url/environment_id, no credentials).
func TestResolveBindingCarriesIntroAndEnvironmentID(t *testing.T) {
	view := scopeFixtureVersion()
	view.Binding.Environment.SUTs[0].Intro = "订单中心：下单、查询与审核"
	adapter := NewVersionAdapter(&fakeVersionService{view: view})

	bindings, err := adapter.Resolve(context.Background(), 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings = %#v, want exactly one", bindings)
	}
	if bindings[0].Intro != "订单中心：下单、查询与审核" {
		t.Fatalf("binding intro = %q, want the SUT catalog intro", bindings[0].Intro)
	}
	if bindings[0].EnvironmentID != 4 {
		t.Fatalf("binding environment id = %d, want the bound environment 4", bindings[0].EnvironmentID)
	}
}
