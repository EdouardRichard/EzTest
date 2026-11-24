package design

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func int64Ptr(value int64) *int64 { return &value }

// TestValidAssertionKindSet pins the closed P2 assertion-kind enum (GD-22):
// exactly ui/api/db/text are accepted and every other kind is rejected, so a
// case version can never carry a kind outside that set.
func TestValidAssertionKindSet(t *testing.T) {
	for _, kind := range []string{AssertionUI, AssertionAPI, AssertionDB, AssertionText} {
		if !ValidAssertionKind(kind) {
			t.Fatalf("ValidAssertionKind(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{"", "screenshot", "pixel", "baseline", "UI", "bogus"} {
		if ValidAssertionKind(kind) {
			t.Fatalf("ValidAssertionKind(%q) = true, want false (enum is ui/api/db/text only)", kind)
		}
	}
}

// testACIDs is the requirement's acceptance-criterion id set as T8's
// RequirementContext loads it: "AC-<n>" strings in numeric-suffix order. AC-10
// is deliberately present so the tests exercise a two-digit id.
var testACIDs = []string{"AC-1", "AC-2", "AC-10"}

func validProposalContext() ProposalContext {
	return ProposalContext{
		VersionID:            1001,
		RequirementID:        3001,
		RequirementVersionID: 3002,
		CurrentHash:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RequirementACIDs:     append([]string(nil), testACIDs...),
	}
}

func validProposal() Proposal {
	return Proposal{
		RequirementID:        3001,
		RequirementVersionID: 3002,
		Scenarios: []ScenarioProposal{
			{
				Ref:         "s1",
				Name:        "正常登录",
				Description: "正常路径",
			},
		},
		Cases: []CaseProposal{
			{
				Ref:            "c1",
				ScenarioRef:    "s1",
				Title:          "账号密码登录成功",
				Description:    "使用有效账号登录",
				SutDescription: "商城前台登录页与登录接口。",
				Preconditions:  "已有可用账号",
				CoveredACs:     []string{"AC-1", "AC-2"},
				TestData:       json.RawMessage(`{"username":"admin","password":"secret"}`),
				Priority:       "P1",
				Steps: []StepProposal{
					{
						Seq:               1,
						Action:            "打开登录页",
						Expected:          "显示登录表单",
						TargetDescription: "登录页的账号输入框",
						AccountRole:       "admin",
						Data:              json.RawMessage(`{}`),
					},
					{
						Seq:               2,
						Action:            "提交账号密码",
						Expected:          "接口返回成功",
						TargetDescription: "POST /login 接口",
						AccountRole:       "admin",
						Data:              json.RawMessage(`{"username":"admin"}`),
					},
				},
				Assertions: []AssertionProposal{
					{StepSeq: 1, Kind: "ui", Target: "欢迎文案", Expected: "欢迎回来"},
					{StepSeq: 2, Kind: "api", Target: "POST /login", Expected: "200"},
				},
				Sources: []SourceProposal{
					{Kind: "requirement", RefID: "3002"},
				},
			},
		},
	}
}

func assertInvalidArgument(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *platform.Error, got %T: %v", err, err)
	}
	if apiErr.Code != "invalid_argument" && apiErr.Code != "state_conflict" {
		t.Fatalf("expected invalid_argument/state_conflict, got %q (%v)", apiErr.Code, err)
	}
}

// assertInvalidField checks that the rejection names the expected field in the
// error's Details, so a test cannot pass because some unrelated rule fired first.
// wantField is matched on a dot boundary, so "title" accepts the fully qualified
// "cases[0].title" without the test having to hard-code the array index path.
func assertInvalidField(t *testing.T, err error, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error naming %q, got nil", wantField)
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *platform.Error, got %T: %v", err, err)
	}
	if apiErr.Code != "invalid_argument" {
		t.Fatalf("expected invalid_argument, got %q (%v)", apiErr.Code, err)
	}
	field, _ := apiErr.Details["field"].(string)
	if field != wantField && !strings.HasSuffix(field, "."+wantField) {
		t.Fatalf("error field = %q, want %q (message: %s)", field, wantField, apiErr.Message)
	}
}

// ---------- the v0.2.4-B case contract ----------

func TestValidateProposalAcceptsNaturalLanguageContract(t *testing.T) {
	if err := ValidateProposal(validProposal(), validProposalContext()); err != nil {
		t.Fatalf("ValidateProposal: %v", err)
	}
}

func TestValidateProposalRequiresSutDescription(t *testing.T) {
	for _, value := range []string{"", "   "} {
		in := validProposal()
		in.Cases[0].SutDescription = value
		assertInvalidField(t, ValidateProposal(in, validProposalContext()), "sut_description")
	}
}

func TestValidateProposalRejectsOverlongSutDescription(t *testing.T) {
	in := validProposal()
	in.Cases[0].SutDescription = strings.Repeat("系", CaseSutDescriptionMax+1)
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "sut_description")
}

func TestValidateProposalAcceptsSutDescriptionAtLimit(t *testing.T) {
	in := validProposal()
	in.Cases[0].SutDescription = strings.Repeat("系", CaseSutDescriptionMax)
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("sut_description at the %d limit should be valid: %v", CaseSutDescriptionMax, err)
	}
}

// TestValidateProposalRequiresPreconditions pins §5.2: preconditions is the
// scenario-level Given and is required.
func TestValidateProposalRequiresPreconditions(t *testing.T) {
	for _, value := range []string{"", "   ", "\n\t "} {
		in := validProposal()
		in.Cases[0].Preconditions = value
		assertInvalidField(t, ValidateProposal(in, validProposalContext()), "preconditions")
	}
}

// TestValidateProposalAcceptsExplicitNoPreconditions pins the §5.2 escape hatch:
// an explicit "无" is a stated precondition (there is nothing to set up), so it
// satisfies the requirement where a blank does not.
func TestValidateProposalAcceptsExplicitNoPreconditions(t *testing.T) {
	in := validProposal()
	in.Cases[0].Preconditions = "无"
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf(`preconditions "无" should be valid: %v`, err)
	}
}

// ---------- covered_acs linkage ----------

func TestValidateProposalRequiresCoveredACs(t *testing.T) {
	for _, value := range [][]string{nil, {}, {"", "  "}} {
		in := validProposal()
		in.Cases[0].CoveredACs = value
		assertInvalidField(t, ValidateProposal(in, validProposalContext()), "covered_acs")
	}
}

// TestValidateProposalRejectsACOutsideRequirementSet is the linkage rule: an id
// the requirement does not have is a 400 naming the field, never a silently
// dropped element.
func TestValidateProposalRejectsACOutsideRequirementSet(t *testing.T) {
	in := validProposal()
	in.Cases[0].CoveredACs = []string{"AC-1", "AC-99"}
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "covered_acs")
}

// TestValidateProposalRejectsDanglingACAfterRenumber pins the merge/revise
// consequence the controller ruling asked for: when a requirement revision or a
// version-side Merge re-numbers criteria, a case still pointing at the retired id
// fails loudly and the message says why, so a dangling link can never pass as
// coverage.
func TestValidateProposalRejectsDanglingACAfterRenumber(t *testing.T) {
	in := validProposal()
	in.Cases[0].CoveredACs = []string{"AC-3"}
	ctx := validProposalContext()
	// The requirement moved on: AC-3 was retired and the set is now AC-1/AC-2.
	ctx.RequirementACIDs = []string{"AC-1", "AC-2"}
	err := ValidateProposal(in, ctx)
	assertInvalidField(t, err, "covered_acs")
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *platform.Error, got %T", err)
	}
	for _, want := range []string{"AC-3", "renumbered by a merge"} {
		if !strings.Contains(apiErr.Error(), want) {
			t.Fatalf("error %q should name the dangling id and the merge cause (missing %q)", apiErr.Error(), want)
		}
	}
}

// TestValidateProposalRejectsEveryACWhenRequirementHasNone fails closed: a
// requirement with no structured criteria has nothing to cover, so no case can
// claim coverage of it. Vacuously accepting one would hide an unclarified
// requirement behind a green coverage report.
func TestValidateProposalRejectsEveryACWhenRequirementHasNone(t *testing.T) {
	in := validProposal()
	ctx := validProposalContext()
	ctx.RequirementACIDs = nil
	assertInvalidField(t, ValidateProposal(in, ctx), "covered_acs")
}

// TestValidateProposalAcceptsUnsortedAndDuplicateCoveredACs pins that
// covered_acs is a set: submission order and repetition are normalized, not
// rejected, so what is validated matches what the content hash canonicalizes.
func TestValidateProposalAcceptsUnsortedAndDuplicateCoveredACs(t *testing.T) {
	in := validProposal()
	in.Cases[0].CoveredACs = []string{" AC-10 ", "AC-1", "AC-10", "AC-1"}
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("unsorted/duplicate covered_acs should be valid: %v", err)
	}
	if got := NormalizeACIDSet(in.Cases[0].CoveredACs); strings.Join(got, ",") != "AC-1,AC-10" {
		t.Fatalf("NormalizeACIDSet = %v, want [AC-1 AC-10] (sorted unique, trimmed)", got)
	}
}

// TestValidateProposalAcceptsTwoDigitACID guards the ordering/parse trap: AC-10
// is a valid id and must not be confused with AC-1.
func TestValidateProposalAcceptsTwoDigitACID(t *testing.T) {
	in := validProposal()
	in.Cases[0].CoveredACs = []string{"AC-10"}
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("AC-10 should link to the requirement's AC-10: %v", err)
	}
}

// ---------- db assertion: presence only, no ownership ----------

func TestValidateProposalRejectsDBAssertionWithoutSUTID(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions = append(in.Cases[0].Assertions, AssertionProposal{
		StepSeq: 1, Kind: "db", Target: "count", Expected: "1",
	})
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "sut_id")
}

// TestValidateProposalAcceptsDBAssertionWithAnySUTID pins Lead ruling 6: the db
// rule is a PRESENCE check only. The retired ownership group used to require the
// database to belong to the step/case SUT; any positive id is now accepted,
// because design has no resource scope left to check it against.
func TestValidateProposalAcceptsDBAssertionWithAnySUTID(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions = append(in.Cases[0].Assertions, AssertionProposal{
		StepSeq: 1, Kind: "db", Target: "orders 行数", Expected: "1", SUTID: int64Ptr(424242),
	})
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("a db assertion naming any SUT should be valid: %v", err)
	}
}

func TestValidateProposalRejectsNonPositiveDBSUTID(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions = append(in.Cases[0].Assertions, AssertionProposal{
		StepSeq: 1, Kind: "db", Target: "count", Expected: "1", SUTID: int64Ptr(0),
	})
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "sut_id")
}

// TestValidateProposalIgnoresSUTIDForNonDBKinds pins that sut_id is only
// required by kind=db; a ui/api/text assertion may carry one or not.
func TestValidateProposalIgnoresSUTIDForNonDBKinds(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions = []AssertionProposal{
		{StepSeq: 1, Kind: "ui", Target: "欢迎文案", Expected: "欢迎回来"},
		{StepSeq: 2, Kind: "text", Target: "备注", Expected: "已保存", SUTID: int64Ptr(7)},
	}
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("ValidateProposal: %v", err)
	}
}

// ---------- the RETAINED set (§5.2 "删 SUT 归属组后保留集") ----------
//
// These are the rules that must survive the ownership deletion. Each one is
// pinned individually so a future refactor cannot quietly drop half the gate
// along with the resource rules.

func TestValidateProposalRetainedGraphHashCAS(t *testing.T) {
	in := validProposal()
	in.BaseGraphHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	err := ValidateProposal(in, validProposalContext())
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("expected state_conflict, got %v", err)
	}

	in = validProposal()
	in.BaseGraphHash = "not-a-hash"
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "base_graph_hash")

	in = validProposal()
	in.BaseGraphHash = validProposalContext().CurrentHash
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("matching base hash should be valid: %v", err)
	}
}

func TestValidateProposalRetainedScopeMismatch(t *testing.T) {
	in := validProposal()
	in.RequirementVersionID = 9999
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "requirement_version_id")
}

func TestValidateProposalRetainedLimits(t *testing.T) {
	ctx := validProposalContext()
	ctx.Limits = DefaultLimits

	tooManyScenarios := validProposal()
	for i := 2; i <= DefaultLimits.MaxScenariosPerProposal+1; i++ {
		tooManyScenarios.Scenarios = append(tooManyScenarios.Scenarios, ScenarioProposal{
			Ref: "s" + string(rune('0'+i)), Name: "场景" + string(rune('0'+i)),
		})
	}
	assertInvalidArgument(t, ValidateProposal(tooManyScenarios, ctx))

	tooManyCases := validProposal()
	for i := 2; i <= DefaultLimits.MaxCasesPerProposal+1; i++ {
		duplicate := validProposal().Cases[0]
		duplicate.Ref = "c" + string(rune('0'+i))
		duplicate.Title = "用例" + string(rune('0'+i))
		tooManyCases.Cases = append(tooManyCases.Cases, duplicate)
	}
	assertInvalidArgument(t, ValidateProposal(tooManyCases, ctx))

	// The accumulated per-requirement caps still fire from the context counts.
	overScenarioCap := validProposalContext()
	overScenarioCap.Limits = DefaultLimits
	overScenarioCap.ExistingScenarioCount = DefaultLimits.MaxScenariosPerRequirement
	assertInvalidArgument(t, ValidateProposal(validProposal(), overScenarioCap))

	overCaseCap := validProposalContext()
	overCaseCap.Limits = DefaultLimits
	overCaseCap.ExistingCaseCount = DefaultLimits.MaxCasesPerRequirement
	assertInvalidArgument(t, ValidateProposal(validProposal(), overCaseCap))
}

func TestValidateProposalRetainedEmptyBatches(t *testing.T) {
	in := validProposal()
	in.Cases = nil
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "cases")

	in = validProposal()
	in.Scenarios = nil
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "scenarios")
}

func TestValidateProposalRetainedNaturalKeyDedup(t *testing.T) {
	in := validProposal()
	in.Scenarios = append(in.Scenarios, ScenarioProposal{Ref: "s2", Name: "正常登录"})
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "scenarios[1].name")

	in = validProposal()
	duplicate := in.Cases[0]
	duplicate.Ref = "c2"
	in.Cases = append(in.Cases, duplicate)
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "cases[1].title")

	in = validProposal()
	in.Scenarios[0].Ref = ""
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "ref")

	in = validProposal()
	in.Scenarios = append(in.Scenarios, in.Scenarios[0])
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "scenarios[1].ref")

	in = validProposal()
	in.Cases[0].Ref = ""
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "ref")

	in = validProposal()
	in.Cases = append(in.Cases, in.Cases[0])
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "cases[1].ref")

	in = validProposal()
	in.Cases[0].ScenarioRef = "missing"
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "scenario_ref")
}

func TestValidateProposalRetainedContiguousStepSeq(t *testing.T) {
	in := validProposal()
	in.Cases[0].Steps[1].Seq = 3
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "seq")

	in = validProposal()
	in.Cases[0].Steps = in.Cases[0].Steps[:1]
	in.Cases[0].Assertions = in.Cases[0].Assertions[:1]
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("single step case should be valid: %v", err)
	}

	in = validProposal()
	in.Cases[0].Steps = nil
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "steps")
}

func TestValidateProposalRetainedAssertionStepBackReference(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions[0].StepSeq = 9
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "step_seq")
}

func TestValidateProposalRetainedKindEnum(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions[0].Kind = "visual"
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "kind")
}

func TestValidateProposalRetainedFieldLengths(t *testing.T) {
	in := validProposal()
	in.Cases[0].Title = ""
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "title")

	in = validProposal()
	in.Cases[0].Steps[0].Action = ""
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "action")

	in = validProposal()
	in.Cases[0].Steps[0].Expected = ""
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "expected")

	in = validProposal()
	in.Cases[0].Assertions[0].Target = ""
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "target")

	in = validProposal()
	in.Cases[0].Steps[0].AccountRole = strings.Repeat("r", AccountRoleMax+1)
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "account_role")

	in = validProposal()
	in.Cases[0].Steps[0].TargetDescription = strings.Repeat("目", StepActionMax+1)
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "target_description")
}

// TestValidateProposalRetainedRequirementSourceRevisionBinding pins the one
// source rule that survived: a requirement source must reference the requirement
// version being designed, which is what makes stale propagation work.
func TestValidateProposalRetainedRequirementSourceRevisionBinding(t *testing.T) {
	in := validProposal()
	in.Cases[0].Sources[0].RefID = "9999"
	assertInvalidField(t, ValidateProposal(in, validProposalContext()), "ref_id")
}

// TestValidateProposalRetainsPageAndAPISourceKinds pins that dropping the
// ownership lookup did NOT drop the kinds: page/api sources are still accepted as
// provenance (§5.2 "page/api/knowledge 可选引用校验"), they are simply no longer
// resolved against a version scope.
func TestValidateProposalRetainsPageAndAPISourceKinds(t *testing.T) {
	in := validProposal()
	in.Cases[0].Sources = append(in.Cases[0].Sources,
		SourceProposal{Kind: "page", RefID: "6001", Path: "discovery_delta"},
		SourceProposal{Kind: "api", RefID: "4001", Path: "discovery_delta"},
		SourceProposal{Kind: "knowledge", RefID: "77", Path: "exploration_gap"},
	)
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("page/api/knowledge sources should still be valid: %v", err)
	}
}

func TestValidateProposalRetainedSourceDedup(t *testing.T) {
	in := validProposal()
	in.Cases[0].Sources = append(in.Cases[0].Sources, in.Cases[0].Sources[0])
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRetainedPerCaseChildLimits(t *testing.T) {
	ctx := validProposalContext()
	ctx.Limits = DefaultLimits

	tooManySteps := validProposal()
	steps := make([]StepProposal, 0, DefaultLimits.MaxStepsPerCase+1)
	for i := 1; i <= DefaultLimits.MaxStepsPerCase+1; i++ {
		steps = append(steps, StepProposal{Seq: i, Action: "动作", Expected: "结果"})
	}
	tooManySteps.Cases[0].Steps = steps
	tooManySteps.Cases[0].Assertions = []AssertionProposal{{StepSeq: 1, Kind: "ui", Target: "t", Expected: "e"}}
	assertInvalidArgument(t, ValidateProposal(tooManySteps, ctx))

	tooManyAssertions := validProposal()
	assertions := make([]AssertionProposal, 0, DefaultLimits.MaxAssertionsPerCase+1)
	for i := 0; i <= DefaultLimits.MaxAssertionsPerCase; i++ {
		assertions = append(assertions, AssertionProposal{StepSeq: 1, Kind: "ui", Target: "t", Expected: "e"})
	}
	tooManyAssertions.Cases[0].Assertions = assertions
	assertInvalidArgument(t, ValidateProposal(tooManyAssertions, ctx))

	tooManySources := validProposal()
	sources := make([]SourceProposal, 0, DefaultLimits.MaxSourcesPerCase+1)
	for i := 0; i <= DefaultLimits.MaxSourcesPerCase; i++ {
		sources = append(sources, SourceProposal{Kind: "knowledge", RefID: "k" + string(rune('a'+i%26)) + string(rune('0'+i/26))})
	}
	tooManySources.Cases[0].Sources = sources
	assertInvalidArgument(t, ValidateProposal(tooManySources, ctx))
}

// ---------- the RETIRED set ----------

// TestValidateProposalNoLongerValidatesResourceOwnership is the negative half of
// the contract change: the retired ownership group must stay retired. A proposal
// that would once have been rejected for pointing at an unbound resource is now
// accepted, because there is no resource scope in design to point at. If this
// test ever fails, someone re-introduced the coupling v0.2.4-B removes.
func TestValidateProposalNoLongerValidatesResourceOwnership(t *testing.T) {
	ctx := validProposalContext()
	// The context carries no resource maps at all any more; the only thing it can
	// still say is which AC ids exist.
	if err := ValidateProposal(validProposal(), ctx); err != nil {
		t.Fatalf("ValidateProposal: %v", err)
	}

	// A db assertion naming a SUT nothing has ever heard of is fine (presence
	// only, ruling 6).
	in := validProposal()
	in.Cases[0].Assertions = append(in.Cases[0].Assertions, AssertionProposal{
		StepSeq: 1, Kind: "db", Target: "count", Expected: "1", SUTID: int64Ptr(1),
	})
	if err := ValidateProposal(in, ctx); err != nil {
		t.Fatalf("ownership of a db assertion's sut_id must not be checked: %v", err)
	}

	// page/api sources keep their kind but are no longer resolved.
	in = validProposal()
	in.Cases[0].Sources = append(in.Cases[0].Sources,
		SourceProposal{Kind: "page", RefID: "12345", Path: "discovery_delta"})
	if err := ValidateProposal(in, ctx); err != nil {
		t.Fatalf("page source ownership must not be checked: %v", err)
	}
}

func TestValidateConfirmationScope(t *testing.T) {
	for _, scope := range []string{ConfirmScopeCase, ConfirmScopeScenario, ConfirmScopeRequirement} {
		if err := ValidateConfirmationScope(scope); err != nil {
			t.Fatalf("ValidateConfirmationScope(%q): %v", scope, err)
		}
	}
	if err := ValidateConfirmationScope("batch"); err == nil {
		t.Fatal("expected error for unsupported scope")
	}
}

func TestStatusConstants(t *testing.T) {
	want := map[string]string{
		CaseStatusDraft:      "draft",
		CaseStatusConfirmed:  "confirmed",
		CaseStatusStale:      "stale",
		CaseStatusSuperseded: "superseded",
	}
	for got, expected := range want {
		if got != expected {
			t.Fatalf("status constant %q != %q", got, expected)
		}
	}
}
