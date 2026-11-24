//go:build integration

package executionpg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestEvidenceRefReadByOwner pins the production read side of the evidence_ref
// fact table (v0.2.4-G V-0019, SC-G9): an owner_kind/owner_id reverse-lookup
// returns the evidence bound to that owner, joined with its immutable metadata.
// Until now evidence_ref was write-only (InsertRef, no SELECT). The three report
// owner kinds (case_run / step_result / assertion_result) are exercised against
// genuine result rows, and the read must isolate owners by BOTH kind and id.
func TestEvidenceRefReadByOwner(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	mustCreateWave(t, repo, 50, 100)
	mustCreateCaseRun(t, repo, 500, 50, 1, 300)

	// Genuine step_result / assertion_result owners. owner_id is a logical
	// reference (no cross-table FK on evidence_ref.owner_id), but binding to real
	// result rows exercises the reverse-lookup the report actually performs.
	now := time.Now().UTC()
	step := execution.StepResult{
		ID: 6001, CaseRunID: 500, Seq: 1, Status: execution.StepPassed,
		CreatedAt: now, UpdatedAt: now,
	}
	if ok, err := repo.Results.UpsertStep(ctx, step, 0, ""); err != nil || !ok {
		t.Fatalf("upsert step: ok=%v err=%v", ok, err)
	}
	assertion := execution.AssertionResult{
		ID: 6002, CaseRunID: 500, StepSeq: 1, AssertionSeq: 1,
		Kind: execution.AssertionKindUI, Verdict: execution.AssertionFailed,
		Expected: "a", Actual: "b", CreatedAt: now, UpdatedAt: now,
	}
	if ok, err := repo.Results.UpsertAssertion(ctx, assertion, 0, ""); err != nil || !ok {
		t.Fatalf("upsert assertion: ok=%v err=%v", ok, err)
	}

	// Three immutable evidence rows with distinct content-addressed metadata.
	shaConsole := strings.Repeat("b", 64)
	shaNetwork := strings.Repeat("c", 64)
	evShot := execution.Evidence{
		ID: 700, CaseRunID: 500, SUTID: ptrInt64(5001), StepSeq: ptrInt(1),
		Kind: execution.EvidenceScreenshot, ContentRefID: 42, SHA256: testFingerprint,
		SizeBytes: 100, MIME: "image/png", Filename: "shot.png", CreatedAt: now,
	}
	evConsole := execution.Evidence{
		ID: 701, CaseRunID: 500, Kind: execution.EvidenceConsole, ContentRefID: 43,
		SHA256: shaConsole, SizeBytes: 200, MIME: "application/json", CreatedAt: now,
	}
	evNetwork := execution.Evidence{
		ID: 702, CaseRunID: 500, StepSeq: ptrInt(1), Kind: execution.EvidenceNetwork,
		ContentRefID: 44, SHA256: shaNetwork, SizeBytes: 300, MIME: "application/json", CreatedAt: now,
	}
	for _, ev := range []execution.Evidence{evShot, evConsole, evNetwork} {
		if err := repo.Evidence.Insert(ctx, ev); err != nil {
			t.Fatalf("insert evidence %d: %v", ev.ID, err)
		}
	}

	// case_run 500 owns shot+console; step 6001 owns network; assertion 6002 owns
	// the SAME shot evidence under a different owner/role (an evidence row may be
	// bound to several owners).
	refs := []execution.EvidenceRef{
		{ID: 800, EvidenceID: 700, OwnerKind: execution.EvidenceOwnerCaseRun, OwnerID: 500, Role: "screenshot"},
		{ID: 801, EvidenceID: 701, OwnerKind: execution.EvidenceOwnerCaseRun, OwnerID: 500, Role: "console"},
		{ID: 802, EvidenceID: 702, OwnerKind: execution.EvidenceOwnerStepResult, OwnerID: 6001, Role: "attachment"},
		{ID: 803, EvidenceID: 700, OwnerKind: execution.EvidenceOwnerAssertionResult, OwnerID: 6002, Role: "failure"},
	}
	for _, ref := range refs {
		if err := repo.Evidence.InsertRef(ctx, ref); err != nil {
			t.Fatalf("insert evidence ref %d: %v", ref.ID, err)
		}
	}

	// case_run reverse-lookup: two links in append order (id asc), each joined
	// with its evidence metadata.
	byRun, err := repo.Evidence.ListByOwner(ctx, execution.EvidenceOwnerCaseRun, 500)
	if err != nil {
		t.Fatalf("ListByOwner(case_run,500): %v", err)
	}
	if len(byRun) != 2 {
		t.Fatalf("case_run refs = %d, want 2", len(byRun))
	}
	if byRun[0].Ref.ID != 800 || byRun[1].Ref.ID != 801 {
		t.Fatalf("refs must be append order (id asc): %d then %d", byRun[0].Ref.ID, byRun[1].Ref.ID)
	}
	first := byRun[0]
	if first.Ref.OwnerKind != execution.EvidenceOwnerCaseRun || first.Ref.OwnerID != 500 ||
		first.Ref.EvidenceID != 700 || first.Ref.Role != "screenshot" {
		t.Fatalf("link round-trip = %+v", first.Ref)
	}
	if first.Evidence.ID != 700 || first.Evidence.SHA256 != testFingerprint ||
		first.Evidence.SizeBytes != 100 || first.Evidence.Kind != execution.EvidenceScreenshot ||
		first.Evidence.MIME != "image/png" || first.Evidence.Filename != "shot.png" ||
		first.Evidence.CaseRunID != 500 {
		t.Fatalf("joined evidence metadata = %+v", first.Evidence)
	}
	if first.Evidence.SUTID == nil || *first.Evidence.SUTID != 5001 ||
		first.Evidence.StepSeq == nil || *first.Evidence.StepSeq != 1 {
		t.Fatalf("joined evidence nullable dims = sut %v step %v", first.Evidence.SUTID, first.Evidence.StepSeq)
	}
	second := byRun[1]
	if second.Evidence.ID != 701 || second.Evidence.SHA256 != shaConsole ||
		second.Evidence.SizeBytes != 200 || second.Evidence.Kind != execution.EvidenceConsole {
		t.Fatalf("second joined evidence = %+v", second.Evidence)
	}

	// step_result reverse-lookup: only the network evidence bound to step 6001.
	byStep, err := repo.Evidence.ListByOwner(ctx, execution.EvidenceOwnerStepResult, 6001)
	if err != nil {
		t.Fatalf("ListByOwner(step_result,6001): %v", err)
	}
	if len(byStep) != 1 {
		t.Fatalf("step_result refs = %d, want 1", len(byStep))
	}
	if byStep[0].Ref.OwnerKind != execution.EvidenceOwnerStepResult || byStep[0].Ref.OwnerID != 6001 ||
		byStep[0].Evidence.ID != 702 || byStep[0].Evidence.Kind != execution.EvidenceNetwork ||
		byStep[0].Evidence.SHA256 != shaNetwork || byStep[0].Evidence.SizeBytes != 300 {
		t.Fatalf("step_result joined ref = %+v", byStep[0])
	}

	// assertion_result reverse-lookup: the same shot evidence, different owner/role.
	byAssertion, err := repo.Evidence.ListByOwner(ctx, execution.EvidenceOwnerAssertionResult, 6002)
	if err != nil {
		t.Fatalf("ListByOwner(assertion_result,6002): %v", err)
	}
	if len(byAssertion) != 1 {
		t.Fatalf("assertion_result refs = %d, want 1", len(byAssertion))
	}
	if byAssertion[0].Ref.Role != "failure" || byAssertion[0].Ref.OwnerID != 6002 ||
		byAssertion[0].Evidence.ID != 700 || byAssertion[0].Evidence.SHA256 != testFingerprint {
		t.Fatalf("assertion_result joined ref = %+v", byAssertion[0])
	}

	// Owner isolation by id: an unrelated owner_id returns an empty, non-nil slice.
	empty, err := repo.Evidence.ListByOwner(ctx, execution.EvidenceOwnerCaseRun, 999999)
	if err != nil {
		t.Fatalf("ListByOwner(case_run,999999): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("unrelated owner must return empty non-nil slice, got %+v", empty)
	}
	// Owner isolation by kind: the case_run id under the step_result kind matches
	// nothing (owner_kind and owner_id are both part of the lookup key).
	crossKind, err := repo.Evidence.ListByOwner(ctx, execution.EvidenceOwnerStepResult, 500)
	if err != nil {
		t.Fatalf("ListByOwner(step_result,500): %v", err)
	}
	if len(crossKind) != 0 {
		t.Fatalf("owner_kind must isolate the lookup, got %+v", crossKind)
	}
}

// TestIntentGetByIDReturnsFrozenFingerprint pins the per-run actual-fingerprint
// read port (v0.2.4-G SC-G5/G6): GetByID returns the SPECIFIC intent a run
// executed under, with the environment_fingerprint / code_baseline frozen at
// intent creation (F semantics). It must read the stored columns back verbatim —
// never recompute or live-read the current environment, and never return another
// intent's fingerprint. ListActive cannot serve this: a report needs the exact
// intent behind case_run.IntentID, including terminal intents.
func TestIntentGetByIDReturnsFrozenFingerprint(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	const (
		frozenFP       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		frozenBaseline = "9cad0ddddeadbeefcafebabe0123456789abcdef"
		otherFP        = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
		otherBaseline  = "1111111122222222333333334444444455555555"
	)

	// Intent A: the environment/code boundary frozen when the run was planned.
	intentA := execution.ExecutionIntent{
		ID: 9101, VersionID: 100, CaseVersionID: 300, SUTIDs: []int64{5001},
		EnvironmentFingerprint: frozenFP, CodeBaseline: frozenBaseline,
		Purpose: execution.PurposeInitial, Status: execution.IntentOpen,
		DedupeKey: "exec:frozen-a", AttemptNo: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Intents.Create(ctx, intentA); err != nil {
		t.Fatalf("create intent A: %v", err)
	}
	// Intent B: a later retest of the SAME case version under a CHANGED
	// environment/baseline. Its existence must not alter what GetByID(A) returns.
	intentB := execution.ExecutionIntent{
		ID: 9102, VersionID: 100, CaseVersionID: 300, SUTIDs: []int64{5001},
		EnvironmentFingerprint: otherFP, CodeBaseline: otherBaseline,
		Purpose: execution.PurposeRetest, Status: execution.IntentOpen,
		DedupeKey: "exec:frozen-b", AttemptNo: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Intents.Create(ctx, intentB); err != nil {
		t.Fatalf("create intent B: %v", err)
	}

	got, err := repo.Intents.GetByID(ctx, 9101)
	if err != nil {
		t.Fatalf("GetByID(9101): %v", err)
	}
	if got.ID != 9101 || got.VersionID != 100 || got.CaseVersionID != 300 || got.Purpose != execution.PurposeInitial {
		t.Fatalf("GetByID returned the wrong intent: %+v", got)
	}
	if got.EnvironmentFingerprint != frozenFP {
		t.Fatalf("frozen environment fingerprint = %q, want %q (must read stored value, not recompute)", got.EnvironmentFingerprint, frozenFP)
	}
	if got.CodeBaseline != frozenBaseline {
		t.Fatalf("frozen code baseline = %q, want %q", got.CodeBaseline, frozenBaseline)
	}

	// The second intent reads back its OWN distinct frozen boundary: the port is
	// per-intent, not a shared/latest/live value.
	gotB, err := repo.Intents.GetByID(ctx, 9102)
	if err != nil {
		t.Fatalf("GetByID(9102): %v", err)
	}
	if gotB.EnvironmentFingerprint != otherFP || gotB.CodeBaseline != otherBaseline {
		t.Fatalf("intent B frozen boundary = %q/%q, want %q/%q", gotB.EnvironmentFingerprint, gotB.CodeBaseline, otherFP, otherBaseline)
	}

	// A missing intent maps to the domain not-found sentinel.
	if _, err := repo.Intents.GetByID(ctx, 999999); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("missing intent must map to ErrNotFound, got %v", err)
	}
}
