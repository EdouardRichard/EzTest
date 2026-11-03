// Package adapters implements the execution context's application ports over
// the version/design/resource/agent/storage/executor infrastructure. It keeps
// the execution application layer free of cross-context and driver imports.
package adapters

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// VersionService is the version application read surface consumed by the
// adapter (*versionapp.Service satisfies it). Reopening is deliberately not
// part of this surface: the intent path must use the P4 VersionReopener so the
// version.reopened projection is published.
type VersionService interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// VersionReopener is the P4 version reopen surface
// (*versionapp.VersionCloseService satisfies it). It publishes the
// version.reopened projection (version + system channels) and marks the version
// report stale, so the intent path never emits only the P3 status_changed
// projection.
type VersionReopener interface {
	ReopenIfCompleted(ctx context.Context, versionID int64, trigger, reason string) error
}

// VersionStageReader is the deterministic stage-machine projection consumed by
// the execution intent gate (Task 78). *versionapp.StageMachine satisfies it.
type VersionStageReader interface {
	Stage(ctx context.Context, versionID int64) (versionapp.StageView, error)
}

// versionReopenTriggerExecutionIntent is the stable P4 reopen trigger owned by
// the execution intent path (spec §7.5).
const versionReopenTriggerExecutionIntent = "execution_intent"

// VersionAdapter adapts the version application read model to the execution
// ports: fingerprint bindings, task-context SUT bindings and API resolution.
type VersionAdapter struct {
	versions VersionService
	reopener VersionReopener
	stages   VersionStageReader
}

// NewVersionAdapter wires the version adapter. SetVersionReopener must be called
// by the assembly root once the P4 version close service exists.
func NewVersionAdapter(versions VersionService) *VersionAdapter {
	return &VersionAdapter{versions: versions}
}

// SetVersionReopener wires the P4 reopen service (wiring-time only).
func (a *VersionAdapter) SetVersionReopener(reopener VersionReopener) {
	a.reopener = reopener
}

// SetStageReader wires the deterministic version-stage projection. The
// execution intent gate refuses to create initial intents unless the version is
// executing, so production must set it; a nil reader leaves the stage empty,
// which fails closed (never auto-executes).
func (a *VersionAdapter) SetStageReader(reader VersionStageReader) {
	a.stages = reader
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
	if a.stages != nil {
		stage, stageErr := a.stages.Stage(ctx, id)
		if stageErr != nil {
			return executionapp.VersionBinding{}, stageErr
		}
		binding.Stage = string(stage.Stage)
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

// Reopen clears the completed projection of a version through the P4 reopen
// service: it emits version.reopened (trigger=execution_intent) to version:{id}
// and system and best-effort marks the version report stale. It fails closed
// when the P4 service was not wired rather than emitting only the P3
// version.status_changed projection.
func (a *VersionAdapter) Reopen(ctx context.Context, id int64) error {
	if a.reopener == nil {
		return fmt.Errorf("version adapter: P4 version reopen service is not wired")
	}
	return a.reopener.ReopenIfCompleted(ctx, id, versionReopenTriggerExecutionIntent, "")
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
			binding.Commit = item.CodeBaselineCommit
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

// ResolveReplacement resolves the same logical case's current+confirmed version
// through the design historical read (Task 106). It is the execution re-plan
// input: a stale intent's confirmed version was replaced by a re-confirmed
// revision, so the selected case can be queued again instead of silently
// dropped.
func (r *DesignCaseReader) ResolveReplacement(ctx context.Context, versionID, caseVersionID int64) (execution.CaseSnapshot, bool, error) {
	view, found, err := r.graph.ReplacementCaseVersion(ctx, versionID, caseVersionID)
	if err != nil {
		return execution.CaseSnapshot{}, false, err
	}
	if !found {
		return execution.CaseSnapshot{}, false, nil
	}
	return mapExecutableCase(view), true, nil
}

// CaseVersionLabel resolves the display labels of any case version of the
// version, including superseded/stale history (Task 106 / PO E2E F11). It is
// the fallback for terminal case_runs whose version left the executable set, so
// the UI/report row keeps its case id and title.
func (r *DesignCaseReader) CaseVersionLabel(ctx context.Context, versionID, caseVersionID int64) (executionapp.CaseVersionLabel, error) {
	views, err := r.graph.ReadCaseVersionsByIDs(ctx, versionID, []int64{caseVersionID})
	if err != nil {
		return executionapp.CaseVersionLabel{}, err
	}
	if len(views) == 0 {
		return executionapp.CaseVersionLabel{}, execution.ErrNotFound
	}
	view := views[0]
	return executionapp.CaseVersionLabel{
		CaseID:        view.CaseID,
		CaseVersionID: view.ID,
		Title:         view.Title,
		Revision:      int(view.Revision),
		Status:        view.Status,
	}, nil
}

// mapExecutableCase maps a design executable case to the execution snapshot and
// resolves the P2 target inheritance: case_step/case_assertion
// target_version_sut_id is nullable and empty inherits
// case_version.default_version_sut_id (P2 spec §case_step). A case without a
// default target is unambiguous when the version has a single SUT, so that
// binding is inherited too. QA-F1 died because the execution app received raw
// target 0 and rejected the case before the agent could record anything.
func mapExecutableCase(view designapp.ExecutableCaseView) execution.CaseSnapshot {
	defaultSUT := view.DefaultVersionSUTID
	if defaultSUT <= 0 && len(view.VersionSUTIDs) == 1 {
		defaultSUT = view.VersionSUTIDs[0]
	}
	snapshot := execution.CaseSnapshot{
		CaseID:               view.CaseID,
		CaseVersionID:        view.CaseVersionID,
		RequirementID:        view.RequirementID,
		RequirementVersionID: view.RequirementVersionID,
		DefaultVersionSUTID:  defaultSUT,
		Title:                view.Title,
		Description:          view.Description,
		Preconditions:        view.Preconditions,
		Priority:             view.Priority,
		Steps:                make([]execution.CaseStep, 0, len(view.Steps)),
		Assertions:           make([]execution.CaseAssertion, 0, len(view.Assertions)),
	}
	used := append([]int64(nil), view.VersionSUTIDs...)
	if defaultSUT > 0 {
		used = append(used, defaultSUT)
	}
	for _, step := range view.Steps {
		target := step.TargetVersionSUTID
		if target <= 0 {
			target = defaultSUT
		}
		if target > 0 {
			used = append(used, target)
		}
		snapshot.Steps = append(snapshot.Steps, execution.CaseStep{
			Seq:                step.Seq,
			Action:             step.Action,
			Expected:           step.Expected,
			TargetVersionSUTID: target,
			AccountRole:        step.AccountRole,
			TargetPageID:       step.PageID,
			TargetAPIID:        step.APIID,
			Data:               step.Data,
		})
	}
	for _, assertion := range view.Assertions {
		target := assertion.TargetVersionSUTID
		if target <= 0 {
			target = defaultSUT
		}
		if target > 0 {
			used = append(used, target)
		}
		snapshot.Assertions = append(snapshot.Assertions, execution.CaseAssertion{
			StepSeq:            assertion.StepSeq,
			AssertionSeq:       assertion.AssertionSeq,
			Kind:               assertion.Kind,
			Expected:           assertion.Expected,
			TargetVersionSUTID: target,
			TargetDatabaseID:   assertion.DatabaseID,
		})
	}
	snapshot.VersionSUTIDs = normalizedCaseSUTIDs(used)
	return snapshot
}

// normalizedCaseSUTIDs is the P3 §5.1 case SUT set: deduplicated and sorted so
// one case version always fingerprints to the same intent.
func normalizedCaseSUTIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized
}
