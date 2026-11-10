package executionapp

import (
	"reflect"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func observationInt64Ptr(value int64) *int64 { return &value }

// TestCaseSuccessFactSkipsAnonymousRole pins R2-OBS-02: `anonymous` is the
// documented label of an unauthenticated step (see executionSUTBindings), not a
// bound test account. It must never seed the knowledge success fact, otherwise
// the success hook warns `account role "anonymous" is not bound`. Other roles
// (bound or not) keep their existing projection.
func TestCaseSuccessFactSkipsAnonymousRole(t *testing.T) {
	run := execution.CaseRun{ID: 3001, CaseVersionID: 41}
	snapshot := execution.CaseSnapshot{
		Title: "未登录访问后台（负向）",
		Steps: []execution.CaseStep{
			{Seq: 1, AccountRole: "anonymous", TargetPageID: observationInt64Ptr(71)},
			{Seq: 2, AccountRole: " admin ", TargetAPIID: observationInt64Ptr(81)},
			{Seq: 3, AccountRole: "ANONYMOUS"},
		},
	}

	fact := caseSuccessFact(10, run, snapshot, execution.CaseRunSummary{EvidenceCount: 2})
	if !reflect.DeepEqual(fact.AccountRoles, []string{"admin"}) {
		t.Fatalf("account roles = %#v, want [admin]", fact.AccountRoles)
	}
	if !reflect.DeepEqual(fact.PageIDs, []int64{71}) {
		t.Fatalf("page ids = %#v, want [71]", fact.PageIDs)
	}
	if !reflect.DeepEqual(fact.APIIDs, []int64{81}) {
		t.Fatalf("api ids = %#v, want [81]", fact.APIIDs)
	}

	onlyAnonymous := execution.CaseSnapshot{
		Title: "未登录访问",
		Steps: []execution.CaseStep{{Seq: 1, AccountRole: "anonymous"}},
	}
	fact = caseSuccessFact(10, run, onlyAnonymous, execution.CaseRunSummary{})
	if len(fact.AccountRoles) != 0 {
		t.Fatalf("account roles = %#v, want none for an unauthenticated-only case", fact.AccountRoles)
	}
}
