package version_test

import (
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const validCodeBaselineCommit = "0123456789abcdef0123456789abcdef01234567"

func baselineBinding(commit string) version.Binding {
	return version.Binding{
		SUTID:              1001,
		EnvironmentID:      1101,
		CodeBaselineCommit: commit,
		ScopeExclude:       version.NewScopeExclude(nil, nil, nil),
	}
}

func TestBindingValidateAcceptsEmptyOrFullCommit(t *testing.T) {
	for _, commit := range []string{"", validCodeBaselineCommit} {
		if err := baselineBinding(commit).Validate(); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", commit, err)
		}
	}
}

func TestBindingValidateRejectsMalformedCommit(t *testing.T) {
	cases := map[string]string{
		"short":     "0123456789abcdef",
		"long":      validCodeBaselineCommit + "0",
		"uppercase": "0123456789ABCDEF0123456789ABCDEF01234567",
		"non-hex":   "0123456789abcdef0123456789abcdef0123456g",
	}
	for name, commit := range cases {
		t.Run(name, func(t *testing.T) {
			err := baselineBinding(commit).Validate()
			if err == nil {
				t.Fatalf("Validate(%q) accepted a malformed commit", commit)
			}
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want a platform error", err)
			}
			if field, _ := apiErr.Details["field"].(string); field != "code_baseline_commit" {
				t.Fatalf("field = %q, want code_baseline_commit", field)
			}
		})
	}
}

func TestNewScopeExcludeNormalizesEmptyLists(t *testing.T) {
	scope := version.NewScopeExclude(nil, nil, nil)
	if scope.Pages == nil || scope.APIs == nil || scope.Databases == nil {
		t.Fatalf("scope = %#v, want non-nil lists", scope)
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestScopeExcludeValidateRejectsNonPositiveIDs(t *testing.T) {
	cases := map[string]version.ScopeExclude{
		"pages":     version.NewScopeExclude([]int64{0}, nil, nil),
		"apis":      version.NewScopeExclude(nil, []int64{-1}, nil),
		"databases": version.NewScopeExclude(nil, nil, []int64{0}),
	}
	for name, scope := range cases {
		t.Run(name, func(t *testing.T) {
			err := scope.Validate()
			if err == nil {
				t.Fatal("Validate accepted a non-positive ID")
			}
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) || apiErr.Status != 400 {
				t.Fatalf("err = %v, want a 400 platform error", err)
			}
		})
	}
}

func TestBindingValidateRejectsInvalidOverrides(t *testing.T) {
	binding := baselineBinding("")
	binding.RepositoryOverrides = []version.RepositoryOverride{{RepoID: 0, Included: true}}
	if err := binding.Validate(); err == nil {
		t.Fatal("Validate accepted a zero repository override ID")
	}
	binding.RepositoryOverrides = nil
	binding.Accounts = []version.AccountBinding{{AccountID: 0}}
	if err := binding.Validate(); err == nil {
		t.Fatal("Validate accepted a zero account ID")
	}
}

func TestBindingEditableStageGate(t *testing.T) {
	editable := []version.Stage{
		version.StageCreated,
		version.StageClarifying,
		version.StageRequirementReview,
		version.StageDesigning,
		version.StageCaseReview,
	}
	for _, stage := range editable {
		if !version.BindingEditable(stage) {
			t.Fatalf("stage %q must allow binding edits", stage)
		}
	}
	readOnly := []version.Stage{
		version.StageExecuting,
		version.StageReporting,
		version.StageCompleted,
	}
	for _, stage := range readOnly {
		if version.BindingEditable(stage) {
			t.Fatalf("stage %q must be read-only", stage)
		}
	}
}
