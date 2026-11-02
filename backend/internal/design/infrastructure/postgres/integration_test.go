//go:build integration

package designpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	testVersionID   int64 = 1001
	testReqID       int64 = 3001
	testReqVersion  int64 = 3002
	testScenarioID  int64 = 5001
	testCaseID      int64 = 7001
	testCaseVersion int64 = 7002
)

func newRepo(t *testing.T) (*designpg.Repository, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	return designpg.New(pool), pool
}

func seedVersion(t *testing.T, pool *pgxpool.Pool, versionID int64, autoDesign bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, title, status, auto_design_new_requirements, created_by)
		VALUES ($1, 'P2 test version', 'initial', $2, 1)
	`, versionID, autoDesign); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

func seedVersionSUT(t *testing.T, pool *pgxpool.Pool, versionID, versionSUTID int64, pages, apis []int64) {
	t.Helper()
	scope, err := json.Marshal(map[string]any{"pages": pages, "apis": apis})
	if err != nil {
		t.Fatalf("encode scope: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version_sut (id, version_id, sut_id, environment_id, scope)
		VALUES ($1, $2, $1, $1, $3::jsonb)
	`, versionSUTID, versionID, string(scope)); err != nil {
		t.Fatalf("seed version_sut: %v", err)
	}
}

func seedVersionDatabase(t *testing.T, pool *pgxpool.Pool, versionSUTID, databaseID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO version_sut_resource (id, version_sut_id, kind, resource_id, selected)
		VALUES ($1, $2, 'database', $3, true)
	`, databaseID+1, versionSUTID, databaseID); err != nil {
		t.Fatalf("seed version_sut_resource: %v", err)
	}
}

func seedRequirement(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID int64, status, title string, revision int) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, current_version_id, status, target_version_sut_ids)
		VALUES ($1, $2, $3, $4, '[]'::jsonb)
	`, requirementID, versionID, requirementVersionID, status); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, priority, content_hash)
		VALUES ($1, $2, $3, $4, 'P2', $5)
	`, requirementVersionID, requirementID, revision, title, hash); err != nil {
		t.Fatalf("seed requirement_version: %v", err)
	}
}

func seedScenario(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID, scenarioID int64, name string, sortOrder int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_scenario (
			id, version_id, requirement_id, requirement_version_id, name, description,
			version_sut_ids, page_ids, api_ids, sort_order
		) VALUES ($1, $2, $3, $4, $5, '', '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, $6)
	`, scenarioID, versionID, requirementID, requirementVersionID, name, sortOrder); err != nil {
		t.Fatalf("seed case_scenario: %v", err)
	}
}

func seedCase(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID, scenarioID, caseID, caseVersionID int64, revision int, status, title string) {
	t.Helper()
	hash, err := design.ComputeCaseContentHash(design.CaseVersion{
		Title: title, Priority: "P2", Status: status,
		VersionSUTIDs: []int64{9001}, DefaultVersionSUTID: int64Ptr(9001),
		Steps: []design.CaseStep{{Seq: 1, Action: "step " + title, Data: json.RawMessage(`{}`)}},
	})
	if err != nil {
		t.Fatalf("hash case: %v", err)
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES ($1, $2, $3, $4)
	`, caseID, scenarioID, caseVersionID, status); err != nil {
		t.Fatalf("seed case_entity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (
			id, case_id, revision, title, description, preconditions, test_data, priority,
			default_version_sut_id, version_sut_ids, page_ids, api_ids, status, content_hash
		) VALUES ($1, $2, $3, $4, '', '', '{}'::jsonb, 'P2', 9001, '["9001"]'::jsonb,
		          '[]'::jsonb, '[]'::jsonb, $5, $6)
	`, caseVersionID, caseID, revision, title, status, hash); err != nil {
		t.Fatalf("seed case_version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_step (id, case_version_id, seq, action, data)
		VALUES ($1, $2, 1, 'step', '{}'::jsonb)
	`, caseVersionID+100000, caseVersionID); err != nil {
		t.Fatalf("seed case_step: %v", err)
	}
}

func int64Ptr(value int64) *int64 { return &value }

func TestVersionContextTargetMaps(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, true)
	seedVersionSUT(t, pool, testVersionID, 9001, []int64{6001}, []int64{4001})
	seedVersionSUT(t, pool, testVersionID, 9002, []int64{6002}, []int64{4002})
	seedVersionDatabase(t, pool, 9002, 8001)

	ctxView, err := repo.VersionContext(context.Background(), testVersionID)
	if err != nil {
		t.Fatalf("VersionContext: %v", err)
	}
	if len(ctxView.AllowedVersionSUTs) != 2 {
		t.Fatalf("allowed SUTs = %v", ctxView.AllowedVersionSUTs)
	}
	if ctxView.PageToSUT[6001] != 9001 || ctxView.PageToSUT[6002] != 9002 {
		t.Fatalf("page map = %v", ctxView.PageToSUT)
	}
	if ctxView.APIToSUT[4002] != 9002 {
		t.Fatalf("api map = %v", ctxView.APIToSUT)
	}
	if ctxView.DBTOSUT[8001] != 9002 {
		t.Fatalf("database map = %v", ctxView.DBTOSUT)
	}
	if !ctxView.AutoDesignNewRequirements {
		t.Fatal("auto design flag not projected")
	}
}

func TestDesignRepoTxRollsBack(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "登录", 1)
	generator, _ := id.NewSnowflake(1)

	sentinel := errors.New("abort proposal")
	err := repo.WithRequirementLock(context.Background(), testVersionID, testReqID, func(ctx context.Context, tx design.Tx) error {
		now := time.Now().UTC()
		if _, err := tx.InsertScenario(ctx, design.Scenario{
			ID: generator.Next(), VersionID: testVersionID, RequirementID: testReqID,
			RequirementVersionID: testReqVersion, Name: "正常登录", SortOrder: 0,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithRequirementLock err = %v, want sentinel", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM case_scenario`).Scan(&count); err != nil {
		t.Fatalf("count scenarios: %v", err)
	}
	if count != 0 {
		t.Fatalf("scenario count = %d after rollback, want 0", count)
	}
}

func TestRequirementSnapshotNaturalKeysAndCurrentProjection(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "登录", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "正常登录", 0)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, design.CaseStatusDraft, "账号密码登录成功")

	var snapshot design.RequirementSnapshot
	err := repo.WithRequirementLock(context.Background(), testVersionID, testReqID, func(ctx context.Context, tx design.Tx) error {
		var err error
		snapshot, err = tx.RequirementSnapshot(ctx, testVersionID, testReqID, testReqVersion)
		return err
	})
	if err != nil {
		t.Fatalf("RequirementSnapshot: %v", err)
	}
	if len(snapshot.Scenarios) != 1 || snapshot.Scenarios[0].Name != "正常登录" {
		t.Fatalf("scenarios = %+v", snapshot.Scenarios)
	}
	if len(snapshot.Cases) != 1 || snapshot.Cases[0].Current == nil {
		t.Fatalf("cases = %+v", snapshot.Cases)
	}
	if snapshot.Cases[0].Current.ContentHash == "" || snapshot.Cases[0].Current.Revision != 1 {
		t.Fatalf("current version = %+v", snapshot.Cases[0].Current)
	}

	// The natural-key unique index rejects a duplicate scenario in the same
	// requirement version.
	_, err = pool.Exec(context.Background(), `
		INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name)
		VALUES (9999, $1, $2, $3, '正常登录')
	`, testVersionID, testReqID, testReqVersion)
	if err == nil {
		t.Fatal("duplicate scenario natural key was accepted")
	}
}

func TestConfirmAtomicAndIdempotent(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "登录", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "正常登录", 0)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, design.CaseStatusDraft, "账号密码登录成功")
	generator, _ := id.NewSnowflake(1)

	sentinel := errors.New("abort confirmation")
	err := repo.WithConfirmationLock(context.Background(), testVersionID, testReqID, func(ctx context.Context, tx design.ConfirmationTx) error {
		cases, err := tx.CasesByScenario(ctx, testScenarioID)
		if err != nil {
			return err
		}
		if len(cases) != 1 {
			t.Fatalf("cases = %d", len(cases))
		}
		now := time.Now().UTC()
		if err := tx.ConfirmCaseVersion(ctx, design.ConfirmCaseVersionCommand{
			Confirmation: design.Confirmation{
				ID: generator.Next(), CaseVersionID: cases[0].Current.ID,
				Scope: design.ConfirmScopeScenario, TargetScopeID: testScenarioID,
				BatchID: "batch_rollback", ConfirmedBy: 1, ConfirmedAt: now,
			},
			EntityStatus: design.CaseStatusConfirmed,
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithConfirmationLock err = %v, want sentinel", err)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM case_version WHERE id = $1`, testCaseVersion).Scan(&status); err != nil {
		t.Fatalf("read version status: %v", err)
	}
	if status != design.CaseStatusDraft {
		t.Fatalf("status after rollback = %q, want draft", status)
	}

	// A committed confirmation flips the version, entity and writes one row.
	err = repo.WithConfirmationLock(context.Background(), testVersionID, testReqID, func(ctx context.Context, tx design.ConfirmationTx) error {
		cases, err := tx.CasesByScenario(ctx, testScenarioID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.ConfirmCaseVersion(ctx, design.ConfirmCaseVersionCommand{
			Confirmation: design.Confirmation{
				ID: generator.Next(), CaseVersionID: cases[0].Current.ID,
				Scope: design.ConfirmScopeCase, TargetScopeID: testCaseID,
				BatchID: "batch_commit", ConfirmedBy: 1, ConfirmedAt: now,
				AffectedCaseVersionIDs: []string{id.Format(testCaseVersion)},
				Snapshot:               map[string]any{"title": "账号密码登录成功"},
			},
			EntityStatus: design.CaseStatusConfirmed,
		}); err != nil {
			return err
		}
		return tx.SupersedeVersions(ctx, testCaseID, cases[0].Current.ID, now)
	})
	if err != nil {
		t.Fatalf("commit confirmation: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT status FROM case_version WHERE id = $1`, testCaseVersion).Scan(&status); err != nil {
		t.Fatalf("read confirmed status: %v", err)
	}
	if status != design.CaseStatusConfirmed {
		t.Fatalf("status = %q, want confirmed", status)
	}

	// Confirming an already-confirmed version is a state conflict at the repo
	// layer; the application turns it into a skip.
	err = repo.WithConfirmationLock(context.Background(), testVersionID, testReqID, func(ctx context.Context, tx design.ConfirmationTx) error {
		return tx.ConfirmCaseVersion(ctx, design.ConfirmCaseVersionCommand{
			Confirmation: design.Confirmation{
				ID: generator.Next(), CaseVersionID: testCaseVersion, Scope: design.ConfirmScopeCase,
				TargetScopeID: testCaseID, BatchID: "batch_again", ConfirmedBy: 1, ConfirmedAt: time.Now().UTC(),
			},
			EntityStatus: design.CaseStatusConfirmed,
		})
	})
	if !errors.Is(err, design.ErrStateConflict) {
		t.Fatalf("repeat confirmation err = %v, want ErrStateConflict", err)
	}
}

func TestStalePropagationAndSafetyNet(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "登录", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "正常登录", 0)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, design.CaseStatusConfirmed, "账号密码登录成功")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE case_entity SET status = 'confirmed' WHERE id = $1`, testCaseID); err != nil {
		t.Fatalf("set entity confirmed: %v", err)
	}

	err := repo.WithStaleLock(ctx, testVersionID, testReqID, func(ctx context.Context, tx design.StaleTx) error {
		ref, err := tx.Requirement(ctx)
		if err != nil {
			return err
		}
		if ref.CurrentVersionID != testReqVersion {
			t.Fatalf("current version = %d", ref.CurrentVersionID)
		}
		cases, err := tx.CasesByRequirement(ctx)
		if err != nil {
			return err
		}
		if len(cases) != 1 || cases[0].Current.Status != design.CaseStatusConfirmed {
			t.Fatalf("cases = %+v", cases)
		}
		now := time.Now().UTC()
		if err := tx.MarkCaseVersionStale(ctx, cases[0].Current.ID, now); err != nil {
			return err
		}
		return tx.MarkCaseEntityStale(ctx, cases[0].Case.ID, now)
	})
	if err != nil {
		t.Fatalf("WithStaleLock: %v", err)
	}
	var versionStatus, entityStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM case_version WHERE id = $1`, testCaseVersion).Scan(&versionStatus); err != nil {
		t.Fatalf("read stale version: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM case_entity WHERE id = $1`, testCaseID).Scan(&entityStatus); err != nil {
		t.Fatalf("read stale entity: %v", err)
	}
	if versionStatus != design.CaseStatusStale || entityStatus != design.CaseStatusStale {
		t.Fatalf("stale projection = version %q entity %q", versionStatus, entityStatus)
	}

	// Idempotent: marking an already-stale version again is a no-op.
	err = repo.WithStaleLock(ctx, testVersionID, testReqID, func(ctx context.Context, tx design.StaleTx) error {
		return tx.MarkCaseVersionStale(ctx, testCaseVersion, time.Now().UTC())
	})
	if err != nil {
		t.Fatalf("repeat stale mark: %v", err)
	}

	// Safety net: move the requirement to a new revision while a confirmed case
	// still points at the old revision.
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, priority, content_hash)
		VALUES (4002, $1, 2, '登录 v2', 'P2', $2)
	`, testReqID, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed new requirement version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE requirement SET current_version_id = 4002, status = 'clarified' WHERE id = $1
	`, testReqID); err != nil {
		t.Fatalf("move requirement version: %v", err)
	}
	// Restore a confirmed current case so the safety net has work to do.
	if _, err := pool.Exec(ctx, `UPDATE case_version SET status = 'confirmed' WHERE id = $1`, testCaseVersion); err != nil {
		t.Fatalf("restore confirmed version: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE case_entity SET status = 'confirmed' WHERE id = $1`, testCaseID); err != nil {
		t.Fatalf("restore confirmed entity: %v", err)
	}
	outdated, err := repo.ListOutdatedConfirmedRequirements(ctx, 100)
	if err != nil {
		t.Fatalf("ListOutdatedConfirmedRequirements: %v", err)
	}
	if len(outdated) != 1 || outdated[0].RequirementID != testReqID || outdated[0].CurrentRequirementVersionID != 4002 {
		t.Fatalf("outdated = %+v", outdated)
	}
}

func TestPlannerQueriesAndCaseCounts(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "登录", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "正常登录", 0)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, design.CaseStatusDraft, "账号密码登录成功")
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID+1, testCaseVersion+1, 1, design.CaseStatusConfirmed, "账号锁定")
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID+2, testCaseVersion+2, 1, design.CaseStatusStale, "旧用例")
	ctx := context.Background()

	counts, err := repo.CaseCounts(ctx, testVersionID)
	if err != nil {
		t.Fatalf("CaseCounts: %v", err)
	}
	if counts.Draft != 1 || counts.Confirmed != 1 || counts.Stale != 1 {
		t.Fatalf("case counts = %+v", counts)
	}

	pending, err := repo.CountPendingDesign(ctx, testVersionID)
	if err != nil {
		t.Fatalf("CountPendingDesign: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
	candidates, err := repo.ListCandidateVersionIDs(ctx, 100)
	if err != nil {
		t.Fatalf("ListCandidateVersionIDs: %v", err)
	}
	if len(candidates) != 1 || candidates[0] != testVersionID {
		t.Fatalf("candidates = %v", candidates)
	}
	clarified, err := repo.ListClarifiedRequirements(ctx, testVersionID)
	if err != nil {
		t.Fatalf("ListClarifiedRequirements: %v", err)
	}
	if len(clarified) != 1 || clarified[0].CurrentVersionID != testReqVersion {
		t.Fatalf("clarified = %+v", clarified)
	}

	generator, _ := id.NewSnowflake(1)
	now := time.Now().UTC()
	err = repo.WithVersionLock(ctx, testVersionID, func(ctx context.Context) error {
		round, err := repo.CreateRound(ctx, design.Round{
			ID: generator.Next(), VersionID: testVersionID, ConversationID: 1111,
			Activation: design.ActivationManual, RequirementIDs: []int64{testReqID},
			RequirementVersionIDs: []int64{testReqVersion}, Status: design.RoundStatusActive,
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if _, created, err := repo.UpsertScope(ctx, design.Scope{
			ID: generator.Next(), VersionID: testVersionID, RequirementID: testReqID,
			ActivatedByRoundID: &round.ID, ActivatedAt: now, Status: design.ScopeStatusActive,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil || !created {
			return err
		}
		// Second upsert is idempotent.
		if _, created, err := repo.UpsertScope(ctx, design.Scope{
			ID: generator.Next(), VersionID: testVersionID, RequirementID: testReqID,
			ActivatedByRoundID: &round.ID, ActivatedAt: now, Status: design.ScopeStatusActive,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil || created {
			return err
		}
		return repo.UpdateRoundRequirements(ctx, round.ID, []int64{testReqID, testReqID + 1}, []int64{testReqVersion, 3003})
	})
	if err != nil {
		t.Fatalf("planner round/scope writes: %v", err)
	}

	pending, err = repo.CountPendingDesign(ctx, testVersionID)
	if err != nil {
		t.Fatalf("CountPendingDesign after scope: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending after activation = %d, want 0", pending)
	}
	latest, err := repo.GetLatestRound(ctx, testVersionID)
	if err != nil || latest == nil {
		t.Fatalf("GetLatestRound = %v, %v", latest, err)
	}
	if latest.ConversationID != 1111 || len(latest.RequirementIDs) != 2 {
		t.Fatalf("latest round = %+v", latest)
	}
	byConversation, err := repo.GetRoundByConversation(ctx, 1111)
	if err != nil || byConversation == nil || byConversation.ID != latest.ID {
		t.Fatalf("GetRoundByConversation = %+v, %v", byConversation, err)
	}
	rounds, err := repo.ListRounds(ctx, testVersionID)
	if err != nil || len(rounds) != 1 {
		t.Fatalf("ListRounds = %+v, %v", rounds, err)
	}

	coverage, err := repo.ListRequirementCoverage(ctx, testVersionID)
	if err != nil {
		t.Fatalf("ListRequirementCoverage: %v", err)
	}
	if len(coverage) != 1 || coverage[0].ScenarioCount != 1 || coverage[0].DraftCount != 1 ||
		coverage[0].ConfirmedCount != 1 || coverage[0].StaleCount != 1 {
		t.Fatalf("coverage = %+v", coverage)
	}
}

func TestReadGraphIncludesStaleAndCounts(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "登录", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "正常登录", 0)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, design.CaseStatusDraft, "账号密码登录成功")
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID+1, testCaseVersion+1, 1, design.CaseStatusStale, "旧用例")
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO design_scope (id, version_id, requirement_id, activated_at, status)
		VALUES (6001, $1, $2, now(), 'active')
	`, testVersionID, testReqID); err != nil {
		t.Fatalf("seed design scope: %v", err)
	}

	graph, err := repo.ReadGraph(context.Background(), testVersionID)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	if len(graph.Requirements) != 1 {
		t.Fatalf("requirements = %d", len(graph.Requirements))
	}
	requirement := graph.Requirements[0]
	if requirement.DesignStatus != design.CaseStatusStale {
		t.Fatalf("design status = %q, want stale", requirement.DesignStatus)
	}
	if graph.Stats.Scenarios != 1 || graph.Stats.Cases != 2 || graph.Stats.Draft != 1 || graph.Stats.Stale != 1 {
		t.Fatalf("stats = %+v", graph.Stats)
	}
	if graph.PendingDesignCount != 0 {
		t.Fatalf("pending = %d, want 0", graph.PendingDesignCount)
	}
	first, err := design.ComputeGraphHash(graph.Requirements)
	if err != nil {
		t.Fatalf("ComputeGraphHash: %v", err)
	}
	second, _ := design.ComputeGraphHash(graph.Requirements)
	if first != second || len(first) != 64 {
		t.Fatalf("graph hash not stable: %q vs %q", first, second)
	}

	detail, err := repo.ReadCase(context.Background(), testVersionID, testCaseID, nil)
	if err != nil {
		t.Fatalf("ReadCase: %v", err)
	}
	if detail.Selected.ID != testCaseVersion || detail.Selected.Steps == nil {
		t.Fatalf("selected = %+v", detail.Selected)
	}
	if len(detail.Versions) != 1 || detail.Versions[0].Revision != 1 {
		t.Fatalf("versions = %+v", detail.Versions)
	}

	overview, err := repo.Overview(context.Background(), testVersionID)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if overview.RoundCount != 0 || overview.ActiveRound != nil {
		t.Fatalf("overview = %+v", overview)
	}
}

func TestDataPlanRevision(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "登录", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "正常登录", 0)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, design.CaseStatusDraft, "账号密码登录成功")
	ctx := context.Background()

	current, err := repo.CurrentCaseVersion(ctx, testVersionID, testCaseVersion)
	if err != nil {
		t.Fatalf("CurrentCaseVersion: %v", err)
	}
	generator, _ := id.NewSnowflake(1)
	now := time.Now().UTC()
	preconditions := "数据准备完成"
	revision := current
	revision.ID = generator.Next()
	revision.Revision = current.Revision + 1
	revision.Preconditions = preconditions
	revision.TestData = json.RawMessage(`{"user":"admin"}`)
	for index := range revision.Steps {
		revision.Steps[index].ID = generator.Next()
	}
	for index := range revision.Assertions {
		revision.Assertions[index].ID = generator.Next()
	}
	for index := range revision.Sources {
		revision.Sources[index].ID = generator.Next()
	}
	hash, err := design.ComputeCaseContentHash(revision)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash: %v", err)
	}
	revision.ContentHash = hash
	revision.Status = design.CaseStatusDraft
	revision.CreatedAt = now
	revision.UpdatedAt = now

	stored, err := repo.InsertDataPlanRevision(ctx, testVersionID, testCaseVersion, revision, now)
	if err != nil {
		t.Fatalf("InsertDataPlanRevision: %v", err)
	}
	if stored.Revision != 2 || stored.Preconditions != preconditions {
		t.Fatalf("stored revision = %+v", stored)
	}
	var currentVersionID int64
	if err := pool.QueryRow(ctx, `SELECT current_version_id FROM case_entity WHERE id = $1`, testCaseID).Scan(&currentVersionID); err != nil {
		t.Fatalf("read current: %v", err)
	}
	if currentVersionID != stored.ID {
		t.Fatalf("current version = %d, want %d", currentVersionID, stored.ID)
	}
	var stepCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM case_step WHERE case_version_id = $1`, stored.ID).Scan(&stepCount); err != nil {
		t.Fatalf("count copied steps: %v", err)
	}
	if stepCount != len(current.Steps) {
		t.Fatalf("copied steps = %d, want %d", stepCount, len(current.Steps))
	}

	// A stale base revision is rejected.
	_, err = repo.InsertDataPlanRevision(ctx, testVersionID, testCaseVersion, revision, now)
	if !errors.Is(err, design.ErrStateConflict) {
		t.Fatalf("stale data-plan base err = %v, want ErrStateConflict", err)
	}
}
