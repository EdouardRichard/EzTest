package version

import "github.com/EdouardRichard/EzTest/backend/internal/platform"

// Scope is the page/API subset selected for one SUT binding. The JSON shape is
// persisted verbatim in version_sut.scope.
type Scope struct {
	Pages []int64 `json:"pages"`
	APIs  []int64 `json:"apis"`
}

// NewScope returns a scope with non-nil slices so the stored JSON is always
// {"pages":[],"apis":[]} when empty.
func NewScope(pages, apis []int64) Scope {
	if pages == nil {
		pages = []int64{}
	}
	if apis == nil {
		apis = []int64{}
	}
	return Scope{Pages: pages, APIs: apis}
}

// AccountBinding is one selected test account and its optional role label.
type AccountBinding struct {
	AccountID int64
	Role      string
}

// Binding is one SUT environment bound into a version.
type Binding struct {
	ID               int64
	VersionID        int64
	SUTID            int64
	EnvironmentID    int64
	CodeRepositoryID *int64
	Scope            Scope
	DatabaseIDs      []int64
	Accounts         []AccountBinding
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
	if b.CodeRepositoryID != nil && *b.CodeRepositoryID <= 0 {
		return platform.Invalid("code_repository_id", "must be a positive decimal ID")
	}
	for _, pageID := range b.Scope.Pages {
		if pageID <= 0 {
			return platform.Invalid("page_ids", "must contain positive decimal IDs")
		}
	}
	for _, apiID := range b.Scope.APIs {
		if apiID <= 0 {
			return platform.Invalid("api_ids", "must contain positive decimal IDs")
		}
	}
	for _, databaseID := range b.DatabaseIDs {
		if databaseID <= 0 {
			return platform.Invalid("database_ids", "must contain positive decimal IDs")
		}
	}
	for _, account := range b.Accounts {
		if account.AccountID <= 0 {
			return platform.Invalid("accounts", "account_id must be a positive decimal ID")
		}
	}
	return nil
}
