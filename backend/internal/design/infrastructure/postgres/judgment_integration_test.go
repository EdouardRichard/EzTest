//go:build integration

package designpg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newJudgmentStore opens an isolated migrated schema and builds the judgment
// store without a judge-exhausted adapter: the version-fact adapter is wired by
// the composition root (T13), so these tests pin the fail-closed delegation
// contract instead of a cross-context write.
func newJudgmentStore(t *testing.T) (*designpg.JudgmentStore, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	return designpg.NewJudgmentStore(pool, nil), pool
}

func judgmentRow(id, caseID, caseVersionID int64, round int, verdict string) design.Judgment {
	return design.Judgment{
		ID: id, VersionID: 1001, RequirementID: 10, CaseID: caseID,
		CaseVersionID: caseVersionID, ContentHash: strings.Repeat("a", 64),
		Round: round, Verdict: verdict,
	}
}

func TestJudgmentRepoRoundTripAndUnique(t *testing.T) {
	repo, _ := newJudgmentStore(t)
	ctx := context.Background()
	j := judgmentRow(1, 7001, 5, 1, design.VerdictPass)
	if err := repo.InsertJudgment(ctx, j); err != nil {
		t.Fatalf("insert judgment: %v", err)
	}
	// Same (case_id, case_version_id) again -> unique conflict.
	err := repo.InsertJudgment(ctx, j)
	if err == nil {
		t.Fatal("unique (case_id,case_version_id) not enforced")
	}
	if !errors.Is(err, design.ErrDuplicate) {
		t.Fatalf("duplicate must map to design.ErrDuplicate (409), got %v", err)
	}
	// A different case version at the SAME round must insert (host edit rejudge).
	j2 := j
	j2.ID = 2
	j2.CaseVersionID = 6
	if err := repo.InsertJudgment(ctx, j2); err != nil {
		t.Fatalf("different version same round must insert: %v", err)
	}
	got, err := repo.LatestJudgment(ctx, 7001)
	if err != nil || got == nil || got.CaseVersionID != 6 {
		t.Fatalf("latest = %+v err %v", got, err)
	}
	missing, err := repo.LatestJudgment(ctx, 9999)
	if err != nil || missing != nil {
		t.Fatalf("missing case must return nil,nil; got %+v err %v", missing, err)
	}
}

func TestJudgmentRepoFindingsRoundTrip(t *testing.T) {
	repo, _ := newJudgmentStore(t)
	ctx := context.Background()
	j := judgmentRow(1, 7001, 5, 1, design.VerdictFail)
	j.AgentRunID = 900
	j.CreatedAt = time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	j.Findings = []design.JudgmentFinding{
		{Dimension: "coverage", Severity: design.SeverityBlocker, Message: "AC-2 未被覆盖", CaseRef: "7001"},
		{Dimension: "executability", Severity: design.SeverityMinor, Message: "步骤顺序可优化"},
	}
	j.ACCoverageMatrix = map[string]any{"AC-1": []any{"7001"}, "AC-2": []any{}}
	if err := repo.InsertJudgment(ctx, j); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := repo.LatestJudgment(ctx, 7001)
	if err != nil || got == nil {
		t.Fatalf("latest: %+v err %v", got, err)
	}
	if got.AgentRunID != 900 || got.Verdict != design.VerdictFail || got.Round != 1 ||
		got.ContentHash != j.ContentHash || !got.CreatedAt.Equal(j.CreatedAt) {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if len(got.Findings) != 2 || got.Findings[0] != j.Findings[0] || got.Findings[1] != j.Findings[1] {
		t.Fatalf("findings round trip mismatch: %+v", got.Findings)
	}
	if len(got.ACCoverageMatrix) != 2 {
		t.Fatalf("coverage matrix round trip mismatch: %+v", got.ACCoverageMatrix)
	}
	// A judgment without findings/matrix persists the empty defaults.
	bare := judgmentRow(2, 7002, 8, 1, design.VerdictPass)
	if err := repo.InsertJudgment(ctx, bare); err != nil {
		t.Fatalf("insert bare: %v", err)
	}
	gotBare, err := repo.LatestJudgment(ctx, 7002)
	if err != nil || gotBare == nil {
		t.Fatalf("latest bare: %+v err %v", gotBare, err)
	}
	if len(gotBare.Findings) != 0 || gotBare.AgentRunID != 0 || gotBare.CreatedAt.IsZero() {
		t.Fatalf("bare judgment defaults wrong: %+v", gotBare)
	}
}

func TestJudgmentRepoListByRequirement(t *testing.T) {
	repo, _ := newJudgmentStore(t)
	ctx := context.Background()
	rows := []design.Judgment{
		judgmentRow(1, 7001, 5, 1, design.VerdictFail),
		judgmentRow(2, 7001, 6, 2, design.VerdictPass),
		judgmentRow(3, 7002, 7, 1, design.VerdictPass),
	}
	rows[2].RequirementID = 11 // another requirement of the same version
	other := judgmentRow(4, 7003, 9, 1, design.VerdictPass)
	other.VersionID = 1002 // another version, same requirement id
	rows = append(rows, other)
	for _, row := range rows {
		if err := repo.InsertJudgment(ctx, row); err != nil {
			t.Fatalf("insert %d: %v", row.ID, err)
		}
	}
	got, err := repo.ListJudgmentsByRequirement(ctx, 1001, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("list must scope to (version,requirement) in id order: %+v", got)
	}
	empty, err := repo.ListJudgmentsByRequirement(ctx, 1001, 99)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list = %+v err %v", empty, err)
	}
}

func TestJudgmentRepoOverrideRoundTrip(t *testing.T) {
	repo, _ := newJudgmentStore(t)
	ctx := context.Background()
	judgmentID := int64(2)
	first := design.Override{
		ID: 1, CaseID: 7001, CaseVersionID: 5, ContentHash: strings.Repeat("a", 64),
		JudgmentID: &judgmentID, Condition: designapp.OverrideConditionFailExhausted,
		ActorID: 42, Reason: "人工复核接受",
		CreatedAt: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC),
	}
	if err := repo.InsertOverride(ctx, first); err != nil {
		t.Fatalf("insert fail_exhausted override: %v", err)
	}
	second := design.Override{
		ID: 2, CaseID: 7001, CaseVersionID: 5, ContentHash: strings.Repeat("a", 64),
		Condition: designapp.OverrideConditionJudgeExhausted,
		ActorID:   43, Reason: "裁判不可用",
		CreatedAt: first.CreatedAt.Add(time.Minute),
	}
	if err := repo.InsertOverride(ctx, second); err != nil {
		t.Fatalf("insert judge_exhausted override with null judgment id: %v", err)
	}
	got, err := repo.LatestOverride(ctx, 7001, 5)
	if err != nil || got == nil {
		t.Fatalf("latest override: %+v err %v", got, err)
	}
	if got.ID != 2 || got.JudgmentID != nil || got.Condition != designapp.OverrideConditionJudgeExhausted ||
		got.ActorID != 43 || !got.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("override round trip mismatch: %+v", got)
	}
	// Version-scoped: another case version has no override.
	none, err := repo.LatestOverride(ctx, 7001, 6)
	if err != nil || none != nil {
		t.Fatalf("other version override = %+v err %v, want nil,nil", none, err)
	}
	// The earlier row keeps its judgment reference.
	older, err := repo.LatestOverride(ctx, 7001, 5)
	if err != nil || older == nil {
		t.Fatalf("re-read: %v", err)
	}
	bad := first
	bad.ID = 3
	bad.Condition = "whatever"
	err = repo.InsertOverride(ctx, bad)
	if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != 400 {
		t.Fatalf("invalid condition must be rejected with 400, got %v", err)
	}
}

func TestJudgmentRepoCountReworks(t *testing.T) {
	repo, _ := newJudgmentStore(t)
	ctx := context.Background()
	// No facts -> 0 reworks.
	count, err := repo.CountReworks(ctx, 7001)
	if err != nil || count != 0 {
		t.Fatalf("empty case reworks = %d err %v, want 0", count, err)
	}
	// Rounds 1,2,2: one rework; the same-round host-edit rejudge adds none.
	for _, row := range []design.Judgment{
		judgmentRow(1, 7001, 5, 1, design.VerdictFail),
		judgmentRow(2, 7001, 6, 2, design.VerdictFail),
		judgmentRow(3, 7001, 7, 2, design.VerdictPass),
	} {
		if err := repo.InsertJudgment(ctx, row); err != nil {
			t.Fatalf("insert %d: %v", row.ID, err)
		}
	}
	count, err = repo.CountReworks(ctx, 7001)
	if err != nil || count != 1 {
		t.Fatalf("reworks = %d err %v, want 1 (max round 2 - 1)", count, err)
	}
	// Another case is isolated.
	if err := repo.InsertJudgment(ctx, judgmentRow(4, 7002, 8, 1, design.VerdictPass)); err != nil {
		t.Fatalf("insert other case: %v", err)
	}
	count, err = repo.CountReworks(ctx, 7002)
	if err != nil || count != 0 {
		t.Fatalf("other case reworks = %d err %v, want 0", count, err)
	}
}

// stubExhaustedStore records the delegation arguments so the test can prove the
// store forwards to the port unchanged instead of answering by fiat.
type stubExhaustedStore struct {
	inserted [][4]int64
	present  map[[2]int64]bool
}

func (s *stubExhaustedStore) InsertJudgeExhausted(_ context.Context, versionID, requirementID, caseID, caseVersionID int64) (bool, error) {
	s.inserted = append(s.inserted, [4]int64{versionID, requirementID, caseID, caseVersionID})
	if s.present == nil {
		s.present = map[[2]int64]bool{}
	}
	key := [2]int64{caseID, caseVersionID}
	created := !s.present[key]
	s.present[key] = true
	return created, nil
}

func (s *stubExhaustedStore) HasJudgeExhausted(_ context.Context, caseID, caseVersionID int64) (bool, error) {
	return s.present[[2]int64{caseID, caseVersionID}], nil
}

func TestJudgmentRepoJudgeExhaustedDelegation(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	// Unwired store: both methods fail closed with the typed sentinel, never a
	// silent false/nil (constitution VII.6).
	unwired := designpg.NewJudgmentStore(pool, nil)
	if _, err := unwired.InsertJudgeExhausted(ctx, 1001, 10, 7001, 5); !errors.Is(err, designapp.ErrJudgeExhaustedStoreUnavailable) {
		t.Fatalf("unwired insert must fail closed, got %v", err)
	}
	if _, err := unwired.HasJudgeExhausted(ctx, 7001, 5); !errors.Is(err, designapp.ErrJudgeExhaustedStoreUnavailable) {
		t.Fatalf("unwired has must fail closed, got %v", err)
	}
	// Wired store: arguments AND the created flag forward unchanged.
	stub := &stubExhaustedStore{}
	wired := designpg.NewJudgmentStore(pool, stub)
	if created, err := wired.InsertJudgeExhausted(ctx, 1001, 10, 7001, 5); err != nil || !created {
		t.Fatalf("wired insert = %v/%v, want true/nil", created, err)
	}
	if created, err := wired.InsertJudgeExhausted(ctx, 1001, 10, 7001, 5); err != nil || created {
		t.Fatalf("wired re-insert = %v/%v, want false/nil (created flag must forward)", created, err)
	}
	if len(stub.inserted) != 2 || stub.inserted[0] != [4]int64{1001, 10, 7001, 5} {
		t.Fatalf("delegated args wrong: %+v", stub.inserted)
	}
	has, err := wired.HasJudgeExhausted(ctx, 7001, 5)
	if err != nil || !has {
		t.Fatalf("has = %v err %v, want true", has, err)
	}
	has, err = wired.HasJudgeExhausted(ctx, 7001, 6)
	if err != nil || has {
		t.Fatalf("other version has = %v err %v, want false", has, err)
	}
}

// Schema pin (controller ruling 3, carried from T2): the legacy expected-table
// list in migrate_integration_test.go is a non-exhaustive floor; this test is
// the real pin for both judgment tables, the unique key, both CHECKs and both
// indexes. CHECK value sets derive from the Go authorities, so a constant
// rename turns this test red instead of silently diverging from the DB.
func TestJudgmentSchemaPin(t *testing.T) {
	_, pool := newJudgmentStore(t)
	ctx := context.Background()

	judgmentRequireTable(t, pool, "design_judgment")
	judgmentRequireTable(t, pool, "design_judgment_override")
	judgmentRequireIndex(t, pool, "idx_design_judgment_version")
	judgmentRequireIndex(t, pool, "idx_design_judgment_override_case")
	judgmentRequireUnique(t, pool, "design_judgment_case_id_case_version_id_key")
	judgmentRequireCheckSet(t, pool, "design_judgment_verdict_check",
		[]string{design.VerdictPass, design.VerdictFail}, []string{"inconclusive", "blocked"})
	judgmentRequireCheckSet(t, pool, "design_judgment_override_condition_check",
		[]string{designapp.OverrideConditionFailExhausted, designapp.OverrideConditionJudgeExhausted},
		[]string{"manual", "override"})
	for _, column := range []string{"case_id", "case_version_id", "content_hash", "round", "findings", "ac_coverage_matrix"} {
		judgmentRequireColumn(t, pool, "design_judgment", column)
	}
	for _, column := range []string{"case_id", "case_version_id", "content_hash", "judgment_id", "condition", "actor_id", "reason"} {
		judgmentRequireColumn(t, pool, "design_judgment_override", column)
	}

	// Behavioral probes: the constraints actually reject, not just exist.
	if _, err := pool.Exec(ctx, `
		INSERT INTO design_judgment (id, version_id, requirement_id, case_id, case_version_id, content_hash, round, verdict)
		VALUES (9001, 1001, 10, 8001, 1, 'h', 1, 'maybe')
	`); err == nil {
		t.Fatal("verdict CHECK must reject a value outside pass/fail")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO design_judgment_override (id, case_id, case_version_id, content_hash, condition, actor_id, reason)
		VALUES (9002, 8001, 1, 'h', 'whatever', 42, 'r')
	`); err == nil {
		t.Fatal("condition CHECK must reject a value outside fail_exhausted/judge_exhausted")
	}
	insert := `
		INSERT INTO design_judgment (id, version_id, requirement_id, case_id, case_version_id, content_hash, round, verdict)
		VALUES ($1, 1001, 10, 8002, 1, 'h', $2, 'pass')
	`
	if _, err := pool.Exec(ctx, insert, int64(9003), 1); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := pool.Exec(ctx, insert, int64(9004), 2); err == nil {
		t.Fatal("UNIQUE(case_id,case_version_id) must reject a second fact for the same version")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO design_judgment (id, version_id, requirement_id, case_id, case_version_id, content_hash, round, verdict)
		VALUES (9005, 1001, 10, 8002, 2, 'h', 1, 'pass')
	`); err != nil {
		t.Fatalf("same round on a different case version must insert: %v", err)
	}
}

func judgmentRequireTable(t *testing.T, pool *pgxpool.Pool, table string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Errorf("check table %s: %v", table, err)
		return
	}
	if !exists {
		t.Errorf("missing table %s", table)
	}
}

func judgmentRequireIndex(t *testing.T, pool *pgxpool.Pool, index string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, index).Scan(&exists); err != nil {
		t.Errorf("check index %s: %v", index, err)
		return
	}
	if !exists {
		t.Errorf("missing index %s", index)
	}
}

func judgmentRequireUnique(t *testing.T, pool *pgxpool.Pool, constraint string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = $1 AND contype = 'u'
			  AND connamespace = current_schema()::regnamespace
		)
	`, constraint).Scan(&exists); err != nil {
		t.Errorf("check unique constraint %s: %v", constraint, err)
		return
	}
	if !exists {
		t.Errorf("missing unique constraint %s", constraint)
	}
}

func judgmentRequireColumn(t *testing.T, pool *pgxpool.Pool, table, column string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
		)
	`, table, column).Scan(&exists); err != nil {
		t.Errorf("check column %s.%s: %v", table, column, err)
		return
	}
	if !exists {
		t.Errorf("missing column %s.%s", table, column)
	}
}

func judgmentRequireCheckSet(t *testing.T, pool *pgxpool.Pool, constraint string, allowed, forbidden []string) {
	t.Helper()
	var def string
	if err := pool.QueryRow(context.Background(), `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = $1 AND contype = 'c'
		  AND connamespace = current_schema()::regnamespace
	`, constraint).Scan(&def); err != nil {
		t.Errorf("read check constraint %s: %v", constraint, err)
		return
	}
	for _, value := range allowed {
		if !strings.Contains(def, "'"+value+"'") {
			t.Errorf("check constraint %s misses %q: %s", constraint, value, def)
		}
	}
	for _, value := range forbidden {
		if strings.Contains(def, "'"+value+"'") {
			t.Errorf("check constraint %s still allows %q: %s", constraint, value, def)
		}
	}
}
