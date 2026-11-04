package version

import (
	"regexp"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ScopeExclude is the environment-owned resource subset excluded from one
// version binding (v0.22 C-1/DB-11). The JSON shape is persisted verbatim in
// version_sut.scope_exclude; empty lists mean the whole environment is in
// scope.
type ScopeExclude struct {
	Pages     []int64 `json:"pages"`
	APIs      []int64 `json:"apis"`
	Databases []int64 `json:"databases"`
}

// NewScopeExclude returns a scope with non-nil slices so the stored JSON is
// always {"pages":[],"apis":[],"databases":[]} when empty.
func NewScopeExclude(pages, apis, databases []int64) ScopeExclude {
	if pages == nil {
		pages = []int64{}
	}
	if apis == nil {
		apis = []int64{}
	}
	if databases == nil {
		databases = []int64{}
	}
	return ScopeExclude{Pages: pages, APIs: apis, Databases: databases}
}

// Validate checks that every excluded ID is a positive resource ID. Ownership
// against the bound environment is validated by the application service because
// it needs the resource catalog.
func (s ScopeExclude) Validate() error {
	if err := positiveIDs("scope_exclude.pages", s.Pages); err != nil {
		return err
	}
	if err := positiveIDs("scope_exclude.apis", s.APIs); err != nil {
		return err
	}
	return positiveIDs("scope_exclude.databases", s.Databases)
}

// RepositoryOverride is one version-level repository include/exclude on top of
// the SUT-inherited repositories (v0.22 C-2/DB-13).
type RepositoryOverride struct {
	RepoID   int64
	Included bool
}

// AccountBinding is one test account enabled for the version (DB-14). The
// account itself is a row of the global test-account pool; roles live on the
// account, not on the binding.
type AccountBinding struct {
	AccountID int64
}

// Binding is one SUT environment bound into a version. v0.22 derives the SUT
// from the environment, so a version has exactly one binding and no
// single-repository column: repositories are inherited from the SUT and adjusted
// through RepositoryOverrides.
type Binding struct {
	ID            int64
	VersionID     int64
	SUTID         int64
	EnvironmentID int64
	// CodeBaselineCommit is the resolved commit SHA of the deterministic baseline
	// repository (the lowest effective repository ID) at binding time (P5 §5.5).
	// Empty means "not resolved yet"; a valid value is 40 lowercase hex
	// characters.
	CodeBaselineCommit  string
	ScopeExclude        ScopeExclude
	RepositoryOverrides []RepositoryOverride
	Accounts            []AccountBinding
}

// codeBaselineCommitPattern matches the lowercase 40-hex commit SHA stored in
// version_sut.code_baseline_commit (spec §3.7).
var codeBaselineCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// IsValidCodeBaselineCommit reports whether commit is empty (unresolved) or a
// lowercase 40-hex Git commit SHA.
func IsValidCodeBaselineCommit(commit string) bool {
	return commit == "" || codeBaselineCommitPattern.MatchString(commit)
}

// Validate checks the identifiers of one binding. Cross-resource ownership and
// uniqueness are validated by the application service because they need the
// resource catalog.
func (b Binding) Validate() error {
	if b.SUTID <= 0 {
		return platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	if b.EnvironmentID <= 0 {
		return platform.Invalid("environment_id", "must be a positive decimal ID")
	}
	if !IsValidCodeBaselineCommit(b.CodeBaselineCommit) {
		return platform.Invalid("code_baseline_commit", "must be empty or a lowercase 40-hex commit SHA")
	}
	if err := b.ScopeExclude.Validate(); err != nil {
		return err
	}
	seenRepositories := make(map[int64]struct{}, len(b.RepositoryOverrides))
	for _, override := range b.RepositoryOverrides {
		if override.RepoID <= 0 {
			return platform.Invalid("repository_overrides", "repository_id must be a positive decimal ID")
		}
		if _, duplicate := seenRepositories[override.RepoID]; duplicate {
			return platform.Invalid("repository_overrides", "repository is overridden more than once")
		}
		seenRepositories[override.RepoID] = struct{}{}
	}
	seenAccounts := make(map[int64]struct{}, len(b.Accounts))
	for _, account := range b.Accounts {
		if account.AccountID <= 0 {
			return platform.Invalid("accounts", "account_id must be a positive decimal ID")
		}
		if _, duplicate := seenAccounts[account.AccountID]; duplicate {
			return platform.Invalid("accounts", "account is bound more than once")
		}
		seenAccounts[account.AccountID] = struct{}{}
	}
	return nil
}

// BindingEditable reports whether version binding changes are allowed at stage
// (v0.22 R-C4-2/C §4.4). The lifecycle stages up to and including case_review
// are editable; executing/reporting/completed are read-only. Suspension stages
// are resolved by the application service from the recorded suspended_from
// stage, so they are not editable on their own.
func BindingEditable(stage Stage) bool {
	switch stage {
	case StageCreated, StageClarifying, StageRequirementReview, StageDesigning, StageCaseReview:
		return true
	default:
		return false
	}
}

func positiveIDs(field string, ids []int64) error {
	for _, id := range ids {
		if id <= 0 {
			return platform.Invalid(field, "must contain positive decimal IDs")
		}
	}
	return nil
}
