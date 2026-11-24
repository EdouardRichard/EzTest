// Package versionapp implements the version use cases: environment-only
// creation, the environment binding overview/update, detail aggregation through
// the resource catalog, cursor listing, title/auto-design updates and the
// resource delete-guard usage queries.
//
// v0.2.4-B (SC-B2): a version binds an environment directly. The derived
// version_sut chain, its scope_exclude document and the version repository
// overrides are gone; the SUT catalog, the assets and the code pool are all
// derived from the bound environment through the resource catalog.
package versionapp

import (
	"context"
	"strings"
	"time"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// VersionView is the detail read model returned by Create/Get/Update. Binding
// carries the resolved environment catalog; passwords are never exposed by the
// resource views. ClosedAt/CloseReason carry the P4 close projection
// (nil/empty while the version is not completed; cleared again by a reopen).
type VersionView struct {
	ID                        int64
	Title                     string
	Description               string
	Goal                      string
	Status                    string
	AutoDesignNewRequirements bool
	EnvironmentID             int64
	Binding                   EnvironmentSUTCatalog
	RequirementCounts         version.RequirementCounts
	ConversationCount         int
	PendingDesignCount        int
	CaseCounts                CaseCounts
	ClosedAt                  *time.Time
	CloseReason               string
	// ClosureKind/Unverified carry the waiver projection (v0.2.4-G SC-G11):
	// normal/false for a full-predicate close, waived/true for an admin waiver
	// close; cleared again by a reopen. Users can view them (Q7=A).
	ClosureKind string
	Unverified  bool
	CreatedBy   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CaseCounts is the version projection of current (non-superseded) case
// versions by status.
type CaseCounts struct {
	Draft     int
	Confirmed int
	Stale     int
}

// BindingRepository is one repository of the environment code pool (v0.2.4-B
// GD-03): the union of the repositories associated with the environment's SUTs,
// de-duplicated by repository ID and ordered by repository ID. SUTID names the
// SUT whose association supplied the row, which is also the key the resolved
// code baseline is stored under.
type BindingRepository struct {
	Repository         resourceapp.CodeRepositoryView
	SUTID              int64
	Role               string
	CodeBaselineCommit string
}

// EnvironmentSUTCatalog is the resolved resource catalog of the environment a
// version binds. It is what BindingReader.ResolveEnvironment returns and what
// the execution, chain, codeintel and agent adapters consume in place of the
// removed version_sut chain.
//
// TWO asset projections live here and they are NOT interchangeable:
//
//   - Pages/APIs/Databases are the FLAT EFFECTIVE SCOPE of the version: the
//     approved pages, approved APIs and active target databases of every SUT of
//     the environment, in SUT order. This is what a version is allowed to test.
//   - Environment.SUTs[i].Pages/APIs/Databases are RAW catalog state (every
//     status) and exist so the binding counters can report proposed rows and so
//     the resource catalog UI keeps its full view. Reading them for scope leaks a
//     proposed page into the execution fingerprint and the design model's page
//     locators, and leaks a disabled database into credential resolution.
//
// Per-SUT effective scope is EffectiveSUTs; it is the union of the flat lists, so
// the two can never disagree. Repositories is the environment code pool, Accounts
// the version's own version_account subset, and CodeBaselineCommit the commit of
// the deterministic baseline repository (the lowest repository ID of the pool).
type EnvironmentSUTCatalog struct {
	VersionID          int64
	EnvironmentID      int64
	Environment        resourceapp.EnvironmentView
	Pages              []resourceapp.PageView
	APIs               []resourceapp.APIView
	Databases          []resourceapp.DatabaseView
	Accounts           []resourceapp.AccountView
	Repositories       []BindingRepository
	CodeBaselineCommit string
}

// EffectiveSUT is one SUT of the bound environment paired with its EFFECTIVE
// asset scope: the approved pages, approved APIs and active target databases that
// are inside the version's scope (v0.2.4-B review round 1, Important 1).
//
// It deliberately carries no raw catalog list, so a version-scope consumer that
// takes its assets from here cannot pick the wrong projection by accident. The
// identity fields are pre-parsed from the decimal catalog IDs; Intro is the
// catalog's identity prose (v0.2.4-F T12: the execution task context renders it
// so the executor can semantically match a case's sut_description); Family is
// the trimmed system key and is empty when the SUT has none.
type EffectiveSUT struct {
	SUTID     int64
	Name      string
	Intro     string
	BaseURL   string
	Family    string
	Pages     []resourceapp.PageView
	APIs      []resourceapp.APIView
	Databases []resourceapp.DatabaseView
}

// EffectiveSUTs returns one EffectiveSUT per APPROVED SUT of the bound
// environment, in catalog order. This — not Environment.SUTs[i].Pages/APIs/
// Databases — is the per-SUT projection every version-scope consumer must read.
// The flat Pages/APIs/Databases of the catalog are exactly the union of these
// lists (both come from resourceapp.SUTView.EffectiveAssets), so a per-SUT read
// and an environment-wide read of the same binding always agree.
//
// v0.2.4-B R-B4: a proposed or rejected SUT is skipped whole, gating on the single
// resourceapp.SUTView.IsApproved predicate so the trust boundary is not
// re-implemented here. The raw catalog (Environment.SUTs) still carries those
// SUTs so an admin can approve them.
func (c EnvironmentSUTCatalog) EffectiveSUTs() []EffectiveSUT {
	suts := make([]EffectiveSUT, 0, len(c.Environment.SUTs))
	for _, sut := range c.Environment.SUTs {
		if !sut.IsApproved() {
			continue
		}
		scope := sut.EffectiveAssets()
		family := ""
		if sut.Family != nil {
			family = strings.TrimSpace(*sut.Family)
		}
		suts = append(suts, EffectiveSUT{
			SUTID:     parseCatalogIDOrZero(sut.ID),
			Name:      sut.Name,
			Intro:     sut.Intro,
			BaseURL:   sut.BaseURL,
			Family:    family,
			Pages:     scope.Pages,
			APIs:      scope.APIs,
			Databases: scope.Databases,
		})
	}
	return suts
}

// BindingReader resolves the environment binding of one version. It is the port
// the downstream contexts consume after the version_sut chain was removed;
// *Service implements it.
type BindingReader interface {
	ResolveEnvironment(ctx context.Context, versionID int64) (EnvironmentSUTCatalog, error)
}

// BindingOverview is the API-16 read model (R-C4-1): the resolved environment
// catalog plus the version lifecycle gate, the per-asset counters and the
// deterministic snapshot reference.
type BindingOverview struct {
	EnvironmentSUTCatalog
	Stage    version.Stage
	Editable bool
	Counts   BindingCounts
	Snapshot BindingSnapshot
}

// BindingResourceCounts is the approved/proposed projection of one asset kind of
// the bound environment. Exclusions disappeared with the version_sut chain, so
// every approved asset of the environment's SUTs is in scope and Approved is
// also the included count.
type BindingResourceCounts struct {
	Approved int
	Proposed int
}

// BindingCounts groups the page/API/database counters of the binding overview.
type BindingCounts struct {
	Pages     BindingResourceCounts
	APIs      BindingResourceCounts
	Databases BindingResourceCounts
}

// BindingSnapshot is the R-C4-1 snapshot projection of one binding. W0 has no
// frozen wave snapshot in the version context (R-C5/API-19 is execution-owned:
// run_wave.scope_snapshot), so FrozenWaveID is nil and ScopeVersion is a
// deterministic digest of the current effective scope (environment, its SUTs'
// pages/APIs/databases, the version accounts, the code pool and the pinned code
// baseline). A client can compare ScopeVersion with the snapshot reference
// recorded by a frozen wave to detect that the version scope moved after that
// wave started.
type BindingSnapshot struct {
	ScopeVersion string
	FrozenWaveID *int64
}

// VersionSummary is the list read model. SUTCount is the number of SUTs in the
// bound environment.
type VersionSummary struct {
	ID        int64
	Title     string
	Status    string
	SUTCount  int
	CreatedAt time.Time
	UpdatedAt time.Time
}
