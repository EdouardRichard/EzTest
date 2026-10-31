// Package adapters implements the execution context's application ports over
// the version/design/resource/agent/storage/executor infrastructure. It keeps
// the execution application layer free of cross-context and driver imports.
package adapters

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// VersionService is the version application read/write surface consumed by the
// adapter (*versionapp.Service satisfies it).
type VersionService interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
	Reopen(ctx context.Context, id int64) error
}

// VersionAdapter adapts the version application read model to the execution
// ports: fingerprint bindings, task-context SUT bindings and API resolution.
type VersionAdapter struct {
	versions VersionService
}

// NewVersionAdapter wires the version adapter.
func NewVersionAdapter(versions VersionService) *VersionAdapter {
	return &VersionAdapter{versions: versions}
}

var (
	_ executionapp.VersionReader      = (*VersionAdapter)(nil)
	_ executionapp.SUTBindingResolver = (*VersionAdapter)(nil)
	_ executionapp.SUTAPIResolver     = (*VersionAdapter)(nil)
)

// Get returns the fingerprint-relevant version binding.
func (a *VersionAdapter) Get(ctx context.Context, id int64) (executionapp.VersionBinding, error) {
	view, err := a.versions.Get(ctx, id)
	if err != nil {
		return executionapp.VersionBinding{}, err
	}
	binding := executionapp.VersionBinding{
		VersionID: view.ID,
		Status:    view.Status,
		SUTs:      make([]executionapp.VersionSUTBinding, 0, len(view.Bindings)),
	}
	for _, item := range view.Bindings {
		sutBinding, mapErr := mapVersionBinding(item)
		if mapErr != nil {
			return executionapp.VersionBinding{}, mapErr
		}
		binding.SUTs = append(binding.SUTs, sutBinding)
	}
	return binding, nil
}

// Reopen clears the completed projection of a version (scheduler intent rule).
func (a *VersionAdapter) Reopen(ctx context.Context, id int64) error {
	return a.versions.Reopen(ctx, id)
}

// Resolve returns the task-context bindings (URLs, endpoints, roles, no
// credentials) of every active version SUT.
func (a *VersionAdapter) Resolve(ctx context.Context, versionID int64) ([]execution.SUTBinding, error) {
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	bindings := make([]execution.SUTBinding, 0, len(view.Bindings))
	for _, item := range view.Bindings {
		pageURLs := make([]string, 0, len(item.Pages))
		for _, page := range item.Pages {
			if strings.TrimSpace(page.URL) != "" {
				pageURLs = append(pageURLs, page.URL)
			}
		}
		apiEndpoints := make([]string, 0, len(item.APIs))
		for _, api := range item.APIs {
			if strings.TrimSpace(api.PathTemplate) == "" {
				continue
			}
			method := strings.ToUpper(strings.TrimSpace(api.Method))
			if method == "" {
				method = "GET"
			}
			apiEndpoints = append(apiEndpoints, method+" "+api.PathTemplate)
		}
		roles := make([]string, 0, len(item.Accounts))
		seen := map[string]struct{}{}
		for _, account := range item.Accounts {
			for _, role := range account.Roles {
				role = strings.TrimSpace(role)
				if role == "" {
					continue
				}
				if _, ok := seen[role]; ok {
					continue
				}
				seen[role] = struct{}{}
				roles = append(roles, role)
			}
		}
		bindings = append(bindings, execution.SUTBinding{
			VersionSUTID:    item.ID,
			SUTName:         item.SUT.Name,
			EnvironmentName: item.Environment.Name,
			BaseURL:         item.Environment.BaseURL,
			PageURLs:        pageURLs,
			APIEndpoints:    apiEndpoints,
			AccountRoles:    roles,
		})
	}
	return bindings, nil
}

// API resolves one api_id bound to a version SUT (for http.request api_id).
func (a *VersionAdapter) API(ctx context.Context, versionID, versionSUTID, apiID int64) (executionapp.SUTAPI, error) {
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return executionapp.SUTAPI{}, err
	}
	for _, item := range view.Bindings {
		if item.ID != versionSUTID {
			continue
		}
		for _, api := range item.APIs {
			if parseViewID(api.ID) != apiID {
				continue
			}
			method := strings.ToUpper(strings.TrimSpace(api.Method))
			if method == "" {
				method = "GET"
			}
			return executionapp.SUTAPI{Method: method, Path: api.PathTemplate}, nil
		}
	}
	return executionapp.SUTAPI{}, platform.NotFound("api")
}

// mapVersionBinding converts one BindingView to the fingerprint binding.
func mapVersionBinding(item versionapp.BindingView) (executionapp.VersionSUTBinding, error) {
	sutID := parseViewID(item.SUT.ID)
	environmentID := parseViewID(item.Environment.ID)
	if sutID <= 0 || environmentID <= 0 {
		return executionapp.VersionSUTBinding{}, fmt.Errorf("version binding has invalid resource IDs")
	}
	binding := executionapp.VersionSUTBinding{
		VersionSUTID:    item.ID,
		SUTID:           sutID,
		EnvironmentID:   environmentID,
		SUTName:         item.SUT.Name,
		EnvironmentName: item.Environment.Name,
		BaseURL:         item.Environment.BaseURL,
	}
	for _, page := range item.Pages {
		if id := parseViewID(page.ID); id > 0 {
			binding.PageIDs = append(binding.PageIDs, id)
		}
	}
	for _, api := range item.APIs {
		if id := parseViewID(api.ID); id > 0 {
			binding.APIIDs = append(binding.APIIDs, id)
		}
	}
	for _, database := range item.Databases {
		if id := parseViewID(database.ID); id > 0 {
			binding.DatabaseIDs = append(binding.DatabaseIDs, id)
		}
	}
	seenRoles := map[string]struct{}{}
	for _, account := range item.Accounts {
		for _, role := range account.Roles {
			role = strings.TrimSpace(role)
			if role == "" {
				continue
			}
			if _, ok := seenRoles[role]; ok {
				continue
			}
			seenRoles[role] = struct{}{}
			binding.AccountRoles = append(binding.AccountRoles, role)
		}
	}
	if item.Repository != nil {
		if id := parseViewID(item.Repository.ID); id > 0 {
			binding.CodeRepoID = &id
			binding.Branch = item.Repository.Branch
		}
	}
	return binding, nil
}

// parseViewID parses a decimal resource ID from a view. Invalid IDs become 0.
func parseViewID(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

// DesignCaseReader adapts the design executable-case read model to the
// execution CaseReader port.
type DesignCaseReader struct {
	graph *designapp.GraphService
}

// NewDesignCaseReader wires the design case reader.
func NewDesignCaseReader(graph *designapp.GraphService) *DesignCaseReader {
	return &DesignCaseReader{graph: graph}
}

var _ executionapp.CaseReader = (*DesignCaseReader)(nil)

// GetCaseVersion returns one current+confirmed case version for execution.
func (r *DesignCaseReader) GetCaseVersion(ctx context.Context, versionID, caseVersionID int64) (execution.CaseSnapshot, error) {
	view, err := r.graph.GetCaseVersion(ctx, versionID, caseVersionID)
	if err != nil {
		return execution.CaseSnapshot{}, err
	}
	return mapExecutableCase(view), nil
}

// ListExecutable returns every current+confirmed case version of the version.
func (r *DesignCaseReader) ListExecutable(ctx context.Context, versionID int64) ([]execution.CaseSnapshot, error) {
	views, err := r.graph.ListExecutable(ctx, versionID)
	if err != nil {
		return nil, err
	}
	snapshots := make([]execution.CaseSnapshot, 0, len(views))
	for _, view := range views {
		snapshots = append(snapshots, mapExecutableCase(view))
	}
	return snapshots, nil
}

func mapExecutableCase(view designapp.ExecutableCaseView) execution.CaseSnapshot {
	snapshot := execution.CaseSnapshot{
		CaseID:               view.CaseID,
		CaseVersionID:        view.CaseVersionID,
		RequirementID:        view.RequirementID,
		RequirementVersionID: view.RequirementVersionID,
		DefaultVersionSUTID:  view.DefaultVersionSUTID,
		Title:                view.Title,
		Description:          view.Description,
		Preconditions:        view.Preconditions,
		Priority:             view.Priority,
		VersionSUTIDs:        append([]int64(nil), view.VersionSUTIDs...),
		Steps:                make([]execution.CaseStep, 0, len(view.Steps)),
		Assertions:           make([]execution.CaseAssertion, 0, len(view.Assertions)),
	}
	for _, step := range view.Steps {
		snapshot.Steps = append(snapshot.Steps, execution.CaseStep{
			Seq:                step.Seq,
			Action:             step.Action,
			Expected:           step.Expected,
			TargetVersionSUTID: step.TargetVersionSUTID,
			AccountRole:        step.AccountRole,
			TargetPageID:       step.PageID,
			TargetAPIID:        step.APIID,
			Data:               step.Data,
		})
	}
	for _, assertion := range view.Assertions {
		snapshot.Assertions = append(snapshot.Assertions, execution.CaseAssertion{
			StepSeq:            assertion.StepSeq,
			AssertionSeq:       assertion.AssertionSeq,
			Kind:               assertion.Kind,
			Expected:           assertion.Expected,
			TargetVersionSUTID: assertion.TargetVersionSUTID,
			TargetDatabaseID:   assertion.DatabaseID,
		})
	}
	return snapshot
}
