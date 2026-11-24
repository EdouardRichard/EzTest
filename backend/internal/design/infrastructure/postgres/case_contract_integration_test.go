//go:build integration

package designpg_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestCaseContractRoundTripAgainstRealSchema pins the v0.2.4-B case contract
// against the migrated schema (design_0004): every new column survives a
// write -> read round trip, and the content_hash stored on write is byte-identical
// to the one recomputed from what was read back.
//
// The second half is the load-bearing assertion. The Go struct, the INSERT column
// list, the SELECT column list and the hash payload are four separate places that
// must agree about the same contract; a mismatch in any of them (a column read
// into the wrong field, a scan order off by one, a new field left out of the
// payload) shows up here as a hash disagreement rather than as a silently wrong
// read.
func TestCaseContractRoundTripAgainstRealSchema(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := designpg.New(pool)
	ctx := context.Background()

	const (
		versionID   = int64(1001)
		requirement = int64(3001)
		reqVersion  = int64(3002)
		scenarioID  = int64(5001)
		caseID      = int64(7001)
	)
	seedVersion(t, pool, versionID, false)
	seedRequirement(t, pool, versionID, requirement, reqVersion, "clarified", "登录", 1)

	generator, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	now := time.Now().UTC()
	sutID := int64(9001)
	line := 42

	// Deliberately unsorted / duplicated / padded: the write path must normalize
	// covered_acs to the same canonical set the hash uses.
	written := design.CaseVersion{
		ID:             generator.Next(),
		CaseID:         caseID,
		Revision:       1,
		Title:          "账号密码登录成功",
		Description:    "使用有效账号登录",
		SutDescription: "  商城前台登录页与登录接口。  ",
		Preconditions:  "无",
		CoveredACs:     []string{" AC-10 ", "AC-1", "AC-10"},
		TestData:       json.RawMessage(`{"username":"admin"}`),
		Priority:       design.PriorityP1,
		Status:         design.CaseStatusDraft,
		Trigger:        design.CaseTriggerNormal,
		CreatedAt:      now,
		UpdatedAt:      now,
		Steps: []design.CaseStep{
			{
				ID: generator.Next(), Seq: 1, Action: "打开登录页", Expected: "显示登录表单",
				TargetDescription: "登录页账号输入框", AccountRole: "admin",
				Data: json.RawMessage(`{"k":"v"}`), CreatedAt: now,
			},
			{
				ID: generator.Next(), Seq: 2, Action: "提交账号密码", Expected: "接口返回成功",
				Data: json.RawMessage(`{}`), CreatedAt: now,
			},
		},
		Assertions: []design.CaseAssertion{
			{ID: generator.Next(), StepSeq: 1, Kind: design.AssertionUI, Target: "欢迎文案", Expected: "欢迎回来", CreatedAt: now},
			{ID: generator.Next(), StepSeq: 2, Kind: design.AssertionDB, Target: "登录审计行数", Expected: "1", SUTID: &sutID, CreatedAt: now},
		},
		Sources: []design.CaseSource{
			{ID: generator.Next(), Kind: design.SourceRequirement, RefID: "3002", Path: "requirement", CreatedAt: now},
			{ID: generator.Next(), Kind: design.SourceCode, RefID: "repo", Path: "internal/login.go", Line: &line, SHA: strings.Repeat("c", 40), CreatedAt: now},
		},
	}
	// Mirror what the propose write path does: normalize before hashing, so the
	// stored hash is the hash of the canonical values.
	written.SutDescription = strings.TrimSpace(written.SutDescription)
	written.CoveredACs = design.NormalizeACIDSet(written.CoveredACs)
	hash, err := design.ComputeCaseContentHash(written)
	if err != nil {
		t.Fatalf("ComputeCaseContentHash: %v", err)
	}
	written.ContentHash = hash

	err = repo.WithRequirementLock(ctx, versionID, requirement, func(ctx context.Context, tx design.Tx) error {
		if _, err := tx.InsertScenario(ctx, design.Scenario{
			ID: scenarioID, VersionID: versionID, RequirementID: requirement,
			RequirementVersionID: reqVersion, Name: "正常登录", Description: "正常路径",
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.InsertCase(ctx, design.CaseEntity{
			ID: caseID, ScenarioID: scenarioID, CurrentVersionID: &written.ID,
			Status: design.CaseStatusDraft, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		_, err := tx.InsertCaseVersion(ctx, written)
		return err
	})
	if err != nil {
		t.Fatalf("WithRequirementLock: %v", err)
	}

	caseVersionID := written.ID
	data, err := repo.ReadCase(ctx, versionID, caseID, &caseVersionID)
	if err != nil {
		t.Fatalf("ReadCase: %v", err)
	}
	read := data.Selected

	if read.SutDescription != "商城前台登录页与登录接口。" {
		t.Fatalf("sut_description = %q", read.SutDescription)
	}
	if got := strings.Join(read.CoveredACs, ","); got != "AC-1,AC-10" {
		t.Fatalf("covered_acs = %v, want [AC-1 AC-10] normalized", read.CoveredACs)
	}
	if read.Preconditions != "无" {
		t.Fatalf("preconditions = %q", read.Preconditions)
	}
	if len(read.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(read.Steps))
	}
	if read.Steps[0].TargetDescription != "登录页账号输入框" || read.Steps[0].AccountRole != "admin" {
		t.Fatalf("step 1 = %+v", read.Steps[0])
	}
	if read.Steps[1].TargetDescription != "" {
		t.Fatalf("step 2 target_description = %q, want empty (optional field)", read.Steps[1].TargetDescription)
	}
	if len(read.Assertions) != 2 {
		t.Fatalf("assertions = %d, want 2", len(read.Assertions))
	}
	var dbAssertion *design.CaseAssertion
	for index := range read.Assertions {
		if read.Assertions[index].Kind == design.AssertionDB {
			dbAssertion = &read.Assertions[index]
		}
	}
	if dbAssertion == nil {
		t.Fatalf("db assertion missing: %+v", read.Assertions)
	}
	if dbAssertion.SUTID == nil || *dbAssertion.SUTID != sutID {
		t.Fatalf("db assertion sut_id = %v, want %d (renamed from database_id)", dbAssertion.SUTID, sutID)
	}
	if len(read.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(read.Sources))
	}

	// The round-tripped row must re-hash to exactly the stored hash.
	recomputed, err := design.ComputeCaseContentHash(read)
	if err != nil {
		t.Fatalf("recompute hash: %v", err)
	}
	if recomputed != read.ContentHash {
		t.Fatalf("content_hash round trip mismatch:\n stored %s\n recomputed %s", read.ContentHash, recomputed)
	}
	if recomputed != hash {
		t.Fatalf("content_hash changed between write and read: %s -> %s", hash, recomputed)
	}
}

// TestCaseContractColumnsAreNotNullDefault pins the C10 lesson at the schema
// level: the new text/JSONB columns are NOT NULL with a default, so a writer that
// omits them (a legacy fixture, a hand-written INSERT, a future code path) stores
// an empty value instead of NULL. A NULL here would break every Go scanner that
// reads these columns into a plain string or []byte.
func TestCaseContractColumnsAreNotNullDefault(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	seedVersion(t, pool, 1001, false)
	seedRequirement(t, pool, 1001, 3001, 3002, "clarified", "登录", 1)
	seedScenario(t, pool, 1001, 3001, 3002, 5001, "正常登录", 0)

	// Insert a case version naming none of the new columns.
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES (7001, 5001, 7002, 'draft')
	`); err != nil {
		t.Fatalf("seed case entity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (id, case_id, revision, title, content_hash, status)
		VALUES (7002, 7001, 1, '最小用例', $1, 'draft')
	`, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("insert minimal case version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_step (id, case_version_id, seq, action)
		VALUES (7102, 7002, 1, '最小步骤')
	`); err != nil {
		t.Fatalf("insert minimal case step: %v", err)
	}

	var sutDescription, preconditions, coveredACs, targetDescription string
	if err := pool.QueryRow(ctx, `
		SELECT cv.sut_description, cv.preconditions, cv.covered_acs::text, st.target_description
		FROM case_version cv
		JOIN case_step st ON st.case_version_id = cv.id
		WHERE cv.id = 7002
	`).Scan(&sutDescription, &preconditions, &coveredACs, &targetDescription); err != nil {
		// A NULL in any of these columns fails the scan -- which is exactly the
		// C10 failure mode this test exists to prevent.
		t.Fatalf("scan defaults (a NULL here means the column is nullable): %v", err)
	}
	if sutDescription != "" || preconditions != "" || targetDescription != "" {
		t.Fatalf("defaults = sut_description %q preconditions %q target_description %q",
			sutDescription, preconditions, targetDescription)
	}
	if coveredACs != "[]" {
		t.Fatalf("covered_acs default = %s, want []", coveredACs)
	}

	// The renamed db-assertion column stays nullable: only kind=db requires it.
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected)
		VALUES (7202, 7002, 1, 'ui', '欢迎文案', '欢迎回来')
	`); err != nil {
		t.Fatalf("insert ui assertion without sut_id: %v", err)
	}
	var sutID *int64
	if err := pool.QueryRow(ctx, `SELECT sut_id FROM case_assertion WHERE id = 7202`).Scan(&sutID); err != nil {
		t.Fatalf("scan sut_id: %v", err)
	}
	if sutID != nil {
		t.Fatalf("sut_id = %v, want NULL for a non-db assertion", *sutID)
	}
}

// TestCaseContractColumnChecksRejectOverlongText pins that the upper bounds are
// enforced in the database as well as in the domain gate, so no caller can store
// more text than the §5.2 contract allows.
func TestCaseContractColumnChecksRejectOverlongText(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	seedVersion(t, pool, 1001, false)
	seedRequirement(t, pool, 1001, 3001, 3002, "clarified", "登录", 1)
	seedScenario(t, pool, 1001, 3001, 3002, 5001, "正常登录", 0)
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		VALUES (7001, 5001, NULL, 'draft')
	`); err != nil {
		t.Fatalf("seed case entity: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (id, case_id, revision, title, sut_description, content_hash, status)
		VALUES (7002, 7001, 1, '超长', $1, $2, 'draft')
	`, strings.Repeat("系", design.CaseSutDescriptionMax+1), strings.Repeat("a", 64)); err == nil {
		t.Fatalf("sut_description over %d chars was accepted", design.CaseSutDescriptionMax)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_version (id, case_id, revision, title, covered_acs, content_hash, status)
		VALUES (7003, 7001, 1, '非数组', '{"AC-1"}'::jsonb, $1, 'draft')
	`, strings.Repeat("b", 64)); err == nil {
		t.Fatal("a non-array covered_acs document was accepted")
	}
}

// TestGraphAndSnapshotProjectionsAgreeOnContract pins that the two independent
// read projections of the same row -- the graph card projection and the shared
// case+current projection used by the proposal snapshot -- decode the contract
// columns identically. They are separate SQL statements, so a column added to one
// and forgotten in the other is a real risk.
func TestGraphAndSnapshotProjectionsAgreeOnContract(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := designpg.New(pool)
	ctx := context.Background()

	seedVersion(t, pool, 1001, false)
	seedRequirement(t, pool, 1001, 3001, 3002, "clarified", "登录", 1)
	seedScenario(t, pool, 1001, 3001, 3002, 5001, "正常登录", 0)
	seedCase(t, pool, 1001, 3001, 3002, 5001, 7001, 7002, 1, design.CaseStatusDraft, "账号密码登录成功")

	graph, err := repo.ReadGraph(ctx, 1001)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	if len(graph.Requirements) != 1 || len(graph.Requirements[0].Scenarios) != 1 {
		t.Fatalf("graph = %+v", graph)
	}
	cases := graph.Requirements[0].Scenarios[0].Cases
	if len(cases) != 1 {
		t.Fatalf("graph cases = %+v", cases)
	}
	card := cases[0].Current
	if card.SutDescription != seedSutDescription {
		t.Fatalf("graph card sut_description = %q", card.SutDescription)
	}
	if len(card.CoveredACs) != 1 || card.CoveredACs[0] != "AC-1" {
		t.Fatalf("graph card covered_acs = %v", card.CoveredACs)
	}

	var snapshot design.RequirementSnapshot
	err = repo.WithRequirementLock(ctx, 1001, 3001, func(ctx context.Context, tx design.Tx) error {
		var err error
		snapshot, err = tx.RequirementSnapshot(ctx, 1001, 3001, 3002)
		return err
	})
	if err != nil {
		t.Fatalf("RequirementSnapshot: %v", err)
	}
	if len(snapshot.Cases) != 1 || snapshot.Cases[0].Current == nil {
		t.Fatalf("snapshot cases = %+v", snapshot.Cases)
	}
	snapshotted := snapshot.Cases[0].Current
	if snapshotted.SutDescription != card.SutDescription {
		t.Fatalf("snapshot sut_description %q != graph %q", snapshotted.SutDescription, card.SutDescription)
	}
	if strings.Join(snapshotted.CoveredACs, ",") != strings.Join(card.CoveredACs, ",") {
		t.Fatalf("snapshot covered_acs %v != graph %v", snapshotted.CoveredACs, card.CoveredACs)
	}
	if snapshotted.ContentHash != card.ContentHash {
		t.Fatalf("snapshot content_hash %q != graph %q", snapshotted.ContentHash, card.ContentHash)
	}
}

// TestProposalGateRejectsForeignACAgainstRealSchema pins the covered_acs linkage
// rule against the real AC-set read: design.RequirementRef.ACIDs comes out of
// requirement_version.acceptance_criteria, and a proposal naming an id that is
// not there is a 400. seedRequirementVersion writes AC-1 and AC-10.
//
// The second subtest is the one that matters for the cross-context invariant. It
// does NOT hand-set ProposalContext.RequirementACIDs; it seeds a real second
// revision of the requirement with a different criteria document and flips
// requirement.current_version_id onto it -- which is exactly the observable
// outcome of a requirement revision or of version-side Merge re-numbering an
// absorbed requirement's criteria. The AC set is then re-read through the real
// repository SQL. If that read were ever changed to resolve criteria against the
// scenario's pinned requirement_version_id instead of the requirement's CURRENT
// one ("to support designing against a pinned revision"), the gate would validate
// against the retired set, a merge-orphaned id would pass, and E's coverage union
// pre-check would see fabricated coverage -- with this test green. Pinning it
// through the real read is what makes that regression detectable.
func TestProposalGateRejectsForeignACAgainstRealSchema(t *testing.T) {
	t.Run("id outside the current set", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := designpg.New(pool)
		ctx := context.Background()

		seedVersion(t, pool, 1001, false)
		seedRequirement(t, pool, 1001, 3001, 3002, "clarified", "登录", 1)

		ref, err := repo.RequirementContext(ctx, 1001, 3001)
		if err != nil {
			t.Fatalf("RequirementContext: %v", err)
		}
		if len(ref.ACIDs) != 2 || ref.ACIDs[0] != "AC-1" || ref.ACIDs[1] != "AC-10" {
			t.Fatalf("ACIDs = %v, want [AC-1 AC-10] in numeric-suffix order", ref.ACIDs)
		}

		proposal := linkageProbeProposal(3002, ref.SutDescription, "AC-2")
		err = design.ValidateProposal(proposal, design.ProposalContext{
			VersionID: 1001, RequirementID: 3001, RequirementVersionID: 3002,
			RequirementACIDs: ref.ACIDs,
		})
		if err == nil {
			t.Fatal("a covered_acs id outside the requirement's AC set was accepted")
		}
		assertNamesOffendingAC(t, err, "AC-2")

		// The same proposal with an in-set id passes.
		proposal.Cases[0].CoveredACs = []string{"AC-10"}
		if err := design.ValidateProposal(proposal, design.ProposalContext{
			VersionID: 1001, RequirementID: 3001, RequirementVersionID: 3002,
			RequirementACIDs: ref.ACIDs,
		}); err != nil {
			t.Fatalf("an in-set AC id was rejected: %v", err)
		}
	})

	t.Run("id retired by a real renumber", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := designpg.New(pool)
		ctx := context.Background()

		seedVersion(t, pool, 1001, false)
		// Revision 1 mints AC-1 and AC-10.
		seedRequirement(t, pool, 1001, 3001, 3002, "clarified", "登录", 1)

		// Revision 2 replaces the criteria: AC-1 survives by identical content,
		// AC-10 is retired (an edited g/w/t holes the old id and mints a new one,
		// and a Merge re-numbers against the target's set the same way). Writing
		// this revision also moves requirement.current_version_id to 3003, which
		// is the only thing a consumer can observe.
		seedRequirementRevision(t, pool, 1001, 3001, 3003, "clarified", "登录", 2,
			`[{"id":"AC-1","given":"已注册","when":"提交账号密码","then":"登录成功"},
			  {"id":"AC-5","given":"已锁定","when":"等待解锁","then":"可重新登录"}]`)

		var currentVersionID int64
		if err := pool.QueryRow(ctx,
			`SELECT current_version_id FROM requirement WHERE id = 3001`).Scan(&currentVersionID); err != nil {
			t.Fatalf("read current_version_id: %v", err)
		}
		if currentVersionID != 3003 {
			t.Fatalf("current_version_id = %d, want 3003 (the renumber must be observable)", currentVersionID)
		}

		// The AC set is re-read through the real repository SQL, not hand-set.
		ref, err := repo.RequirementContext(ctx, 1001, 3001)
		if err != nil {
			t.Fatalf("RequirementContext: %v", err)
		}
		if ref.CurrentVersionID != 3003 {
			t.Fatalf("ref.CurrentVersionID = %d, want 3003", ref.CurrentVersionID)
		}
		if len(ref.ACIDs) != 2 || ref.ACIDs[0] != "AC-1" || ref.ACIDs[1] != "AC-5" {
			t.Fatalf("ACIDs after renumber = %v, want [AC-1 AC-5]", ref.ACIDs)
		}

		// A case still claiming AC-10 -- valid one revision ago, retired now --
		// must fail loudly and name the id.
		stale := linkageProbeProposal(3003, ref.SutDescription, "AC-10")
		err = design.ValidateProposal(stale, design.ProposalContext{
			VersionID: 1001, RequirementID: 3001, RequirementVersionID: 3003,
			RequirementACIDs: ref.ACIDs,
		})
		if err == nil {
			t.Fatal("a merge-retired covered_acs id was accepted; the coverage matrix would report a gap as covered")
		}
		assertNamesOffendingAC(t, err, "AC-10")
		if !strings.Contains(err.Error(), "renumbered by a merge") {
			t.Fatalf("error %q should name the renumber/merge cause", err)
		}

		// And the ids the new revision does carry are accepted, so the gate is
		// not simply rejecting everything after a renumber.
		for _, acID := range []string{"AC-1", "AC-5"} {
			fresh := linkageProbeProposal(3003, ref.SutDescription, acID)
			if err := design.ValidateProposal(fresh, design.ProposalContext{
				VersionID: 1001, RequirementID: 3001, RequirementVersionID: 3003,
				RequirementACIDs: ref.ACIDs,
			}); err != nil {
				t.Fatalf("%s is in the current AC set but was rejected: %v", acID, err)
			}
		}
	})
}

// linkageProbeProposal is a minimal otherwise-valid proposal whose only variable
// is the covered_acs id under test, so a rejection can only come from the
// linkage rule.
func linkageProbeProposal(requirementVersionID int64, sutDescription, coveredAC string) design.Proposal {
	return design.Proposal{
		RequirementID: 3001, RequirementVersionID: requirementVersionID,
		Scenarios: []design.ScenarioProposal{{Ref: "s1", Name: "正常登录"}},
		Cases: []design.CaseProposal{{
			Ref: "c1", ScenarioRef: "s1", Title: "账号密码登录成功",
			SutDescription: sutDescription, Preconditions: "无",
			CoveredACs: []string{coveredAC},
			Steps:      []design.StepProposal{{Seq: 1, Action: "打开登录页", Expected: "显示表单"}},
			Assertions: []design.AssertionProposal{{StepSeq: 1, Kind: design.AssertionUI, Target: "表单", Expected: "可见"}},
		}},
	}
}

func assertNamesOffendingAC(t *testing.T, err error, acID string) {
	t.Helper()
	if !strings.Contains(err.Error(), acID) {
		t.Fatalf("error %q should name the offending AC id %q", err, acID)
	}
}
