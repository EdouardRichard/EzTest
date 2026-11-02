package designpg

import (
	"context"
	"fmt"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/jackc/pgx/v5"
)

// proposeTx is the design.Tx implementation backed by one pgx transaction.
type proposeTx struct {
	tx        pgx.Tx
	versionID int64
}

var _ design.Tx = (*proposeTx)(nil)

// WithRequirementLock runs fn in a transaction that holds the per
// (version, requirement) advisory lock for its whole duration.
func (r *Repository) WithRequirementLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, design.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin design proposal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockRequirement(ctx, tx, versionID, requirementID); err != nil {
		return err
	}
	if err := fn(ctx, &proposeTx{tx: tx, versionID: versionID}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit design proposal transaction: %w", err)
	}
	return nil
}

// CurrentGraphHash computes the graph hash from the transaction snapshot, so a
// base_graph_hash read can be revalidated atomically under the requirement lock.
func (t *proposeTx) CurrentGraphHash(ctx context.Context) (string, error) {
	graph, err := readGraph(ctx, t.tx, t.versionID)
	if err != nil {
		return "", err
	}
	return design.ComputeGraphHash(graph.Requirements)
}

// RequirementSnapshot loads the scenarios of one requirement version and the
// current version of every case in those scenarios.
func (t *proposeTx) RequirementSnapshot(ctx context.Context, versionID, requirementID, requirementVersionID int64) (design.RequirementSnapshot, error) {
	snapshot := design.RequirementSnapshot{RequirementVersionID: requirementVersionID}
	rows, err := t.tx.Query(ctx, `
		SELECT id, version_id, requirement_id, requirement_version_id, name, description,
		       COALESCE(version_sut_ids, '[]'::jsonb), COALESCE(page_ids, '[]'::jsonb),
		       COALESCE(api_ids, '[]'::jsonb), sort_order, created_at, updated_at
		FROM case_scenario
		WHERE version_id = $1 AND requirement_id = $2 AND requirement_version_id = $3
		ORDER BY sort_order, id
	`, versionID, requirementID, requirementVersionID)
	if err != nil {
		return design.RequirementSnapshot{}, fmt.Errorf("read requirement scenarios: %w", err)
	}
	for rows.Next() {
		scenario, err := scanScenario(rows)
		if err != nil {
			rows.Close()
			return design.RequirementSnapshot{}, err
		}
		snapshot.Scenarios = append(snapshot.Scenarios, scenario)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return design.RequirementSnapshot{}, fmt.Errorf("scan requirement scenarios: %w", err)
	}

	scenarioIDs := make([]int64, 0, len(snapshot.Scenarios))
	for _, scenario := range snapshot.Scenarios {
		scenarioIDs = append(scenarioIDs, scenario.ID)
	}
	if len(scenarioIDs) == 0 {
		snapshot.Cases = []design.CaseWithCurrent{}
		return snapshot, nil
	}
	caseRows, err := t.tx.Query(ctx, `
		SELECT ce.id, ce.scenario_id, ce.current_version_id, ce.status,
		       ce.created_by, ce.created_by_run_id, ce.created_at, ce.updated_at,
		       cv.id, cv.revision, cv.title, cv.description, cv.preconditions, cv.test_data, cv.priority,
		       cv.default_version_sut_id, COALESCE(cv.version_sut_ids, '[]'::jsonb),
		       COALESCE(cv.page_ids, '[]'::jsonb), COALESCE(cv.api_ids, '[]'::jsonb),
		       cv.status, cv.content_hash, cv.created_at,
		       (SELECT count(*) FROM case_step step WHERE step.case_version_id = cv.id),
		       (SELECT count(*) FROM case_assertion assertion WHERE assertion.case_version_id = cv.id),
		       (SELECT count(*) FROM case_source source WHERE source.case_version_id = cv.id)
		FROM case_entity ce
		JOIN case_version cv ON cv.id = ce.current_version_id
		WHERE ce.scenario_id = ANY($1)
		ORDER BY ce.id
	`, scenarioIDs)
	if err != nil {
		return design.RequirementSnapshot{}, fmt.Errorf("read requirement snapshot cases: %w", err)
	}
	for caseRows.Next() {
		item, err := scanCaseWithCurrent(caseRows)
		if err != nil {
			caseRows.Close()
			return design.RequirementSnapshot{}, err
		}
		snapshot.Cases = append(snapshot.Cases, item)
	}
	caseRows.Close()
	if err := caseRows.Err(); err != nil {
		return design.RequirementSnapshot{}, fmt.Errorf("scan requirement snapshot cases: %w", err)
	}
	if snapshot.Cases == nil {
		snapshot.Cases = []design.CaseWithCurrent{}
	}
	return snapshot, nil
}

// InsertScenario writes one scenario row.
func (t *proposeTx) InsertScenario(ctx context.Context, scenario design.Scenario) (design.Scenario, error) {
	suts, err := encodeIDArray(scenario.VersionSUTIDs)
	if err != nil {
		return design.Scenario{}, err
	}
	pages, err := encodeIDArray(scenario.PageIDs)
	if err != nil {
		return design.Scenario{}, err
	}
	apis, err := encodeIDArray(scenario.APIIDs)
	if err != nil {
		return design.Scenario{}, err
	}
	err = t.tx.QueryRow(ctx, `
		INSERT INTO case_scenario (
			id, version_id, requirement_id, requirement_version_id, name, description,
			version_sut_ids, page_ids, api_ids, sort_order, created_by, created_by_run_id,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9::jsonb,$10,$11,$12,$13,$14)
		RETURNING id
	`, scenario.ID, scenario.VersionID, scenario.RequirementID, scenario.RequirementVersionID,
		scenario.Name, scenario.Description, string(suts), string(pages), string(apis),
		scenario.SortOrder, scenario.CreatedBy, scenario.CreatedByRunID,
		scenario.CreatedAt, scenario.UpdatedAt).Scan(&scenario.ID)
	if err != nil {
		return design.Scenario{}, fmt.Errorf("insert scenario: %w", err)
	}
	return scenario, nil
}

// UpdateScenario rewrites the mutable scenario fields.
func (t *proposeTx) UpdateScenario(ctx context.Context, scenario design.Scenario) (design.Scenario, error) {
	suts, err := encodeIDArray(scenario.VersionSUTIDs)
	if err != nil {
		return design.Scenario{}, err
	}
	pages, err := encodeIDArray(scenario.PageIDs)
	if err != nil {
		return design.Scenario{}, err
	}
	apis, err := encodeIDArray(scenario.APIIDs)
	if err != nil {
		return design.Scenario{}, err
	}
	tag, err := t.tx.Exec(ctx, `
		UPDATE case_scenario
		SET name = $3, description = $4, version_sut_ids = $5::jsonb,
		    page_ids = $6::jsonb, api_ids = $7::jsonb, sort_order = $8, updated_at = $9
		WHERE id = $1 AND version_id = $2
	`, scenario.ID, scenario.VersionID, scenario.Name, scenario.Description,
		string(suts), string(pages), string(apis), scenario.SortOrder, scenario.UpdatedAt)
	if err != nil {
		return design.Scenario{}, fmt.Errorf("update scenario: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return design.Scenario{}, design.ErrNotFound
	}
	return scenario, nil
}

// InsertCase writes one logical case row.
func (t *proposeTx) InsertCase(ctx context.Context, entity design.CaseEntity) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO case_entity (
			id, scenario_id, current_version_id, status, created_by, created_by_run_id,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, entity.ID, entity.ScenarioID, entity.CurrentVersionID, entity.Status,
		entity.CreatedBy, entity.CreatedByRunID, entity.CreatedAt, entity.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert case entity: %w", err)
	}
	return nil
}

// InsertCaseVersion writes one immutable version and its children.
func (t *proposeTx) InsertCaseVersion(ctx context.Context, version design.CaseVersion) (design.CaseVersion, error) {
	testData, err := design.NormalizeJSONObject(version.TestData)
	if err != nil {
		return design.CaseVersion{}, err
	}
	suts, err := encodeIDArray(version.VersionSUTIDs)
	if err != nil {
		return design.CaseVersion{}, err
	}
	pages, err := encodeIDArray(version.PageIDs)
	if err != nil {
		return design.CaseVersion{}, err
	}
	apis, err := encodeIDArray(version.APIIDs)
	if err != nil {
		return design.CaseVersion{}, err
	}
	err = t.tx.QueryRow(ctx, `
		INSERT INTO case_version (
			id, case_id, revision, title, description, preconditions, test_data, priority,
			default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash,
			created_by, created_by_run_id, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10::jsonb,$11::jsonb,$12::jsonb,$13,$14,$15,$16,$17,$18)
		RETURNING id
	`, version.ID, version.CaseID, version.Revision, version.Title, version.Description,
		version.Preconditions, string(testData), version.Priority, version.DefaultVersionSUTID,
		string(suts), string(pages), string(apis), version.Status, version.ContentHash,
		version.CreatedBy, version.CreatedByRunID, version.CreatedAt, version.UpdatedAt).Scan(&version.ID)
	if err != nil {
		return design.CaseVersion{}, fmt.Errorf("insert case version: %w", err)
	}
	for index := range version.Steps {
		step := &version.Steps[index]
		step.CaseVersionID = version.ID
		data, err := design.NormalizeJSONObject(step.Data)
		if err != nil {
			return design.CaseVersion{}, err
		}
		step.Data = data
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO case_step (
				id, case_version_id, seq, action, expected, target_version_sut_id,
				target_page_id, target_api_id, account_role, data, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12)
		`, step.ID, step.CaseVersionID, step.Seq, step.Action, step.Expected,
			step.TargetVersionSUTID, step.TargetPageID, step.TargetAPIID, step.AccountRole,
			string(data), step.CreatedAt, step.CreatedAt); err != nil {
			return design.CaseVersion{}, fmt.Errorf("insert case step: %w", err)
		}
	}
	for index := range version.Assertions {
		assertion := &version.Assertions[index]
		assertion.CaseVersionID = version.ID
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO case_assertion (
				id, case_version_id, step_seq, kind, target, expected,
				target_version_sut_id, target_api_id, database_id, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`, assertion.ID, assertion.CaseVersionID, assertion.StepSeq, assertion.Kind,
			assertion.Target, assertion.Expected, assertion.TargetVersionSUTID,
			assertion.TargetAPIID, assertion.DatabaseID, assertion.CreatedAt, assertion.CreatedAt); err != nil {
			return design.CaseVersion{}, fmt.Errorf("insert case assertion: %w", err)
		}
	}
	for index := range version.Sources {
		source := &version.Sources[index]
		source.CaseVersionID = version.ID
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO case_source (
				id, case_version_id, kind, ref_id, path, line_no, sha, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, source.ID, source.CaseVersionID, source.Kind, source.RefID,
			source.Path, source.Line, source.SHA, source.CreatedAt, source.CreatedAt); err != nil {
			return design.CaseVersion{}, fmt.Errorf("insert case source: %w", err)
		}
	}
	return version, nil
}

// SetCurrentVersion moves the case's current pointer and status projection.
func (t *proposeTx) SetCurrentVersion(ctx context.Context, caseID, caseVersionID int64, status string, at time.Time) error {
	tag, err := t.tx.Exec(ctx, `
		UPDATE case_entity
		SET current_version_id = $2, status = $3, updated_at = $4
		WHERE id = $1
	`, caseID, caseVersionID, status, at)
	if err != nil {
		return fmt.Errorf("set case current version: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return design.ErrNotFound
	}
	return nil
}

// scanCaseWithCurrent scans the shared case+current-version projection.
func scanCaseWithCurrent(row pgx.Row) (design.CaseWithCurrent, error) {
	var item design.CaseWithCurrent
	var current design.CaseVersion
	var defaultSUT *int64
	var testData, suts, pages, apis []byte
	if err := row.Scan(
		&item.Case.ID, &item.Case.ScenarioID, &item.Case.CurrentVersionID, &item.Case.Status,
		&item.Case.CreatedBy, &item.Case.CreatedByRunID, &item.Case.CreatedAt, &item.Case.UpdatedAt,
		&current.ID, &current.Revision, &current.Title, &current.Description, &current.Preconditions,
		&testData, &current.Priority, &defaultSUT, &suts, &pages, &apis,
		&current.Status, &current.ContentHash, &current.CreatedAt,
		&current.StepCount, &current.AssertionCount, &current.SourceCount,
	); err != nil {
		return design.CaseWithCurrent{}, err
	}
	current.CaseID = item.Case.ID
	current.DefaultVersionSUTID = defaultSUT
	current.TestData = testData
	var err error
	if current.VersionSUTIDs, err = decodeIDArray(suts); err != nil {
		return design.CaseWithCurrent{}, err
	}
	if current.PageIDs, err = decodeIDArray(pages); err != nil {
		return design.CaseWithCurrent{}, err
	}
	if current.APIIDs, err = decodeIDArray(apis); err != nil {
		return design.CaseWithCurrent{}, err
	}
	item.Current = &current
	return item, nil
}

// caseWithCurrentQuery is the projection shared by confirmation/stale reads.
const caseWithCurrentQuery = `
	SELECT ce.id, ce.scenario_id, ce.current_version_id, ce.status,
	       ce.created_by, ce.created_by_run_id, ce.created_at, ce.updated_at,
	       cv.id, cv.revision, cv.title, cv.description, cv.preconditions, cv.test_data, cv.priority,
	       cv.default_version_sut_id, COALESCE(cv.version_sut_ids, '[]'::jsonb),
	       COALESCE(cv.page_ids, '[]'::jsonb), COALESCE(cv.api_ids, '[]'::jsonb),
	       cv.status, cv.content_hash, cv.created_at,
	       (SELECT count(*) FROM case_step step WHERE step.case_version_id = cv.id),
	       (SELECT count(*) FROM case_assertion assertion WHERE assertion.case_version_id = cv.id),
	       (SELECT count(*) FROM case_source source WHERE source.case_version_id = cv.id)
	FROM case_entity ce
	JOIN case_version cv ON cv.id = ce.current_version_id
	JOIN case_scenario s ON s.id = ce.scenario_id
`
