package designpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// VersionTitle returns the version title used by exports.
func (r *Repository) VersionTitle(ctx context.Context, versionID int64) (string, error) {
	var title string
	if err := r.pool.QueryRow(ctx, `SELECT title FROM version WHERE id = $1`, versionID).Scan(&title); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", design.ErrNotFound
		}
		return "", fmt.Errorf("read version title: %w", err)
	}
	return title, nil
}

// pgxQuerier is the read surface shared by the pool and a transaction.
type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ReadGraph assembles the full version design graph snapshot.
func (r *Repository) ReadGraph(ctx context.Context, versionID int64) (design.GraphData, error) {
	graph, err := readGraph(ctx, r.pool, versionID)
	if err != nil {
		return design.GraphData{}, err
	}
	if graph.PendingDesignCount, err = r.CountPendingDesign(ctx, versionID); err != nil {
		return design.GraphData{}, err
	}
	return graph, nil
}

func readGraph(ctx context.Context, q pgxQuerier, versionID int64) (design.GraphData, error) {
	graph := design.GraphData{VersionID: versionID, Requirements: []design.RequirementGraph{}}

	var autoDesign bool
	if err := q.QueryRow(ctx, `
		SELECT auto_design_new_requirements FROM version WHERE id = $1
	`, versionID).Scan(&autoDesign); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return design.GraphData{}, design.ErrNotFound
		}
		return design.GraphData{}, fmt.Errorf("read design graph version: %w", err)
	}
	graph.AutoDesignNewRequirements = autoDesign

	activeScopes, err := activeRequirementIDs(ctx, q, versionID)
	if err != nil {
		return design.GraphData{}, err
	}

	requirementIndex := map[int64]int{}
	// The requirement ref carries the SAME contract columns RequirementContext
	// reads (repo.go): priority, sut_description and the acceptance_criteria id
	// set. ACIDs is load-bearing for the judge scheduler's coverage precheck
	// (ensureJudgeTasks -> NeedsACRepair) and the shared review gate
	// (ReviewGateService -> MissingACs): an empty set here made CoverageComplete
	// trivially true, silently disabling the SC-E2 coverage gate and the SC-E4
	// AC-gap repair (T19 closed-loop integration caught it; the application unit
	// tests hand-populated ACIDs on their fakes and never exercised this reader).
	requirementRows, err := q.Query(ctx, `
		SELECT r.id, r.version_id, r.current_version_id, rv.revision, rv.title, r.status,
		       COALESCE(rv.priority, $3), COALESCE(rv.sut_description, ''),
		       COALESCE(rv.acceptance_criteria, '[]'::jsonb)
		FROM requirement r
		LEFT JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.version_id = $1
		  AND (r.status = $2
		       OR EXISTS (SELECT 1 FROM case_scenario s WHERE s.version_id = $1 AND s.requirement_id = r.id))
		ORDER BY r.id
	`, versionID, string(version.RequirementClarified), version.DefaultPriority)
	if err != nil {
		return design.GraphData{}, fmt.Errorf("read design graph requirements: %w", err)
	}
	for requirementRows.Next() {
		var ref design.RequirementRef
		var currentVersionID *int64
		var revision *int
		var title *string
		var criteria []byte
		if err := requirementRows.Scan(
			&ref.ID, &ref.VersionID, &currentVersionID, &revision, &title, &ref.Status,
			&ref.Priority, &ref.SutDescription, &criteria,
		); err != nil {
			requirementRows.Close()
			return design.GraphData{}, fmt.Errorf("scan design graph requirement: %w", err)
		}
		if currentVersionID != nil {
			ref.CurrentVersionID = *currentVersionID
		}
		if revision != nil {
			ref.Revision = *revision
		}
		if title != nil {
			ref.Title = *title
		}
		if ref.ACIDs, err = decodeACIDs(criteria); err != nil {
			requirementRows.Close()
			return design.GraphData{}, err
		}
		requirementIndex[ref.ID] = len(graph.Requirements)
		graph.Requirements = append(graph.Requirements, design.RequirementGraph{
			Requirement: ref,
			Scenarios:   []design.ScenarioGraph{},
		})
	}
	requirementRows.Close()
	if err := requirementRows.Err(); err != nil {
		return design.GraphData{}, fmt.Errorf("scan design graph requirements: %w", err)
	}

	type scenarioLocation struct {
		requirement int
		scenario    int
	}
	scenarioIndex := map[int64]scenarioLocation{}
	scenarioRows, err := q.Query(ctx, `
		SELECT id, version_id, requirement_id, requirement_version_id, name, description,
		       sort_order, created_at, updated_at
		FROM case_scenario
		WHERE version_id = $1
		ORDER BY requirement_id, sort_order, id
	`, versionID)
	if err != nil {
		return design.GraphData{}, fmt.Errorf("read design graph scenarios: %w", err)
	}
	for scenarioRows.Next() {
		scenario, err := scanScenario(scenarioRows)
		if err != nil {
			scenarioRows.Close()
			return design.GraphData{}, err
		}
		position, ok := requirementIndex[scenario.RequirementID]
		if !ok {
			continue
		}
		scenarioIndex[scenario.ID] = scenarioLocation{
			requirement: position,
			scenario:    len(graph.Requirements[position].Scenarios),
		}
		graph.Requirements[position].Scenarios = append(graph.Requirements[position].Scenarios, design.ScenarioGraph{
			Scenario: scenario,
			Cases:    []design.CaseGraph{},
		})
	}
	scenarioRows.Close()
	if err := scenarioRows.Err(); err != nil {
		return design.GraphData{}, fmt.Errorf("scan design graph scenarios: %w", err)
	}

	caseRows, err := q.Query(ctx, `
		SELECT ce.id, ce.scenario_id, ce.current_version_id, ce.status,
		       ce.created_by, ce.created_by_run_id, ce.created_at, ce.updated_at,
		       cv.id, cv.revision, cv.title, COALESCE(cv.description, ''), cv.priority,
		       cv.sut_description, COALESCE(cv.covered_acs, '[]'::jsonb),
		       cv.status, cv.content_hash, cv.created_at,
		       (SELECT count(*) FROM case_step st WHERE st.case_version_id = cv.id),
		       (SELECT count(*) FROM case_assertion a WHERE a.case_version_id = cv.id)
		FROM case_entity ce
		JOIN case_version cv ON cv.id = ce.current_version_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE s.version_id = $1
		ORDER BY ce.scenario_id, ce.id
	`, versionID)
	if err != nil {
		return design.GraphData{}, fmt.Errorf("read design graph cases: %w", err)
	}
	for caseRows.Next() {
		var item design.CaseGraph
		var current design.CaseVersion
		var coveredACs []byte
		if err := caseRows.Scan(
			&item.Case.ID, &item.Case.ScenarioID, &item.Case.CurrentVersionID, &item.Case.Status,
			&item.Case.CreatedBy, &item.Case.CreatedByRunID, &item.Case.CreatedAt, &item.Case.UpdatedAt,
			&current.ID, &current.Revision, &current.Title, &current.Description, &current.Priority,
			&current.SutDescription, &coveredACs, &current.Status, &current.ContentHash,
			&current.CreatedAt, &item.StepCount, &item.AssertionCount,
		); err != nil {
			caseRows.Close()
			return design.GraphData{}, fmt.Errorf("scan design graph case: %w", err)
		}
		if current.CoveredACs, err = decodeStringArray(coveredACs); err != nil {
			caseRows.Close()
			return design.GraphData{}, err
		}
		item.Current = current

		location, ok := scenarioIndex[item.Case.ScenarioID]
		if !ok {
			continue
		}
		target := &graph.Requirements[location.requirement].Scenarios[location.scenario]
		target.Cases = append(target.Cases, item)
		graph.Stats.Cases++
		switch current.Status {
		case design.CaseStatusDraft:
			graph.Stats.Draft++
		case design.CaseStatusConfirmed:
			graph.Stats.Confirmed++
		case design.CaseStatusStale:
			graph.Stats.Stale++
		}
	}
	caseRows.Close()
	if err := caseRows.Err(); err != nil {
		return design.GraphData{}, fmt.Errorf("scan design graph cases: %w", err)
	}

	graph.Stats.Requirements = len(graph.Requirements)
	for index := range graph.Requirements {
		requirement := &graph.Requirements[index]
		graph.Stats.Scenarios += len(requirement.Scenarios)
		requirement.DesignStatus = designStatus(*requirement, activeScopes)
	}
	return graph, nil
}

// activeRequirementIDs returns the requirement IDs with an active design scope.
func activeRequirementIDs(ctx context.Context, q pgxQuerier, versionID int64) (map[int64]struct{}, error) {
	rows, err := q.Query(ctx, `
		SELECT requirement_id FROM design_scope
		WHERE version_id = $1 AND status = $2
	`, versionID, design.ScopeStatusActive)
	if err != nil {
		return nil, fmt.Errorf("read active design scopes: %w", err)
	}
	defer rows.Close()
	active := map[int64]struct{}{}
	for rows.Next() {
		var requirementID int64
		if err := rows.Scan(&requirementID); err != nil {
			return nil, fmt.Errorf("scan active design scope: %w", err)
		}
		active[requirementID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan active design scopes: %w", err)
	}
	return active, nil
}

// designStatus projects the requirement node status. Priority order is
// stale > draft > confirmed > in_design, with pending for requirements that are
// not activated yet.
func designStatus(requirement design.RequirementGraph, activeScopes map[int64]struct{}) string {
	hasDraft, hasConfirmed, allConfirmed := false, false, true
	total := 0
	for _, scenario := range requirement.Scenarios {
		// Historical scenarios from older requirement versions are preserved for
		// audit/export, but they must not make the current requirement look stale
		// or draft forever. Only the current requirement version drives status.
		if scenario.Scenario.RequirementVersionID != requirement.Requirement.CurrentVersionID {
			continue
		}
		for _, item := range scenario.Cases {
			total++
			switch item.Current.Status {
			case design.CaseStatusStale:
				return design.CaseStatusStale
			case design.CaseStatusDraft:
				hasDraft = true
				allConfirmed = false
			case design.CaseStatusConfirmed:
				hasConfirmed = true
			}
		}
	}
	if hasDraft {
		return design.CaseStatusDraft
	}
	if hasConfirmed && allConfirmed {
		return design.CaseStatusConfirmed
	}
	if total == 0 {
		if requirement.Requirement.Status == string(version.RequirementClarified) {
			if _, activated := activeScopes[requirement.Requirement.ID]; !activated {
				return design.DesignStatusPending
			}
		}
	}
	return design.DesignStatusInDesign
}

// ReadCase returns the case detail with its version history and one selected
// version (the requested one or the current version).
func (r *Repository) ReadCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (design.CaseData, error) {
	var data design.CaseData
	err := r.pool.QueryRow(ctx, `
		SELECT ce.id, ce.scenario_id, ce.current_version_id, ce.status,
		       ce.created_by, ce.created_by_run_id, ce.created_at, ce.updated_at,
		       s.id, s.version_id, s.requirement_id, s.requirement_version_id, s.name, s.description,
		       s.sort_order, s.created_at, s.updated_at,
		       r.id, r.version_id, r.current_version_id, rv.revision, rv.title, r.status
		FROM case_entity ce
		JOIN case_scenario s ON s.id = ce.scenario_id
		JOIN requirement r ON r.id = s.requirement_id
		LEFT JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE ce.id = $1 AND s.version_id = $2
	`, caseID, versionID).Scan(
		&data.Case.ID, &data.Case.ScenarioID, &data.Case.CurrentVersionID, &data.Case.Status,
		&data.Case.CreatedBy, &data.Case.CreatedByRunID, &data.Case.CreatedAt, &data.Case.UpdatedAt,
		&data.Scenario.ID, &data.Scenario.VersionID, &data.Scenario.RequirementID,
		&data.Scenario.RequirementVersionID, &data.Scenario.Name, &data.Scenario.Description,
		&data.Scenario.SortOrder, &data.Scenario.CreatedAt, &data.Scenario.UpdatedAt,
		&data.Requirement.ID, &data.Requirement.VersionID, &data.Requirement.CurrentVersionID,
		&data.Requirement.Revision, &data.Requirement.Title, &data.Requirement.Status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return design.CaseData{}, design.ErrNotFound
	}
	if err != nil {
		return design.CaseData{}, fmt.Errorf("read case detail: %w", err)
	}

	versionRows, err := r.pool.Query(ctx, `
		SELECT cv.id, cv.revision, cv.status, cv.title, cv.created_at, cc.confirmed_at
		FROM case_version cv
		LEFT JOIN case_confirmation cc ON cc.case_version_id = cv.id
		WHERE cv.case_id = $1
		ORDER BY cv.revision DESC
	`, caseID)
	if err != nil {
		return design.CaseData{}, fmt.Errorf("read case versions: %w", err)
	}
	for versionRows.Next() {
		var summary design.CaseVersionSummary
		if err := versionRows.Scan(
			&summary.ID, &summary.Revision, &summary.Status, &summary.Title, &summary.CreatedAt, &summary.ConfirmedAt,
		); err != nil {
			versionRows.Close()
			return design.CaseData{}, fmt.Errorf("scan case version summary: %w", err)
		}
		data.Versions = append(data.Versions, summary)
	}
	versionRows.Close()
	if err := versionRows.Err(); err != nil {
		return design.CaseData{}, fmt.Errorf("scan case versions: %w", err)
	}

	selectedID := int64(0)
	if caseVersionID != nil {
		selectedID = *caseVersionID
	} else if data.Case.CurrentVersionID != nil {
		selectedID = *data.Case.CurrentVersionID
	}
	if selectedID > 0 {
		selected, err := loadCaseVersion(ctx, r.pool, selectedID)
		if err != nil {
			return design.CaseData{}, err
		}
		if selected.CaseID != caseID {
			return design.CaseData{}, design.ErrNotFound
		}
		data.Selected = selected
	}
	return data, nil
}

// Overview returns the design overview projection.
func (r *Repository) Overview(ctx context.Context, versionID int64) (design.OverviewData, error) {
	var overview design.OverviewData
	var autoDesign bool
	if err := r.pool.QueryRow(ctx, `
		SELECT auto_design_new_requirements FROM version WHERE id = $1
	`, versionID).Scan(&autoDesign); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return design.OverviewData{}, design.ErrNotFound
		}
		return design.OverviewData{}, fmt.Errorf("read design overview version: %w", err)
	}
	overview.AutoDesignNewRequirements = autoDesign
	pending, err := r.CountPendingDesign(ctx, versionID)
	if err != nil {
		return design.OverviewData{}, err
	}
	overview.PendingDesignCount = pending

	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM design_round WHERE version_id = $1
	`, versionID).Scan(&overview.RoundCount); err != nil {
		return design.OverviewData{}, fmt.Errorf("count design rounds: %w", err)
	}
	latest, err := r.GetLatestRound(ctx, versionID)
	if err != nil {
		return design.OverviewData{}, err
	}
	if latest != nil {
		overview.ActiveRound = latest
		var roundNo int
		if err := r.pool.QueryRow(ctx, `
			SELECT count(*) FROM design_round WHERE version_id = $1 AND id <= $2
		`, versionID, latest.ID).Scan(&roundNo); err != nil {
			return design.OverviewData{}, fmt.Errorf("count rounds before latest: %w", err)
		}
		overview.ActiveRoundNo = roundNo
	}
	return overview, nil
}

// loadCaseVersion loads one immutable version with its steps/assertions/sources.
func loadCaseVersion(ctx context.Context, q querier, caseVersionID int64) (design.CaseVersion, error) {
	var version design.CaseVersion
	var testData, coveredACs []byte
	var createdAt time.Time
	err := q.QueryRow(ctx, `
		SELECT id, case_id, revision, title, description, sut_description, preconditions,
		       COALESCE(covered_acs, '[]'::jsonb), test_data, priority,
		       status, content_hash, trigger, source_observation_id,
		       COALESCE(auto_confirmed_by, ''), rollback_of,
		       created_by, created_by_run_id, created_at, updated_at
		FROM case_version
		WHERE id = $1
	`, caseVersionID).Scan(
		&version.ID, &version.CaseID, &version.Revision, &version.Title, &version.Description,
		&version.SutDescription, &version.Preconditions, &coveredACs, &testData, &version.Priority,
		&version.Status, &version.ContentHash, &version.Trigger, &version.SourceObservationID,
		&version.AutoConfirmedBy, &version.RollbackOf,
		&version.CreatedBy, &version.CreatedByRunID, &createdAt, &version.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return design.CaseVersion{}, design.ErrNotFound
	}
	if err != nil {
		return design.CaseVersion{}, fmt.Errorf("read case version: %w", err)
	}
	version.CreatedAt = createdAt
	version.TestData = testData
	if version.CoveredACs, err = decodeStringArray(coveredACs); err != nil {
		return design.CaseVersion{}, err
	}
	if version.Steps, err = loadSteps(ctx, q, caseVersionID); err != nil {
		return design.CaseVersion{}, err
	}
	if version.Assertions, err = loadAssertions(ctx, q, caseVersionID); err != nil {
		return design.CaseVersion{}, err
	}
	if version.Sources, err = loadSources(ctx, q, caseVersionID); err != nil {
		return design.CaseVersion{}, err
	}
	return version, nil
}

func scanScenario(row pgx.Row) (design.Scenario, error) {
	var scenario design.Scenario
	if err := row.Scan(
		&scenario.ID, &scenario.VersionID, &scenario.RequirementID, &scenario.RequirementVersionID,
		&scenario.Name, &scenario.Description, &scenario.SortOrder,
		&scenario.CreatedAt, &scenario.UpdatedAt,
	); err != nil {
		return design.Scenario{}, fmt.Errorf("scan scenario: %w", err)
	}
	return scenario, nil
}

func loadSteps(ctx context.Context, q querier, caseVersionID int64) ([]design.CaseStep, error) {
	rows, err := q.Query(ctx, `
		SELECT id, case_version_id, seq, action, expected, target_description,
		       account_role, COALESCE(data, '{}'::jsonb), created_at
		FROM case_step
		WHERE case_version_id = $1
		ORDER BY seq
	`, caseVersionID)
	if err != nil {
		return nil, fmt.Errorf("read case steps: %w", err)
	}
	defer rows.Close()
	steps := make([]design.CaseStep, 0)
	for rows.Next() {
		var step design.CaseStep
		if err := rows.Scan(
			&step.ID, &step.CaseVersionID, &step.Seq, &step.Action, &step.Expected,
			&step.TargetDescription, &step.AccountRole, &step.Data, &step.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan case step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan case steps: %w", err)
	}
	return steps, nil
}

func loadAssertions(ctx context.Context, q querier, caseVersionID int64) ([]design.CaseAssertion, error) {
	rows, err := q.Query(ctx, `
		SELECT id, case_version_id, step_seq, kind, target, expected, sut_id, created_at
		FROM case_assertion
		WHERE case_version_id = $1
		ORDER BY step_seq, id
	`, caseVersionID)
	if err != nil {
		return nil, fmt.Errorf("read case assertions: %w", err)
	}
	defer rows.Close()
	assertions := make([]design.CaseAssertion, 0)
	for rows.Next() {
		var assertion design.CaseAssertion
		if err := rows.Scan(
			&assertion.ID, &assertion.CaseVersionID, &assertion.StepSeq, &assertion.Kind,
			&assertion.Target, &assertion.Expected, &assertion.SUTID, &assertion.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan case assertion: %w", err)
		}
		assertions = append(assertions, assertion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan case assertions: %w", err)
	}
	return assertions, nil
}

func loadSources(ctx context.Context, q querier, caseVersionID int64) ([]design.CaseSource, error) {
	rows, err := q.Query(ctx, `
		SELECT id, case_version_id, kind, ref_id, path, line_no, sha, created_at
		FROM case_source
		WHERE case_version_id = $1
		ORDER BY kind, id
	`, caseVersionID)
	if err != nil {
		return nil, fmt.Errorf("read case sources: %w", err)
	}
	defer rows.Close()
	sources := make([]design.CaseSource, 0)
	for rows.Next() {
		var source design.CaseSource
		if err := rows.Scan(
			&source.ID, &source.CaseVersionID, &source.Kind, &source.RefID,
			&source.Path, &source.Line, &source.SHA, &source.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan case source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan case sources: %w", err)
	}
	return sources, nil
}
