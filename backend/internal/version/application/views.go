// Package versionapp implements the version use cases: create with multi-SUT
// bindings, detail aggregation through the resource catalog, cursor listing,
// title/auto-design updates and the resource delete-guard usage queries.
package versionapp

import (
	"time"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// VersionView is the detail read model returned by Create/Get/Update. Bindings
// carry nested resource summaries; passwords are never exposed by the resource
// views. ClosedAt/CloseReason carry the P4 close projection (nil/empty while
// the version is not completed; cleared again by a reopen).
type VersionView struct {
	ID                        int64
	Title                     string
	Status                    string
	AutoDesignNewRequirements bool
	Bindings                  []BindingView
	RequirementCounts         version.RequirementCounts
	ConversationCount         int
	PendingDesignCount        int
	CaseCounts                CaseCounts
	ClosedAt                  *time.Time
	CloseReason               string
	CreatedBy                 int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// CaseCounts is the version projection of current (non-superseded) case
// versions by status.
type CaseCounts struct {
	Draft     int
	Confirmed int
	Stale     int
}

// BindingView is one SUT binding with its selected resources in input order.
// CodeBaselineCommit is empty while the bound repository's HEAD has not been
// resolved (P5 §5.5).
type BindingView struct {
	ID                 int64
	CodeBaselineCommit string
	SUT                resourceapp.SUTView
	Environment        resourceapp.EnvironmentView
	Repository         *resourceapp.CodeRepositoryView
	Pages              []resourceapp.PageView
	APIs               []resourceapp.APIView
	Databases          []resourceapp.DatabaseView
	Accounts           []resourceapp.AccountView
}

// VersionSummary is the list read model.
type VersionSummary struct {
	ID        int64
	Title     string
	Status    string
	SUTCount  int
	CreatedAt time.Time
	UpdatedAt time.Time
}
