// Package version adapts the P3 version application services to the codeintel
// VersionCatalog port (design spec §3.5). It reads version bindings,
// requirements and requirement relations through the P3 application layer and
// never touches P3 tables or infrastructure; it is strictly read-only.
package version

import (
	"context"
	"errors"
	"fmt"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codedomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// requirementPageSize is the cursor page size used while draining a version's
// requirements. It matches the P3 HTTP page bound.
const requirementPageSize = 100

// VersionReader is the narrow slice of the P3 version service the adapter needs.
// *version/application.VersionService satisfies it.
type VersionReader interface {
	Get(ctx context.Context, id int64) (*versionapp.Version, error)
}

// RequirementLister is the narrow slice of the P3 requirement service the
// adapter needs. *version/application.RequirementService satisfies it.
type RequirementLister interface {
	List(ctx context.Context, versionID int64, status string, page versiondomain.Page) ([]versionapp.Requirement, bool, error)
}

// RelationLister is the narrow slice of the P3 relation service the adapter
// needs. *version/application.RelationService satisfies it.
type RelationLister interface {
	List(ctx context.Context, versionID int64) ([]versionapp.Relation, error)
}

// Catalog is the codeintel VersionCatalog adapter over the P3 application
// services.
type Catalog struct {
	versions     VersionReader
	requirements RequirementLister
	relations    RelationLister
}

// NewCatalog wires the adapter to the P3 version, requirement and relation
// services.
func NewCatalog(versions VersionReader, requirements RequirementLister, relations RelationLister) *Catalog {
	return &Catalog{versions: versions, requirements: requirements, relations: relations}
}

var _ codeintelapp.VersionCatalog = (*Catalog)(nil)

// Get returns the codeintel projection of one P3 version binding. A version
// without a system/repository binding reports the Has flags false and zero
// values; a missing version is a codeintel not-found error.
func (c *Catalog) Get(ctx context.Context, versionID int64) (codeintelapp.VersionInfo, error) {
	if c == nil || c.versions == nil {
		return codeintelapp.VersionInfo{}, errors.New("codeintel: version catalog is not configured")
	}
	version, err := c.versions.Get(ctx, versionID)
	if err != nil {
		return codeintelapp.VersionInfo{}, mapVersionLookupError(err)
	}
	if version == nil {
		return codeintelapp.VersionInfo{}, codedomain.NewNotFound("not_found",
			fmt.Sprintf("version %d not found", versionID))
	}

	info := codeintelapp.VersionInfo{ID: version.ID}
	if version.SystemID != nil {
		info.HasSystem = true
		info.SystemID = *version.SystemID
	}
	if version.CodeRepositoryID != nil {
		info.HasRepo = true
		info.RepoID = *version.CodeRepositoryID
	}
	if version.CodeBranch != nil {
		info.Branch = *version.CodeBranch
	}
	if version.CodeBaselineCommit != nil {
		info.BaselineCommit = *version.CodeBaselineCommit
	}
	return info, nil
}

// ListRequirements returns every requirement of versionID, draining the P3
// cursor pages so an analysis never sees only the first 100 requirements.
func (c *Catalog) ListRequirements(ctx context.Context, versionID int64) ([]codeintelapp.RequirementInfo, error) {
	if c == nil || c.requirements == nil {
		return nil, errors.New("codeintel: version catalog requirement reader is not configured")
	}
	requirements := make([]codeintelapp.RequirementInfo, 0)
	cursor := ""
	for {
		page, more, err := c.requirements.List(ctx, versionID, "", versiondomain.Page{
			Limit:  requirementPageSize,
			Cursor: cursor,
		})
		if err != nil {
			return nil, err
		}
		for _, requirement := range page {
			requirements = append(requirements, codeintelapp.RequirementInfo{
				ID:          requirement.ID,
				Title:       requirement.Title,
				Description: requirement.Description,
				Status:      requirement.Status,
			})
		}
		if !more {
			return requirements, nil
		}
		if len(page) == 0 {
			return nil, errors.New("codeintel: list requirements: empty page with more set")
		}
		next := platform.EncodeCursor(page[len(page)-1].ID)
		if next == cursor {
			return nil, errors.New("codeintel: list requirements: cursor did not advance")
		}
		cursor = next
	}
}

// ListRelations returns every requirement relation of versionID. The P3
// relation service already bounds one version's listing, so the adapter maps
// the rows one-to-one.
func (c *Catalog) ListRelations(ctx context.Context, versionID int64) ([]codeintelapp.RelationInfo, error) {
	if c == nil || c.relations == nil {
		return nil, errors.New("codeintel: version catalog relation reader is not configured")
	}
	relations, err := c.relations.List(ctx, versionID)
	if err != nil {
		return nil, err
	}
	mapped := make([]codeintelapp.RelationInfo, 0, len(relations))
	for _, relation := range relations {
		mapped = append(mapped, codeintelapp.RelationInfo{
			FromID: relation.FromRequirementID,
			ToID:   relation.ToRequirementID,
			Type:   relation.RelationType,
		})
	}
	return mapped, nil
}

// mapVersionLookupError translates a P3 not-found into the codeintel not-found
// error. Operational failures are returned unchanged so they stay internal.
func mapVersionLookupError(err error) error {
	var domainErr *versiondomain.Error
	if errors.As(err, &domainErr) && domainErr.Kind == versiondomain.KindNotFound {
		return codedomain.NewNotFound("not_found", domainErr.Error())
	}
	return err
}
