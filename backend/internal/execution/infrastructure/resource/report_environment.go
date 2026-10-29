// This file adapts the version and resource application ports to T14's
// ReportEnvironmentSource: the batch report header names the bound system and
// environment and its base URL, which the execution context does not own.
package resource

import (
	"context"
	"errors"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// VersionRefReader is the narrow execution VersionPort slice the report needs.
// *execution/infrastructure/version.Adapter satisfies it.
type VersionRefReader interface {
	GetVersion(ctx context.Context, versionID int64) (executionapp.VersionRef, error)
}

// ReportEnvironmentSource implements application.ReportEnvironmentSource by
// resolving the version's environment binding through the resource catalog.
type ReportEnvironmentSource struct {
	versions VersionRefReader
	catalog  Catalog
}

// NewReportEnvironmentSource wires the report environment source to the version
// port and the resource catalog. Missing dependencies fail closed.
func NewReportEnvironmentSource(versions VersionRefReader, catalog Catalog) *ReportEnvironmentSource {
	return &ReportEnvironmentSource{versions: versions, catalog: catalog}
}

var _ executionapp.ReportEnvironmentSource = (*ReportEnvironmentSource)(nil)

// ReportEnvironment resolves the report header environment of a version. An
// unbound version is an empty header rather than an error (a report must still
// render); a bound environment that cannot be read is an error. A stale
// environment binding to a missing system leaves the system name empty.
func (s *ReportEnvironmentSource) ReportEnvironment(ctx context.Context, versionID int64) (executionapp.ReportEnvironment, error) {
	if s == nil || s.versions == nil {
		return executionapp.ReportEnvironment{}, errors.New("execution: report environment source is not configured")
	}
	version, err := s.versions.GetVersion(ctx, versionID)
	if err != nil {
		return executionapp.ReportEnvironment{}, err
	}
	if version.EnvironmentID == nil {
		return executionapp.ReportEnvironment{}, nil
	}
	if s.catalog == nil {
		return executionapp.ReportEnvironment{}, errors.New("execution: resource catalog is not configured")
	}

	environment, err := s.catalog.GetEnvironment(ctx, *version.EnvironmentID)
	if err != nil {
		return executionapp.ReportEnvironment{}, mapResourceError(err)
	}
	if environment == nil {
		return executionapp.ReportEnvironment{}, errors.New("execution: report environment is unavailable")
	}

	resolved := executionapp.ReportEnvironment{
		Environment: environment.Name,
		BaseURL:     environment.BaseURL,
	}
	systemID := environment.SystemID
	if systemID <= 0 && version.SystemID != nil {
		systemID = *version.SystemID
	}
	if systemID <= 0 {
		return resolved, nil
	}
	system, err := s.catalog.GetSystem(ctx, systemID)
	if err != nil {
		if isResourceNotFound(err) {
			return resolved, nil
		}
		return executionapp.ReportEnvironment{}, mapResourceError(err)
	}
	if system != nil {
		resolved.System = system.Name
	}
	return resolved, nil
}
