// Package resource adapts the P2 resource application services to the P5
// mindmap gateway ports (spec §4.3/§6.9): the environment/app-model catalog and
// the exploration session reader. The adapter is deliberately thin: it maps the
// P2 projections, forces the P5 exploration source on imported app models and
// translates P2 errors into the mindmap domain error kinds. Existing-key
// skipping is the exploration service's job, not the adapter's.
package resource

import (
	"context"
	"errors"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// appModelPageSize is the cursor page size used while draining a system's app
// model nodes. It matches the P2 page bound.
const appModelPageSize = 100

// EnvironmentReader is the narrow slice of the P2 catalog service the adapter
// needs. *resource/application.CatalogService satisfies it.
type EnvironmentReader interface {
	GetEnvironment(ctx context.Context, id int64) (*resourcedomain.Environment, error)
}

// AppModelCatalog is the narrow slice of the P2 catalog service the adapter
// needs for the app model read and import. *resource/application.CatalogService
// satisfies it.
type AppModelCatalog interface {
	ListAppModels(ctx context.Context, filter resourcedomain.AppModelFilter) ([]resourcedomain.AppModelNode, bool, error)
	ImportAppModels(ctx context.Context, systemID int64, inputs []resourceapp.AppModelInput) (int, int, error)
}

// CatalogAdapter implements application.CatalogGateway on top of the P2 catalog
// service.
type CatalogAdapter struct {
	environments EnvironmentReader
	appModels    AppModelCatalog
}

// NewCatalogAdapter wires the adapter to the P2 environment reader and app model
// catalog.
func NewCatalogAdapter(environments EnvironmentReader, appModels AppModelCatalog) *CatalogAdapter {
	return &CatalogAdapter{environments: environments, appModels: appModels}
}

var _ mindmapapp.CatalogGateway = (*CatalogAdapter)(nil)

// GetEnvironment returns the exploration-relevant projection of one environment:
// its base URL and the allowed-host allowlist used by the read-only explorer.
func (a *CatalogAdapter) GetEnvironment(ctx context.Context, envID int64) (mindmapapp.EnvironmentSnapshot, error) {
	if a == nil || a.environments == nil {
		return mindmapapp.EnvironmentSnapshot{}, errors.New("mindmap: environment reader is not configured")
	}
	environment, err := a.environments.GetEnvironment(ctx, envID)
	if err != nil {
		return mindmapapp.EnvironmentSnapshot{}, mapResourceError(err, "environment", envID)
	}
	if environment == nil {
		return mindmapapp.EnvironmentSnapshot{}, mindmapdomain.NewNotFound("environment", envID)
	}
	return mindmapapp.EnvironmentSnapshot{
		ID:           environment.ID,
		SystemID:     environment.SystemID,
		Name:         environment.Name,
		BaseURL:      environment.BaseURL,
		Kind:         environment.Kind,
		AllowedHosts: cloneStrings(environment.AllowedHosts),
	}, nil
}

// ListAppModels returns every app model node of systemID, draining the P2 cursor
// pages so the explorer can compare the whole model.
func (a *CatalogAdapter) ListAppModels(ctx context.Context, systemID int64) ([]mindmapapp.AppModelSnapshot, error) {
	if a == nil || a.appModels == nil {
		return nil, errors.New("mindmap: app model catalog is not configured")
	}
	nodes := make([]mindmapapp.AppModelSnapshot, 0)
	afterID := int64(0)
	for {
		page, more, err := a.appModels.ListAppModels(ctx, resourcedomain.AppModelFilter{
			SystemID: systemID,
			Page:     resourcedomain.Page{Limit: appModelPageSize, AfterID: afterID},
		})
		if err != nil {
			return nil, mapResourceError(err, "app_model", systemID)
		}
		for _, node := range page {
			nodes = append(nodes, mindmapapp.AppModelSnapshot{
				ID:       node.ID,
				SystemID: node.SystemID,
				ParentID: cloneInt64(node.ParentID),
				Key:      node.Key,
				Kind:     node.Kind,
				Name:     node.Name,
				Path:     node.Path,
				Method:   node.Method,
				Source:   node.Source,
			})
		}
		if !more {
			return nodes, nil
		}
		if len(page) == 0 {
			return nil, errors.New("mindmap: list app models: empty page with more set")
		}
		next := page[len(page)-1].ID
		if next == afterID {
			return nil, errors.New("mindmap: list app models: cursor did not advance")
		}
		afterID = next
	}
}

// ImportAppModels upserts explored app model nodes. The source is always
// 'explored' (D36): the caller cannot downgrade an exploration import to
// 'manual' or 'import', and duplicate/existing keys are the P2 upsert's concern.
func (a *CatalogAdapter) ImportAppModels(ctx context.Context, systemID int64, inputs []mindmapapp.AppModelInput) (int, int, error) {
	if a == nil || a.appModels == nil {
		return 0, 0, errors.New("mindmap: app model catalog is not configured")
	}
	converted := make([]resourceapp.AppModelInput, 0, len(inputs))
	for _, input := range inputs {
		converted = append(converted, resourceapp.AppModelInput{
			Key:       input.Key,
			Kind:      input.Kind,
			Name:      input.Name,
			Path:      input.Path,
			Method:    input.Method,
			ParentKey: input.ParentKey,
			Source:    resourcedomain.AppModelSourceExplored,
			Meta:      input.Meta,
		})
	}
	created, updated, err := a.appModels.ImportAppModels(ctx, systemID, converted)
	if err != nil {
		return 0, 0, mapResourceError(err, "system", systemID)
	}
	return created, updated, nil
}

// mapResourceError translates a P2 resource domain error into the mindmap error
// kinds and reasons (spec §7.4). Not-found is reported with the mindmap
// "<resource>_not_found" reason; an operational failure is returned unchanged.
func mapResourceError(err error, resourceName string, id int64) error {
	if err == nil {
		return nil
	}
	var domainErr *resourcedomain.Error
	if !errors.As(err, &domainErr) {
		return err
	}
	switch domainErr.Kind {
	case resourcedomain.KindNotFound:
		return mindmapdomain.NewNotFound(resourceName, id)
	case resourcedomain.KindInvalid:
		return mindmapdomain.NewInvalid(domainErr.Field, resourceReasonOrDefault(domainErr.Reason, "invalid"), domainErr.Error())
	case resourcedomain.KindConflict:
		return mindmapdomain.NewConflict(resourceReasonOrDefault(domainErr.Reason, "conflict"), domainErr.Error())
	case resourcedomain.KindDependency:
		return mindmapdomain.NewConflict(resourceReasonOrDefault(domainErr.Reason, "has_dependencies"), domainErr.Error())
	case resourcedomain.KindStateConflict, resourcedomain.KindLeaseConflict:
		return mindmapdomain.NewStateConflict(resourceReasonOrDefault(domainErr.Reason, "state_conflict"), domainErr.Error())
	default:
		return err
	}
}

// resourceReasonOrDefault returns reason, or fallback when the P2 error carried
// none.
func resourceReasonOrDefault(reason, fallback string) string {
	if reason == "" {
		return fallback
	}
	return reason
}

// cloneInt64 copies an optional id so the projection never aliases P2 state.
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// cloneStrings copies a P2 string slice so the projection never aliases stored
// state.
func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}
