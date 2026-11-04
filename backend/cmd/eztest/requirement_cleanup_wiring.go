package main

// Requirement cleanup composition. Draft delete/merge/dedupe are scoped by the
// version reader: the service checks the version exists before it retires a
// draft, which is what makes an unknown version a typed 404 instead of an empty
// 200 dedupe response. The reader is therefore required at the composition
// root; wiring nil would silently disable that check.

import (
	"errors"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// newRequirementService wires the requirement use cases. It fails fast on a
// nil version reader because Dedupe/List use it for the version-existence
// check, and Delete/Merge read requirements through the version-scoped
// repository. It returns a typed error instead of a service so the composition
// root can log.Fatalf before the API starts serving cleanup routes without
// scope checks.
func newRequirementService(
	repo versiondomain.RequirementRepository,
	versions versionapp.VersionReader,
	resources versionapp.ResourceCatalog,
	stale versionapp.CaseStaleHook,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
	ids platformid.Generator,
	clk clock.Clock,
) (*versionapp.RequirementService, error) {
	if versions == nil {
		return nil, errors.New("requirement version reader is required")
	}
	return versionapp.NewRequirementService(repo, versions, resources, stale, audit, events, ids, clk), nil
}
