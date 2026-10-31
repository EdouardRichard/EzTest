package design

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func int64Ptr(value int64) *int64 { return &value }

func validProposalContext() ProposalContext {
	return ProposalContext{
		VersionID:            1001,
		RequirementID:        3001,
		RequirementVersionID: 3002,
		CurrentHash:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AllowedVersionSUTs:   []int64{9001, 9002},
		PageToSUT:            map[int64]int64{6001: 9001, 6002: 9002},
		APIToSUT:             map[int64]int64{4001: 9002},
		DBTOSUT:              map[int64]int64{8001: 9002},
	}
}

func validProposal() Proposal {
	return Proposal{
		RequirementID:        3001,
		RequirementVersionID: 3002,
		Scenarios: []ScenarioProposal{
			{
				Ref:           "s1",
				Name:          "正常登录",
				Description:   "正常路径",
				VersionSUTIDs: []int64{9001, 9002},
				PageIDs:       []int64{6001},
				APIIDs:        []int64{4001},
			},
		},
		Cases: []CaseProposal{
			{
				Ref:                 "c1",
				ScenarioRef:         "s1",
				Title:               "账号密码登录成功",
				Description:         "使用有效账号登录",
				Preconditions:       "已有可用账号",
				TestData:            json.RawMessage(`{"username":"admin","password":"secret"}`),
				Priority:            "P1",
				DefaultVersionSUTID: int64Ptr(9001),
				VersionSUTIDs:       []int64{9001, 9002},
				PageIDs:             []int64{6001, 6002},
				APIIDs:              []int64{4001},
				Steps: []StepProposal{
					{
						Seq:          1,
						Action:       "打开登录页",
						Expected:     "显示登录表单",
						TargetPageID: int64Ptr(6001),
						AccountRole:  "admin",
						Data:         json.RawMessage(`{}`),
					},
					{
						Seq:                2,
						Action:             "提交账号密码",
						Expected:           "接口返回成功",
						TargetVersionSUTID: int64Ptr(9002),
						TargetAPIID:        int64Ptr(4001),
						AccountRole:        "admin",
						Data:               json.RawMessage(`{"username":"admin"}`),
					},
				},
				Assertions: []AssertionProposal{
					{StepSeq: 1, Kind: "ui", Target: "欢迎文案", Expected: "欢迎回来", TargetVersionSUTID: int64Ptr(9001)},
					{StepSeq: 2, Kind: "api", Target: "POST /login", Expected: "200", TargetVersionSUTID: int64Ptr(9002)},
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

func TestValidateProposalAcceptsCrossSUTSteps(t *testing.T) {
	in := validProposal()
	ctx := validProposalContext()
	if err := ValidateProposal(in, ctx); err != nil {
		t.Fatalf("ValidateProposal: %v", err)
	}
}

func TestValidateProposalRejectsUnknownVersionSUT(t *testing.T) {
	in := validProposal()
	in.Cases[0].VersionSUTIDs = []int64{9001, 9999}
	in.Cases[0].DefaultVersionSUTID = int64Ptr(9999)
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsUnknownStepTargetSUT(t *testing.T) {
	in := validProposal()
	in.Cases[0].Steps[1].TargetVersionSUTID = int64Ptr(9999)
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsNonContiguousSteps(t *testing.T) {
	in := validProposal()
	in.Cases[0].Steps[1].Seq = 3
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))

	in = validProposal()
	in.Cases[0].Steps = in.Cases[0].Steps[:1]
	in.Cases[0].Assertions = in.Cases[0].Assertions[:1]
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("single step case should be valid: %v", err)
	}
}

func TestValidateProposalRejectsBadAssertionStep(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions[0].StepSeq = 9
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsInheritedStepTargetMismatch(t *testing.T) {
	in := validProposal()
	// Step 1 has no explicit target, so it inherits default 9001; page 6002
	// belongs to version_sut 9002.
	in.Cases[0].Steps[0].TargetPageID = int64Ptr(6002)
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsAssertionOutsideStepTarget(t *testing.T) {
	in := validProposal()
	// Step 1 resolves to default 9001; its assertion API 4001 belongs to 9002.
	in.Cases[0].Assertions[0].TargetAPIID = int64Ptr(4001)
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsCrossSUTPageTarget(t *testing.T) {
	in := validProposal()
	// Page 6002 belongs to version_sut 9002, but the step declares 9001.
	in.Cases[0].Steps[0].TargetVersionSUTID = int64Ptr(9001)
	in.Cases[0].Steps[0].TargetPageID = int64Ptr(6002)
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsUnknownPageTarget(t *testing.T) {
	in := validProposal()
	in.Cases[0].PageIDs = []int64{12345}
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsDBAssertionWithoutDatabase(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions = append(in.Cases[0].Assertions, AssertionProposal{
		StepSeq: 1, Kind: "db", Target: "count", Expected: "1",
	})
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsDatabaseOutsideTargetSUT(t *testing.T) {
	in := validProposal()
	in.Cases[0].Assertions = append(in.Cases[0].Assertions, AssertionProposal{
		StepSeq: 1, Kind: "db", Target: "count", Expected: "1",
		TargetVersionSUTID: int64Ptr(9001), DatabaseID: int64Ptr(8001),
	})
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalNaturalKeyRejectsDuplicateScenarioNames(t *testing.T) {
	in := validProposal()
	in.Scenarios = append(in.Scenarios, ScenarioProposal{Ref: "s2", Name: "正常登录"})
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalNaturalKeyRejectsDuplicateCaseTitlesInScenario(t *testing.T) {
	in := validProposal()
	duplicate := in.Cases[0]
	duplicate.Ref = "c2"
	in.Cases = append(in.Cases, duplicate)
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsEmptyCaseList(t *testing.T) {
	in := validProposal()
	in.Cases = nil
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsMissingStep(t *testing.T) {
	in := validProposal()
	in.Cases[0].Steps = nil
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsDefaultSUTOutsideCaseSUTs(t *testing.T) {
	in := validProposal()
	in.Cases[0].DefaultVersionSUTID = int64Ptr(9002)
	in.Cases[0].VersionSUTIDs = []int64{9001}
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsUnknownScenarioRef(t *testing.T) {
	in := validProposal()
	in.Cases[0].ScenarioRef = "missing"
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsBaseHashMismatch(t *testing.T) {
	in := validProposal()
	in.BaseGraphHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	err := ValidateProposal(in, validProposalContext())
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "state_conflict" {
		t.Fatalf("expected state_conflict, got %v", err)
	}
}

func TestValidateProposalAcceptsMatchingBaseHash(t *testing.T) {
	in := validProposal()
	in.BaseGraphHash = validProposalContext().CurrentHash
	if err := ValidateProposal(in, validProposalContext()); err != nil {
		t.Fatalf("ValidateProposal: %v", err)
	}
}

func TestValidateProposalRejectsScopeMismatch(t *testing.T) {
	in := validProposal()
	in.RequirementVersionID = 9999
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestProposalLimits(t *testing.T) {
	ctx := validProposalContext()

	tooManyScenarios := validProposal()
	for i := 2; i <= DefaultLimits.MaxScenariosPerProposal+1; i++ {
		tooManyScenarios.Scenarios = append(tooManyScenarios.Scenarios, ScenarioProposal{
			Ref: "s" + string(rune('0'+i)), Name: "场景" + string(rune('0'+i)),
		})
	}
	ctx.Limits = DefaultLimits
	assertInvalidArgument(t, ValidateProposal(tooManyScenarios, ctx))

	tooManyCases := validProposal()
	for i := 2; i <= DefaultLimits.MaxCasesPerProposal+1; i++ {
		duplicate := validProposal().Cases[0]
		duplicate.Ref = "c" + string(rune('0'+i))
		duplicate.Title = "用例" + string(rune('0'+i))
		tooManyCases.Cases = append(tooManyCases.Cases, duplicate)
	}
	assertInvalidArgument(t, ValidateProposal(tooManyCases, ctx))
}

func TestValidateProposalRejectsRequirementSourceMismatch(t *testing.T) {
	in := validProposal()
	in.Cases[0].Sources[0].RefID = "9999"
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
}

func TestValidateProposalRejectsDuplicateSources(t *testing.T) {
	in := validProposal()
	in.Cases[0].Sources = append(in.Cases[0].Sources, in.Cases[0].Sources[0])
	assertInvalidArgument(t, ValidateProposal(in, validProposalContext()))
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
