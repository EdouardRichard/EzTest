package adapters

import (
	"reflect"
	"testing"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
)

// TestMapExecutableCaseInheritsDefaultTargets pins the P2 contract (case_step
// target_version_sut_id empty inherits case_version.default_version_sut_id) at
// the execution read boundary: a design-authored case with null step/assertion
// targets must reach the execution app with concrete targets. QA-F1's run died
// because Prepare saw target 0 and rejected the case before the agent started.
func TestMapExecutableCaseInheritsDefaultTargets(t *testing.T) {
	defaultSUT := int64(7001)
	view := designapp.ExecutableCaseView{
		CaseID:              51,
		CaseVersionID:       41,
		DefaultVersionSUTID: defaultSUT,
		VersionSUTIDs:       []int64{defaultSUT},
		Title:               "登录流程",
		Steps: []designapp.ExecutableStepView{
			{Seq: 1, Action: "打开登录页", AccountRole: "anonymous"},
			{Seq: 2, Action: "提交", TargetVersionSUTID: 9002},
		},
		Assertions: []designapp.ExecutableAssertionView{
			{StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "表单可见"},
		},
	}

	snapshot := mapExecutableCase(view)
	if got := snapshot.Steps[0].TargetVersionSUTID; got != defaultSUT {
		t.Fatalf("step 1 target = %d, want the inherited default %d", got, defaultSUT)
	}
	if got := snapshot.Steps[1].TargetVersionSUTID; got != 9002 {
		t.Fatalf("step 2 explicit target = %d, want 9002", got)
	}
	if got := snapshot.Assertions[0].TargetVersionSUTID; got != defaultSUT {
		t.Fatalf("assertion target = %d, want the inherited default %d", got, defaultSUT)
	}
	if want := []int64{7001, 9002}; !reflect.DeepEqual(snapshot.VersionSUTIDs, want) {
		t.Fatalf("version_sut_ids = %v, want the resolved union %v", snapshot.VersionSUTIDs, want)
	}
}

// TestMapExecutableCaseUsesSoleBindingWhenNoDefault covers cases created without
// a default_version_sut_id: the single version_sut binding is unambiguous and
// must be inherited instead of leaving target 0 for the execution app to reject.
func TestMapExecutableCaseUsesSoleBindingWhenNoDefault(t *testing.T) {
	view := designapp.ExecutableCaseView{
		CaseID:              52,
		CaseVersionID:       42,
		DefaultVersionSUTID: 0,
		VersionSUTIDs:       []int64{7002},
		Steps:               []designapp.ExecutableStepView{{Seq: 1, Action: "打开页面"}},
	}

	snapshot := mapExecutableCase(view)
	if got := snapshot.Steps[0].TargetVersionSUTID; got != 7002 {
		t.Fatalf("step target = %d, want the sole binding 7002", got)
	}
	if want := []int64{7002}; !reflect.DeepEqual(snapshot.VersionSUTIDs, want) {
		t.Fatalf("version_sut_ids = %v, want [7002]", snapshot.VersionSUTIDs)
	}
}
