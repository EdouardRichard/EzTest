package version

import (
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// AccountBinding is one test account enabled for the version (v0.22 DB-14). The
// account itself is a row of the global test-account pool; roles live on the
// account, not on the binding.
//
// v0.2.4-B removed the rest of the derived binding chain (version_sut,
// version_sut_resource, version_repository_override and the scope_exclude
// document): the environment a version tests is now version.environment_id, its
// SUT catalog and code pool are derived from that environment through the
// resource catalog, and every approved asset of those SUTs is in scope.
type AccountBinding struct {
	AccountID int64
}

// ValidateAccounts checks a version account selection: every ID is positive and
// no account is bound twice.
func ValidateAccounts(accounts []AccountBinding) error {
	seen := make(map[int64]struct{}, len(accounts))
	for _, account := range accounts {
		if account.AccountID <= 0 {
			return platform.Invalid("accounts", "account_id must be a positive decimal ID")
		}
		if _, duplicate := seen[account.AccountID]; duplicate {
			return platform.Invalid("accounts", "account is bound more than once")
		}
		seen[account.AccountID] = struct{}{}
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
