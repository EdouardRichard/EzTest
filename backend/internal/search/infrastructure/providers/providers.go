// Package providers adapts each owning context's P6 QueryService to the
// global-search Provider port (spec §4.3.1/§6). There is exactly one adapter
// per search type; every adapter issues one read-only application call against
// its owning context and never reads another context's tables or SQL.
//
// Files are grouped by owning context: version.go covers version and
// requirement, design.go covers case, delivery.go covers issue and report.
package providers

import (
	"fmt"
	"strconv"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// New assembles the five per-type providers. The owning contexts' QueryServices
// satisfy the three query interfaces directly, so the composition root can pass
// them in without adapters of its own.
func New(version VersionQueries, design DesignQueries, delivery DeliveryQueries) searchapp.Providers {
	return searchapp.Providers{
		Version:     NewVersionSearcher(version),
		Requirement: NewRequirementSearcher(version),
		Case:        NewCaseSearcher(design),
		Issue:       NewIssueSearcher(delivery),
		Report:      NewReportSearcher(delivery),
	}
}

// notConfigured reports a missing owning QueryService. The search service turns
// the error into degraded=true for the affected group.
func notConfigured(context string) error {
	return fmt.Errorf("search: %s query service is not configured", context)
}

// decimalID formats an entity ID for the JSON boundary (contract IDs are
// decimal strings).
func decimalID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// Keep the compiler honest: the owning QueryServices must satisfy the query
// interfaces this package consumes.
var (
	_ VersionQueries  = (*versionapp.QueryService)(nil)
	_ DesignQueries   = (*designapp.QueryService)(nil)
	_ DeliveryQueries = (*deliveryapp.QueryService)(nil)
)
