//go:build integration

package designpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// seedVersion inserts one version row. v0.2.4-B: version.environment_id is NOT
// NULL (version_0016) and version_sut is gone, so the fixture creates the
// environment the version binds and points at it. The environment id is derived
// from the version id so a test that seeds several versions does not collide.
func seedVersion(t *testing.T, pool *pgxpool.Pool, versionID int64, autoDesign bool) {
	t.Helper()
	ctx := context.Background()
	environmentID := versionID*10 + 1
	if _, err := pool.Exec(ctx, `
		INSERT INTO environment (id, name, status) VALUES ($1, $2, 'active')
		ON CONFLICT (id) DO NOTHING
	`, environmentID, fmt.Sprintf("design-env-%d", environmentID)); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, status, auto_design_new_requirements, created_by, environment_id)
		VALUES ($1, 'P2 test version', 'initial', $2, 1, $3)
	`, versionID, autoDesign, environmentID); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

// seedApprovedPage creates one approved catalog page under the v0.2.4-B
// hierarchy (environment -> sut -> sut_page, resource_0004). It replaces the
// deleted seedVersionSUT: the discovery Critic ① still needs a real approved
// sut_page row to find, but that row is CATALOG state, not version scope -- there
// is no version_sut binding any more, and Critic ⑥ (target inside the version
// scope) was deleted by Lead ruling 7.
func seedApprovedPage(t *testing.T, pool *pgxpool.Pool, versionID, sutID, pageID int64) {
	t.Helper()
	ctx := context.Background()
	environmentID := versionID*10 + 1
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut (id, name, created_by, environment_id, base_url)
		VALUES ($1, $2, 1, $3, $4)
		ON CONFLICT (id) DO NOTHING
	`, sutID, fmt.Sprintf("design-sut-%d", sutID), environmentID,
		fmt.Sprintf("https://sut-%d.example.test", sutID)); err != nil {
		t.Fatalf("seed sut: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sut_page (id, sut_id, name, url, route_template, status)
		VALUES ($1, $2, $3, $4, $5, 'approved')
	`, pageID, sutID, fmt.Sprintf("page-%d", pageID),
		fmt.Sprintf("https://sut-%d.example.test/p%d", sutID, pageID),
		fmt.Sprintf("/p%d", pageID)); err != nil {
		t.Fatalf("seed approved page: %v", err)
	}
}

// seedRequirement inserts one requirement plus its current revision. The revision
// carries the v0.2.4-B natural-language contract fields (version_0017): a
// sut_description and a structured acceptance_criteria array, because
// design.RequirementRef.ACIDs is what a proposal's covered_acs is validated
// against. Two criteria with a two-digit id keep the numeric-suffix ordering
// honest.
func seedRequirement(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID int64, status, title string, revision int) {
	t.Helper()
	seedRequirementVersion(t, pool, versionID, requirementID, requirementVersionID, status, title, revision)
}

// defaultSeedCriteria is the AC document every seeded requirement revision gets
// unless a test overrides it: two criteria, one with a two-digit id so the
// numeric-suffix ordering stays honest.
const defaultSeedCriteria = `[{"id":"AC-1","given":"已注册","when":"提交账号密码","then":"登录成功"},
                              {"id":"AC-10","given":"连续失败","when":"再次提交","then":"账号锁定"}]`

func seedRequirementVersion(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID int64, status, title string, revision int) {
	t.Helper()
	seedRequirementRevision(t, pool, versionID, requirementID, requirementVersionID, status, title, revision, defaultSeedCriteria)
}

// seedRequirementRevision writes ONE requirement revision and points
// requirement.current_version_id at it. Calling it twice for the same requirement
// with a different revision id and a different criteria document reproduces the
// observable outcome of a requirement revision or a version-side Merge: the
// current AC set changes, and ids that the previous revision minted are no longer
// in it. That is the state the covered_acs linkage gate has to reject against, so
// tests can seed a real renumber instead of hand-setting ProposalContext.
func seedRequirementRevision(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID int64, status, title string, revision int, criteriaJSON string) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, current_version_id, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET current_version_id = EXCLUDED.current_version_id, status = EXCLUDED.status
	`, requirementID, versionID, requirementVersionID, status); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (
			id, requirement_id, revision, title, priority, sut_description,
			acceptance_criteria, content_hash
		) VALUES ($1, $2, $3, $4, 'P2', $5, $6::jsonb, $7)
	`, requirementVersionID, requirementID, revision, title,
		seedSutDescription, criteriaJSON, hash); err != nil {
		t.Fatalf("seed requirement_version: %v", err)
	}
}

func seedScenario(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID, scenarioID int64, name string, sortOrder int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_scenario (
			id, version_id, requirement_id, requirement_version_id, name, description, sort_order
		) VALUES ($1, $2, $3, $4, $5, '', $6)
	`, scenarioID, versionID, requirementID, requirementVersionID, name, sortOrder); err != nil {
		t.Fatalf("seed case_scenario: %v", err)
	}
}

// seedCase writes one logical case with a current version carrying the v0.2.4-B
// contract columns (sut_description / covered_acs / required preconditions) and
// one step. The stored content_hash is computed from the same shape the Go writer
// would build, so a reader that decodes the row and re-hashes it agrees.
func seedCase(t *testing.T, pool *pgxpool.Pool, versionID, requirementID, requirementVersionID, scenarioID, caseID, caseVersionID int64, revision int, status, title string) {
	t.Helper()
	hash, err := design.ComputeCaseContentHash(seededCaseVersion(caseVersionID, caseID, revision, status, title))
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
			id, case_id, revision, title, description, sut_description, preconditions,
			covered_acs, test_data, priority, status, content_hash
		) VALUES ($1, $2, $3, $4, '', $5, $6, $7::jsonb, '{}'::jsonb, 'P2', $8, $9)
	`, caseVersionID, caseID, revision, title,
		seedSutDescription, seedPreconditions, `["AC-1"]`, status, hash); err != nil {
		t.Fatalf("seed case_version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_step (id, case_version_id, seq, action, expected, target_description, data)
		VALUES ($1, $2, 1, $3, $4, $5, '{}'::jsonb)
	`, caseVersionID+100000, caseVersionID, "step "+title, "ok", seedTargetDescription); err != nil {
		t.Fatalf("seed case_step: %v", err)
	}
}

// The contract values every seeded case version shares. They are non-empty
// because §5.2 requires them, and a fixture that stored blanks would not survive
// the proposal gate it is meant to stand in for.
const (
	seedSutDescription    = "商城前台登录页与登录接口。"
	seedPreconditions     = "已有可用账号"
	seedTargetDescription = "登录页账号输入框"
)

// seededCaseVersion is the in-memory twin of the row seedCase writes, so the
// stored content_hash and the value a reader recomputes are the same hash.
func seededCaseVersion(caseVersionID, caseID int64, revision int, status, title string) design.CaseVersion {
	return design.CaseVersion{
		ID: caseVersionID, CaseID: caseID, Revision: revision, Status: status,
		Title: title, Priority: "P2",
		SutDescription: seedSutDescription,
		Preconditions:  seedPreconditions,
		CoveredACs:     []string{"AC-1"},
		Steps: []design.CaseStep{{
			Seq: 1, Action: "step " + title, Expected: "ok",
			TargetDescription: seedTargetDescription, Data: json.RawMessage(`{}`),
		}},
	}
}

func int64Ptr(value int64) *int64 { return &value }

// TestVersionContextIsExistenceGate pins what survives of VersionContext after
// v0.2.4-B (Lead ruling 9) and T27: the version id only. The four resource maps
// were deleted with the proposal ownership group; T27 then removed the leftover
// Status / AutoDesignNewRequirements fields because the only production caller
// (GraphService.Propose) discards the value and uses the read purely as a
// version-existence gate. The reflect checks below fail if any retired field is
// re-added, so the dead projection cannot silently come back.
func TestVersionContextIsExistenceGate(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, true)

	ctxView, err := repo.VersionContext(context.Background(), testVersionID)
	if err != nil {
		t.Fatalf("VersionContext: %v", err)
	}
	if ctxView.VersionID != testVersionID {
		t.Fatalf("version id = %d, want %d", ctxView.VersionID, testVersionID)
	}
	// Neither the retired resource maps nor the unconsumed state fields may come
	// back: design has no resource scope to check a proposal against, and the
	// existence gate reads no version state.
	for _, gone := range []string{"PageToSUT", "AllowedVersionSUTs", "APIToSUT", "DBTOSUT", "Status", "AutoDesignNewRequirements"} {
		if _, found := reflect.TypeOf(ctxView).FieldByName(gone); found {
			t.Fatalf("VersionContext re-gained the retired field %q", gone)
		}
	}

	if _, err := repo.VersionContext(context.Background(), testVersionID+99999); !errors.Is(err, design.ErrNotFound) {
		t.Fatalf("missing version err = %v, want ErrNotFound", err)
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

// seedExtraCaseVersion inserts one more case_version row of an existing case
// (no steps/assertions needed for the supersede predicate, which reads status
// only). The contract columns mirror seedCase so the row survives the design
// CHECK constraints; the content hash is computed from the same in-memory twin.
func seedExtraCaseVersion(t *testing.T, pool *pgxpool.Pool, caseID, caseVersionID int64, revision int, status, title string) {
	t.Helper()
	hash, err := design.ComputeCaseContentHash(seededCaseVersion(caseVersionID, caseID, revision, status, title))
	if err != nil {
		t.Fatalf("hash extra case version: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_version (
			id, case_id, revision, title, description, sut_description, preconditions,
			covered_acs, test_data, priority, status, content_hash
		) VALUES ($1, $2, $3, $4, '', $5, $6, $7::jsonb, '{}'::jsonb, 'P2', $8, $9)
	`, caseVersionID, caseID, revision, title,
		seedSutDescription, seedPreconditions, `["AC-1"]`, status, hash); err != nil {
		t.Fatalf("seed extra case_version: %v", err)
	}
}

// TestSupersedeListMatchesSupersedeWrite pins the H-G4 T2① single-source fix:
// SupersededVersionIDs (the preview) and SupersedeVersions (the write) must select
// the SAME supersedable status set. The expected set is derived from the domain
// constants (confirmed/stale), not hand-copied; a divergence between the two call
// sites (one hand-mirrored set drifting) fails the list==write equality.
func TestSupersedeListMatchesSupersedeWrite(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "登录", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "正常登录", 0)
	// Current draft (the kept version) + one version per non-draft status.
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, design.CaseStatusDraft, "草稿")
	seedExtraCaseVersion(t, pool, testCaseID, testCaseVersion+10, 2, design.CaseStatusConfirmed, "已确认")
	seedExtraCaseVersion(t, pool, testCaseID, testCaseVersion+20, 3, design.CaseStatusStale, "已过期")
	seedExtraCaseVersion(t, pool, testCaseID, testCaseVersion+30, 4, design.CaseStatusSuperseded, "已被替换")
	ctx := context.Background()

	// Expected supersedable set, derived from the domain authority: the versions
	// whose status is confirmed or stale, excluding the kept current version.
	supersedable := map[string]bool{design.CaseStatusConfirmed: true, design.CaseStatusStale: true}
	wantIDs := make([]int64, 0)
	rows, err := pool.Query(ctx, `SELECT id, status FROM case_version WHERE case_id = $1 AND id <> $2 ORDER BY id`, testCaseID, testCaseVersion)
	if err != nil {
		t.Fatalf("query case versions: %v", err)
	}
	for rows.Next() {
		var vid int64
		var status string
		if err := rows.Scan(&vid, &status); err != nil {
			rows.Close()
			t.Fatalf("scan case version: %v", err)
		}
		if supersedable[status] {
			wantIDs = append(wantIDs, vid)
		}
	}
	rows.Close()
	if len(wantIDs) != 2 {
		t.Fatalf("fixture precondition: want 2 supersedable versions, got %d", len(wantIDs))
	}

	var listed []int64
	err = repo.WithConfirmationLock(ctx, testVersionID, testReqID, func(ctx context.Context, tx design.ConfirmationTx) error {
		var listErr error
		listed, listErr = tx.SupersededVersionIDs(ctx, testCaseID, testCaseVersion)
		if listErr != nil {
			return listErr
		}
		return tx.SupersedeVersions(ctx, testCaseID, testCaseVersion, time.Now().UTC())
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if !reflect.DeepEqual(listed, wantIDs) {
		t.Fatalf("SupersededVersionIDs = %v, want the domain-derived supersedable set %v", listed, wantIDs)
	}
	// The write moved EXACTLY the listed versions to superseded; the kept draft
	// and the already-superseded version are untouched.
	for _, vid := range []int64{testCaseVersion, testCaseVersion + 10, testCaseVersion + 20, testCaseVersion + 30} {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM case_version WHERE id = $1`, vid).Scan(&status); err != nil {
			t.Fatalf("read version %d status: %v", vid, err)
		}
		switch vid {
		case testCaseVersion:
			if status != design.CaseStatusDraft {
				t.Fatalf("kept version status = %q, want draft", status)
			}
		case testCaseVersion + 30:
			if status != design.CaseStatusSuperseded {
				t.Fatalf("already-superseded version status = %q, want superseded", status)
			}
		default:
			if status != design.CaseStatusSuperseded {
				t.Fatalf("supersedable version %d status = %q, want superseded (list and write diverged)", vid, status)
			}
		}
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
	// Task 79: the planner derives the authoring claim priority from the
	// requirement priority, so the clarified projection must carry it.
	if clarified[0].Priority != "P2" {
		t.Fatalf("clarified priority = %q, want P2", clarified[0].Priority)
	}
	requirementContext, err := repo.RequirementContext(ctx, testVersionID, testReqID)
	if err != nil {
		t.Fatalf("RequirementContext: %v", err)
	}
	if requirementContext.Priority != "P2" {
		t.Fatalf("requirement context priority = %q, want P2", requirementContext.Priority)
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
	if coverage[0].Requirement.Priority != "P2" {
		t.Fatalf("coverage priority = %q, want P2", coverage[0].Requirement.Priority)
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
