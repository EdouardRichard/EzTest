package resourceapp

import (
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// SUTScope is one asset triple: the pages, APIs and target databases of a single
// SUT, or of every SUT of an environment flattened into one union. Raw catalog
// state and the effective version scope share this shape so a consumer can hold
// one without the other being visible, and so the two projections cannot drift
// apart field by field.
type SUTScope struct {
	Pages     []PageView
	APIs      []APIView
	Databases []DatabaseView
}

// IsApproved reports whether the SUT itself is approved for version scope. It is
// the SINGLE SUT-status predicate of the effective-scope rule (v0.2.4-B R-B4):
// EffectiveAssets, EnvironmentView.EffectiveScope and
// versionapp.EnvironmentSUTCatalog.EffectiveSUTs all gate on it, so the trust
// boundary is never re-implemented in an adapter, SQL or handler. A proposed or
// rejected SUT — e.g. one an Agent registered but an admin has not approved — is
// excluded whole, so none of its pages/APIs/databases reach the execution
// fingerprint, credential resolution or the design model.
//
// The gate is fail-closed for any explicit non-approved status. The zero value
// (empty Status) is treated as approved: it is the legacy default resource_0005
// backfills for every pre-existing row (status TEXT NOT NULL DEFAULT 'approved'),
// and every SUT read from the catalog carries a concrete status. This keeps rows
// and fixtures that predate the status column in scope without weakening the gate
// against the proposed/rejected values subproject F will write.
func (s SUTView) IsApproved() bool {
	status := s.Status
	if status == "" {
		status = resource.SUTStatusApproved
	}
	return status == resource.SUTStatusApproved
}

// EffectiveAssets returns the EFFECTIVE (version-scope) asset projection of one
// SUT: the approved pages, the approved APIs and the active target databases, in
// catalog order.
//
// This is the single implementation of the effective-scope rule (v0.2.4-B review
// round 1, Important 1). SUTView.Pages/APIs/Databases are RAW catalog state —
// every status, including the rows a discovery run merely proposed and the
// databases an operator disabled — because the resource catalog UI has to show
// them and the binding counters have to report proposed alongside approved.
// Anything answering "what is inside the scope of a version bound to this
// environment" must go through this method, through
// EnvironmentView.EffectiveScope, or through
// versionapp.EnvironmentSUTCatalog.EffectiveSUTs, and never through the raw
// lists: reading them leaks a proposed page into the execution fingerprint and
// the design model's page locators, and hands out the credential of a disabled
// database.
//
// The statuses are the resource domain's own constants, so the rule cannot drift
// from the CHECK constraints that persist them.
func (s SUTView) EffectiveAssets() SUTScope {
	scope := SUTScope{
		Pages:     make([]PageView, 0, len(s.Pages)),
		APIs:      make([]APIView, 0, len(s.APIs)),
		Databases: make([]DatabaseView, 0, len(s.Databases)),
	}
	// R-B4 trust boundary: a non-approved SUT contributes nothing to the version
	// scope, regardless of the statuses of its individual assets.
	if !s.IsApproved() {
		return scope
	}
	for _, page := range s.Pages {
		if page.Status == resource.PageStatusApproved {
			scope.Pages = append(scope.Pages, page)
		}
	}
	for _, api := range s.APIs {
		if api.Status == resource.APIStatusApproved {
			scope.APIs = append(scope.APIs, api)
		}
	}
	for _, database := range s.Databases {
		if database.Status == resource.StatusActive {
			scope.Databases = append(scope.Databases, database)
		}
	}
	return scope
}

// EffectiveScope returns the flattened effective asset scope of every SUT of the
// environment, in SUT order: the union of each SUT's EffectiveAssets. The version
// binding's flat Pages/APIs/Databases lists are exactly this union, which is why
// the per-SUT and the environment-wide effective scope can never disagree. The
// lists are non-nil so an environment without SUTs (or without approved assets)
// can be ranged over without a nil check.
func (e EnvironmentView) EffectiveScope() SUTScope {
	scope := SUTScope{
		Pages:     make([]PageView, 0),
		APIs:      make([]APIView, 0),
		Databases: make([]DatabaseView, 0),
	}
	for _, sut := range e.SUTs {
		effective := sut.EffectiveAssets()
		scope.Pages = append(scope.Pages, effective.Pages...)
		scope.APIs = append(scope.APIs, effective.APIs...)
		scope.Databases = append(scope.Databases, effective.Databases...)
	}
	return scope
}
