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
	catalog := view.Binding
	binding := executionapp.VersionBinding{
		VersionID: view.ID,
		Status:    view.Status,
		SUTs:      make([]executionapp.SUTBinding, 0, len(catalog.Environment.SUTs)),
	}
	if a.stages != nil {
		stage, stageErr := a.stages.Stage(ctx, id)
		if stageErr != nil {
			return executionapp.VersionBinding{}, stageErr
		}
		binding.Stage = string(stage.Stage)
	}
	for _, sut := range catalog.EffectiveSUTs() {
		sutBinding, mapErr := mapVersionBinding(catalog, sut)
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
		return fmt.Errorf("version adapter: version reopen service is not wired")
	}
	return a.reopener.ReopenIfCompleted(ctx, id, versionReopenTriggerExecutionIntent, "")
}

// Resolve returns the task-context bindings (URLs, endpoints, roles, no
// credentials) of every SUT bound to the version.
func (a *VersionAdapter) Resolve(ctx context.Context, versionID int64) ([]execution.SUTBinding, error) {
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	catalog := view.Binding
	bindings := make([]execution.SUTBinding, 0, len(catalog.Environment.SUTs))
	// EffectiveSUTs, not Environment.SUTs: the discovery scope must contain only
	// the pages/APIs a version is allowed to test. The raw nested catalog lists
	// also carry the rows a previous discovery run merely proposed, and crawling
	// those would let an unapproved page widen its own scope on the next run.
	for _, item := range catalog.EffectiveSUTs() {
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
		// v0.2.4-B: accounts are bound at version level, not per SUT.
		roles := make([]string, 0, len(catalog.Accounts))
		seen := map[string]struct{}{}
		for _, account := range catalog.Accounts {
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
			// v0.2.4-B: the per-version binding row is gone, so the SUT id is
			// the only key. Intro/EnvironmentID (v0.2.4-F T12) carry the catalog
			// identity prose and the bound environment into the task context,
			// so the executor can match the case's sut_description against the
			// catalog (design §6.1) without a second catalog read.
			SUTID:           item.SUTID,
			SUTName:         item.Name,
			Intro:           item.Intro,
			EnvironmentID:   parseViewID(catalog.Environment.ID),
			EnvironmentName: catalog.Environment.Name,
			BaseURL:         item.BaseURL,
			PageURLs:        pageURLs,
			APIEndpoints:    apiEndpoints,
			AccountRoles:    roles,
		})
	}
	return bindings, nil
}

// API resolves one api_id of a bound SUT (for http.request api_id). Only
// an APPROVED API of the bound environment resolves: a proposed api_id is not part
// of the version's scope, so it is a 404 exactly like an id of another SUT.
func (a *VersionAdapter) API(ctx context.Context, versionID, sutID, apiID int64) (executionapp.SUTAPI, error) {
	view, err := a.versions.Get(ctx, versionID)
	if err != nil {
		return executionapp.SUTAPI{}, err
	}
	for _, item := range view.Binding.EffectiveSUTs() {
		if item.SUTID != sutID {
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

// mapVersionBinding converts one SUT of the version's environment catalog to the
// fingerprint binding. v0.2.4-B: the version binds an environment, so the
// per-SUT rows come from the derived catalog while the accounts and the code
// pool are version-level projections.
//
// item is the EFFECTIVE per-SUT projection: the fingerprint's page/API/database
// id lists are the approved/active scope, so a merely proposed page or a disabled
// database never enters them. Reading the raw catalog lists here was the leak
// fixed in review round 1 (Important 1). These lists are the LIVE effective scope
// at read time; the wave freezes them into run_wave.scope_snapshot at plan time
// (R-EX-4/DB-19, SC-F7) — this adapter does not itself freeze anything.
func mapVersionBinding(catalog versionapp.EnvironmentSUTCatalog, item versionapp.EffectiveSUT) (executionapp.SUTBinding, error) {
	sutID := item.SUTID
	environmentID := parseViewID(catalog.Environment.ID)
	if sutID <= 0 || environmentID <= 0 {
		return executionapp.SUTBinding{}, fmt.Errorf("version binding has invalid resource IDs")
	}
	binding := executionapp.SUTBinding{
		// v0.2.4-B: the per-version binding row is gone, so the SUT id is the
		// only key. It used to be carried twice (binding-row id + SUT id); the
		// dropped leg is why every environment fingerprint hash changes.
		SUTID:           sutID,
		EnvironmentID:   environmentID,
		SUTName:         item.Name,
		EnvironmentName: catalog.Environment.Name,
		BaseURL:         item.BaseURL,
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
	for _, account := range catalog.Accounts {
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
	// The deterministic baseline repository is the lowest-ID row of the
	// environment code pool (GD-03); it anchors the wave scope snapshot's legacy
	// single-repository fallback.
	if len(catalog.Repositories) > 0 {
		if id := parseViewID(catalog.Repositories[0].Repository.ID); id > 0 {
			binding.CodeRepoID = &id
		}
	}
	// R-EX-4/DB-19: the wave scope snapshot freezes every repository of the
	// environment code pool, not only the single deterministic baseline.
	binding.RepositoryIDs = effectiveRepositoryIDs(catalog, binding.CodeRepoID)
	// V-0024: pair EVERY repository of the pool with its OWN pinned commit (the
	// code baseline is the environment repo union). The former single
	// Commit=catalog.CodeBaselineCommit leg could carry a different repository's
	// commit paired with this id; each pair now reads its own row's commit.
	binding.RepositoryBaselines = repositoryBaselines(catalog)
	return binding, nil
}

// repositoryBaselines projects the environment code pool into (repository, own
// commit) pairs, deduplicated and ascending by repository ID (V-0024). A row's
// commit is its own sut_repository.code_baseline_commit, never borrowed from
// another repository. An unresolved row keeps an empty commit; the code baseline
// rejects it typed rather than falling back to a branch name.
func repositoryBaselines(catalog versionapp.EnvironmentSUTCatalog) []executionapp.RepositoryBaseline {
	baselines := make([]executionapp.RepositoryBaseline, 0, len(catalog.Repositories))
	seen := make(map[int64]struct{}, len(catalog.Repositories))
	for _, repository := range catalog.Repositories {
		id := parseViewID(repository.Repository.ID)
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		baselines = append(baselines, executionapp.RepositoryBaseline{
			RepoID: id,
			Commit: repository.CodeBaselineCommit,
		})
	}
	sort.Slice(baselines, func(i, j int) bool { return baselines[i].RepoID < baselines[j].RepoID })
	return baselines
}

// effectiveRepositoryIDs returns the deduplicated ascending code-repository IDs
// of the environment code pool. When the pool is empty it falls back to the
// single baseline repository.
func effectiveRepositoryIDs(catalog versionapp.EnvironmentSUTCatalog, codeRepoID *int64) []int64 {
	ids := make([]int64, 0, len(catalog.Repositories)+1)
	seen := make(map[int64]struct{}, len(catalog.Repositories)+1)
	for _, repository := range catalog.Repositories {
		id := parseViewID(repository.Repository.ID)
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 && codeRepoID != nil && *codeRepoID > 0 {
		ids = append(ids, *codeRepoID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
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

// mapExecutableCase maps a design executable case to the execution snapshot.
//
// v0.2.4-B (T9 design contract): this is now a plain 1:1 projection. The old P2
// target inheritance is gone because design no longer supplies any target id —
// case_version.default_version_sut_id, case_version.version_sut_ids,
// case_step.target_version_sut_id/target_page_id/target_api_id and
// case_assertion.target_version_sut_id/database_id were all dropped in favour of
// prose (sut_description / target_description) plus covered_acs. A step says
// what it acts on and the executor locates it.
//
// Consequently snapshot.DefaultSUTID/SUTIDs are left zero/empty here: they are
// resolved from the version binding when a run is prepared (see
// executionapp.executionSUTBindings), which is the only place that still knows
// the bound SUT set. QA-F1's single-binding fallback lives there now.
func mapExecutableCase(view designapp.ExecutableCaseView) execution.CaseSnapshot {
	snapshot := execution.CaseSnapshot{
		CaseID:               view.CaseID,
		CaseVersionID:        view.CaseVersionID,
		RequirementID:        view.RequirementID,
		RequirementVersionID: view.RequirementVersionID,
		Title:                view.Title,
		Description:          view.Description,
		SUTDescription:       view.SutDescription,
		Preconditions:        view.Preconditions,
		CoveredACs:           append([]string(nil), view.CoveredACs...),
		Priority:             view.Priority,
		Steps:                make([]execution.CaseStep, 0, len(view.Steps)),
		Assertions:           make([]execution.CaseAssertion, 0, len(view.Assertions)),
	}
	for _, step := range view.Steps {
		snapshot.Steps = append(snapshot.Steps, execution.CaseStep{
			Seq:               step.Seq,
			Action:            step.Action,
			Expected:          step.Expected,
			TargetDescription: step.TargetDescription,
			AccountRole:       step.AccountRole,
			Data:              step.Data,
		})
	}
	for _, assertion := range view.Assertions {
		snapshot.Assertions = append(snapshot.Assertions, execution.CaseAssertion{
			CaseAssertionID: assertion.CaseAssertionID,
			StepSeq:         assertion.StepSeq,
			AssertionSeq:    assertion.AssertionSeq,
			Kind:            assertion.Kind,
			Expected:        assertion.Expected,
			SUTID:           assertion.SUTID,
		})
	}
	return snapshot
}
