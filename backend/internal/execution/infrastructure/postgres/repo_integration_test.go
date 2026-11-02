//go:build integration

package executionpg

import (
	"context"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newTestRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return NewRepository(pool, ids), pool
}

func mustCreateIntent(t *testing.T, repo *Repository, id, versionID, caseVersionID int64, fingerprint string, purpose execution.Purpose) execution.ExecutionIntent {
	t.Helper()
	ctx := context.Background()
	intent := execution.ExecutionIntent{
		ID:                     id,
		VersionID:              versionID,
		CaseVersionID:          caseVersionID,
		VersionSUTIDs:          []int64{5001},
		EnvironmentFingerprint: fingerprint,
		Purpose:                purpose,
		Status:                 execution.IntentOpen,
		DedupeKey:              "exec:test",
		AttemptNo:              1,
		CreatedAt:              time.Now().UTC(),
		UpdatedAt:              time.Now().UTC(),
	}
	if err := repo.Intents.Create(ctx, intent); err != nil {
		t.Fatalf("create intent %d: %v", id, err)
	}
	return intent
}

func mustCreateWave(t *testing.T, repo *Repository, id, versionID int64) execution.RunWave {
	t.Helper()
	wave := execution.RunWave{
		ID:        id,
		VersionID: versionID,
		Status:    execution.WavePlanned,
		Trigger:   execution.WaveTriggerAuto,
		Counts:    execution.WaveCounts{},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Waves.CreateWithCaseRuns(context.Background(), wave, nil, nil); err != nil {
		t.Fatalf("create wave %d: %v", id, err)
	}
	return wave
}

func mustCreateCaseRun(t *testing.T, repo *Repository, id, waveID, intentID, caseVersionID int64) execution.CaseRun {
	t.Helper()
	run := execution.CaseRun{
		ID:            id,
		WaveID:        waveID,
		IntentID:      intentID,
		CaseVersionID: caseVersionID,
		Status:        execution.CaseRunPending,
		Summary:       execution.CaseRunSummary{},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := repo.CaseRuns.Create(context.Background(), run); err != nil {
		t.Fatalf("create case run %d: %v", id, err)
	}
	return run
}

func TestIntentActiveUniqueAndTransition(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)

	duplicate := execution.ExecutionIntent{
		ID:                     2,
		VersionID:              100,
		CaseVersionID:          300,
		VersionSUTIDs:          []int64{5001},
		EnvironmentFingerprint: testFingerprint,
		Purpose:                execution.PurposeInitial,
		Status:                 execution.IntentOpen,
		DedupeKey:              "exec:test",
		AttemptNo:              1,
	}
	if err := repo.Intents.Create(ctx, duplicate); err == nil {
		t.Fatal("second active intent with the same natural key must conflict")
	}

	// Cancelling the active intent frees the partial-unique slot.
	changed, err := repo.Intents.Transition(ctx, 1, []execution.IntentStatus{execution.IntentOpen}, execution.IntentCancelled)
	if err != nil || !changed {
		t.Fatalf("transition to cancelled: changed=%v err=%v", changed, err)
	}
	stale, err := repo.Intents.Transition(ctx, 1, []execution.IntentStatus{execution.IntentOpen}, execution.IntentCancelled)
	if err != nil || stale {
		t.Fatalf("stale transition must be a no-op: changed=%v err=%v", stale, err)
	}
	if err := repo.Intents.Create(ctx, duplicate); err != nil {
		t.Fatalf("active intent after cancel must be accepted: %v", err)
	}
	if count, err := repo.Intents.CountInitialFor(ctx, 300, testFingerprint, ""); err != nil || count != 2 {
		t.Fatalf("initial history count = %d err=%v, want 2", count, err)
	}

	// A missing row reports not found.
	if _, err := repo.Intents.Get(ctx, 999); err == nil {
		t.Fatal("missing intent must report an error")
	}
	if _, err := repo.Intents.Transition(ctx, 999, []execution.IntentStatus{execution.IntentOpen}, execution.IntentCancelled); err == nil {
		t.Fatal("transition on a missing row must report not found")
	}
}

func TestIntentListingAndActiveScope(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	mustCreateIntent(t, repo, 10, 100, 300, testFingerprint, execution.PurposeInitial)
	mustCreateIntent(t, repo, 11, 100, 301, testFingerprint, execution.PurposeInitial)
	mustCreateIntent(t, repo, 12, 200, 302, testFingerprint, execution.PurposeInitial)

	open, err := repo.Intents.ListOpen(ctx, 100)
	if err != nil || len(open) != 2 {
		t.Fatalf("ListOpen(100) = %d err=%v", len(open), err)
	}
	all, err := repo.Intents.ListOpen(ctx, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListOpen(0) = %d err=%v", len(all), err)
	}
	active, err := repo.Intents.ListActive(ctx, 0)
	if err != nil || len(active) != 3 {
		t.Fatalf("ListActive(0) = %d err=%v", len(active), err)
	}

	page, hasMore, err := repo.Intents.List(ctx, 100, "", 1, nil)
	if err != nil || len(page) != 1 || !hasMore || page[0].ID != 11 {
		t.Fatalf("first page = %+v hasMore=%v err=%v", page, hasMore, err)
	}
	before := page[0].ID
	page, hasMore, err = repo.Intents.List(ctx, 100, "", 1, &before)
	if err != nil || len(page) != 1 || hasMore || page[0].ID != 10 {
		t.Fatalf("second page = %+v hasMore=%v err=%v", page, hasMore, err)
	}
}

func TestWaveActiveUniqueAndClaim(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	intent := mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	second := mustCreateIntent(t, repo, 2, 100, 301, testFingerprint, execution.PurposeInitial)
	wave := execution.RunWave{
		ID:        50,
		VersionID: 100,
		Status:    execution.WavePlanned,
		Trigger:   execution.WaveTriggerAuto,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	intents := []execution.ExecutionIntent{intent, second}
	runs := []execution.CaseRun{
		{ID: 500, WaveID: 50, IntentID: 1, CaseVersionID: 300, Status: execution.CaseRunPending, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
		{ID: 501, WaveID: 50, IntentID: 2, CaseVersionID: 301, Status: execution.CaseRunPending, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
	}
	if err := repo.Waves.CreateWithCaseRuns(ctx, wave, intents, runs); err != nil {
		t.Fatalf("create wave: %v", err)
	}

	for _, id := range []int64{1, 2} {
		claimed, err := repo.Intents.Get(ctx, id)
		if err != nil {
			t.Fatalf("get intent %d: %v", id, err)
		}
		if claimed.Status != execution.IntentClaimed {
			t.Fatalf("intent %d status = %s, want claimed", id, claimed.Status)
		}
	}

	active, err := repo.Waves.GetActive(ctx, 100)
	if err != nil || active == nil || active.ID != 50 {
		t.Fatalf("GetActive = %+v err=%v", active, err)
	}

	secondWave := execution.RunWave{
		ID: 51, VersionID: 100, Status: execution.WavePlanned, Trigger: execution.WaveTriggerManual,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Waves.CreateWithCaseRuns(ctx, secondWave, nil, nil); err == nil {
		t.Fatal("second active wave for the same version must conflict")
	}

	changed, err := repo.Waves.Transition(ctx, 50, []execution.WaveStatus{execution.WavePlanned}, execution.WaveRunning, execution.RunWave{})
	if err != nil || !changed {
		t.Fatalf("planned->running: changed=%v err=%v", changed, err)
	}
	changed, err = repo.Waves.Transition(ctx, 50, []execution.WaveStatus{execution.WaveRunning}, execution.WaveDone, execution.RunWave{})
	if err != nil || !changed {
		t.Fatalf("running->done: changed=%v err=%v", changed, err)
	}
	if active, err := repo.Waves.GetActive(ctx, 100); err != nil || active != nil {
		t.Fatalf("no active wave expected, got %+v err=%v", active, err)
	}
	if err := repo.Waves.CreateWithCaseRuns(ctx, secondWave, nil, nil); err != nil {
		t.Fatalf("new active wave after done must be accepted: %v", err)
	}

	if err := repo.Waves.CreateWithCaseRuns(ctx, execution.RunWave{
		ID: 52, VersionID: 200, Status: execution.WavePlanned, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil, []execution.CaseRun{{ID: 502, WaveID: 52, IntentID: 999, CaseVersionID: 300, Status: execution.CaseRunPending}}); err == nil {
		t.Fatal("case_run with a missing intent must fail the transaction")
	}

	if _, err := repo.Waves.Get(ctx, 999); err == nil {
		t.Fatal("missing wave must report an error")
	}
}

func TestWaveCountsByVersionSUT(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	intent := mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	wave := execution.RunWave{ID: 50, VersionID: 100, Status: execution.WavePlanned, Trigger: execution.WaveTriggerAuto, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	runs := []execution.CaseRun{
		{ID: 500, WaveID: 50, IntentID: 1, CaseVersionID: 300, Status: execution.CaseRunPassed, FailureClass: "", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()},
	}
	if err := repo.Waves.CreateWithCaseRuns(ctx, wave, []execution.ExecutionIntent{intent}, runs); err != nil {
		t.Fatalf("create wave: %v", err)
	}
	// Cross-system run: counts under both SUTs, total once.
	mustCreateIntent(t, repo, 2, 100, 301, testFingerprint, execution.PurposeManual)
	if err := repo.CaseRuns.Create(ctx, execution.CaseRun{
		ID: 501, WaveID: 50, IntentID: 2, CaseVersionID: 301, Status: execution.CaseRunFailed,
		FailureClass: execution.FailureClassApplication, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create second run: %v", err)
	}
	for _, item := range []struct {
		runID   int64
		sutID   int64
		account string
	}{
		{500, 5001, ""},
		{501, 5001, ""},
		{501, 5002, "user"},
	} {
		if err := repo.SUTContexts.CreateSUTContext(ctx, execution.CaseRunSUTContext{
			ID: 900 + item.runID + item.sutID, CaseRunID: item.runID, VersionSUTID: item.sutID,
			AccountRole: item.account, Status: execution.SUTContextCreating, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create sut context: %v", err)
		}
	}
	if err := repo.Evidence.Insert(ctx, execution.Evidence{
		ID: 700, CaseRunID: 500, Kind: execution.EvidenceScreenshot, ContentRefID: 1,
		SHA256: testFingerprint, SizeBytes: 12, MIME: "image/png", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
	counts, err := repo.Waves.Counts(ctx, 50)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts.Total != 2 || counts.Passed != 1 || counts.Failed != 1 || counts.Evidence != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	firstSUT := counts.ByVersionSUT["5001"]
	if firstSUT.Total != 2 || firstSUT.Passed != 1 || firstSUT.Failed != 1 {
		t.Fatalf("by_version_sut[5001] = %+v", firstSUT)
	}
	secondSUT := counts.ByVersionSUT["5002"]
	if secondSUT.Total != 1 || secondSUT.Failed != 1 {
		t.Fatalf("by_version_sut[5002] = %+v", secondSUT)
	}
}

func TestCaseRunUniqueAndCAS(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	intent := mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	_ = intent
	mustCreateWave(t, repo, 50, 100)
	mustCreateCaseRun(t, repo, 500, 50, 1, 300)

	if err := repo.CaseRuns.Create(ctx, execution.CaseRun{
		ID: 501, WaveID: 50, IntentID: 1, CaseVersionID: 300, Status: execution.CaseRunPending,
	}); err == nil {
		t.Fatal("duplicate (wave_id, intent_id) must conflict")
	}

	// Wrong epoch/token leaves the row untouched.
	ok, err := repo.CaseRuns.UpdateCAS(ctx, 500, 3, "token", func(run *execution.CaseRun) error {
		run.Status = execution.CaseRunRunning
		return nil
	})
	if err != nil || ok {
		t.Fatalf("wrong epoch CAS = %v err=%v", ok, err)
	}
	current, err := repo.CaseRuns.Get(ctx, 500)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if current.Status != execution.CaseRunPending {
		t.Fatalf("row changed despite wrong epoch: %s", current.Status)
	}

	// Seed a dispatch and then fence on it.
	current.DispatchEpoch = 2
	current.DispatchToken = "tok"
	current.Status = execution.CaseRunRunning
	current.OwnerInstance = "api-1"
	if ok, err := repo.CaseRuns.Update(ctx, current); err != nil || !ok {
		t.Fatalf("update run: ok=%v err=%v", ok, err)
	}
	ok, err = repo.CaseRuns.UpdateCAS(ctx, 500, 2, "tok", func(run *execution.CaseRun) error {
		run.Status = execution.CaseRunPassed
		run.Summary = execution.CaseRunSummary{VerdictReason: "passed"}
		return nil
	})
	if err != nil || !ok {
		t.Fatalf("matching CAS = %v err=%v", ok, err)
	}
	current, _ = repo.CaseRuns.Get(ctx, 500)
	if current.Status != execution.CaseRunPassed {
		t.Fatalf("status = %s, want passed", current.Status)
	}

	// Late writes are rejected and do not overwrite the new result.
	ok, err = repo.CaseRuns.UpdateCAS(ctx, 500, 2, "tok", func(run *execution.CaseRun) error {
		run.Status = execution.CaseRunFailed
		return nil
	})
	if err != nil || ok {
		t.Fatalf("late CAS must fail: ok=%v err=%v", ok, err)
	}
	if got, _ := repo.CaseRuns.Get(ctx, 500); got.Status != execution.CaseRunPassed {
		t.Fatalf("late write overwrote result: %s", got.Status)
	}

	if changed, err := repo.CaseRuns.Transition(ctx, 500, []execution.CaseRunStatus{execution.CaseRunPending}, execution.CaseRunRunning, 2, "tok"); err != nil || changed {
		t.Fatalf("wrong-status transition = %v err=%v", changed, err)
	}
	if _, err := repo.CaseRuns.ClaimLease(ctx, 500, 99, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("claim lease wrong epoch must be a no-op, got err=%v", err)
	}
}

func TestCaseRunListings(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	mustCreateIntent(t, repo, 2, 100, 301, testFingerprint, execution.PurposeInitial)
	mustCreateWave(t, repo, 50, 100)
	mustCreateCaseRun(t, repo, 500, 50, 1, 300)
	mustCreateCaseRun(t, repo, 501, 50, 2, 301)

	runs, err := repo.CaseRuns.ListAllByWave(ctx, 50)
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListAllByWave = %d err=%v", len(runs), err)
	}
	page, hasMore, err := repo.CaseRuns.ListByWave(ctx, 50, "", 1, nil)
	if err != nil || len(page) != 1 || !hasMore || page[0].ID != 501 {
		t.Fatalf("ListByWave desc page = %+v hasMore=%v err=%v", page, hasMore, err)
	}

	// Only one running run for quota/listRunning.
	run, _ := repo.CaseRuns.Get(ctx, 500)
	run.Status = execution.CaseRunRunning
	if ok, err := repo.CaseRuns.Update(ctx, run); err != nil || !ok {
		t.Fatalf("mark running: %v %v", ok, err)
	}
	running, err := repo.CaseRuns.ListRunning(ctx)
	if err != nil || len(running) != 1 || running[0].ID != 500 {
		t.Fatalf("ListRunning = %+v err=%v", running, err)
	}

	latest, err := repo.CaseRuns.LatestByCaseVersions(ctx, 100, []int64{300, 301, 999})
	if err != nil || len(latest) != 2 {
		t.Fatalf("LatestByCaseVersions = %+v err=%v", latest, err)
	}
	if latest[300].ID != 500 || latest[301].ID != 501 {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestStepAssertionUpsert(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	mustCreateWave(t, repo, 50, 100)
	run := mustCreateCaseRun(t, repo, 500, 50, 1, 300)
	run.DispatchEpoch = 1
	run.DispatchToken = "token-1"
	if _, err := repo.CaseRuns.Update(ctx, run); err != nil {
		t.Fatalf("seed dispatch: %v", err)
	}

	step := execution.StepResult{
		CaseRunID: 500, Seq: 1, Status: execution.StepRunning,
		Observed: map[string]any{"summary": "first"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	ok, err := repo.Results.UpsertStep(ctx, step, 1, "token-1")
	if err != nil || !ok {
		t.Fatalf("upsert step: ok=%v err=%v", ok, err)
	}
	step.Status = execution.StepPassed
	step.Observed = map[string]any{"summary": "second"}
	if ok, err := repo.Results.UpsertStep(ctx, step, 1, "token-1"); err != nil || !ok {
		t.Fatalf("second upsert: ok=%v err=%v", ok, err)
	}
	steps, err := repo.Results.ListSteps(ctx, 500)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps = %+v err=%v", steps, err)
	}
	if steps[0].Status != execution.StepPassed || steps[0].Observed["summary"] != "second" {
		t.Fatalf("step not updated: %+v", steps[0])
	}

	// Late epoch is rejected and does not overwrite.
	step.Status = execution.StepFailed
	if ok, err := repo.Results.UpsertStep(ctx, step, 1, "stale-token"); err != nil || ok {
		t.Fatalf("stale step write: ok=%v err=%v", ok, err)
	}
	steps, _ = repo.Results.ListSteps(ctx, 500)
	if steps[0].Status != execution.StepPassed {
		t.Fatalf("stale write overwrote step: %+v", steps[0])
	}

	assertion := execution.AssertionResult{
		CaseRunID: 500, StepSeq: 1, AssertionSeq: 1, Kind: "ui", Verdict: execution.AssertionFailed,
		Expected: "a", Actual: "b", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if ok, err := repo.Results.UpsertAssertion(ctx, assertion, 1, "token-1"); err != nil || !ok {
		t.Fatalf("upsert assertion: ok=%v err=%v", ok, err)
	}
	assertion.Verdict = execution.AssertionPassed
	if ok, err := repo.Results.UpsertAssertion(ctx, assertion, 1, "token-1"); err != nil || !ok {
		t.Fatalf("second assertion upsert: ok=%v err=%v", ok, err)
	}
	assertions, err := repo.Results.ListAssertions(ctx, 500)
	if err != nil || len(assertions) != 1 || assertions[0].Verdict != execution.AssertionPassed {
		t.Fatalf("assertions = %+v err=%v", assertions, err)
	}
	if ok, err := repo.Results.UpsertAssertion(ctx, assertion, 0, ""); err != nil || !ok {
		// Empty epoch/token means no fence (server-side write).
		t.Fatalf("unfenced assertion upsert: ok=%v err=%v", ok, err)
	}
}

func TestEvidenceAndSUTContext(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()

	mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	mustCreateWave(t, repo, 50, 100)
	mustCreateCaseRun(t, repo, 500, 50, 1, 300)

	old := time.Now().UTC().Add(-time.Hour)
	if err := repo.SUTContexts.CreateSUTContext(ctx, execution.CaseRunSUTContext{
		ID: 900, CaseRunID: 500, VersionSUTID: 5001, AccountRole: "user",
		Status: execution.SUTContextCreating, CreatedAt: old, UpdatedAt: old,
	}); err != nil {
		t.Fatalf("create sut context: %v", err)
	}
	if err := repo.SUTContexts.CreateSUTContext(ctx, execution.CaseRunSUTContext{
		ID: 901, CaseRunID: 500, VersionSUTID: 5001, AccountRole: "user",
		Status: execution.SUTContextCreating, CreatedAt: old, UpdatedAt: old,
	}); err == nil {
		t.Fatal("duplicate (case_run, version_sut, role) must conflict")
	}
	created, err := repo.SUTContexts.ListByCaseRun(ctx, 500)
	if err != nil || len(created) != 1 {
		t.Fatalf("sut contexts = %+v err=%v", created, err)
	}
	ok, err := repo.SUTContexts.UpdateCAS(ctx, 900, func(item *execution.CaseRunSUTContext) error {
		item.Status = execution.SUTContextActive
		item.BrowserContextID = "ctx-1"
		return nil
	})
	if err != nil || !ok {
		t.Fatalf("update sut context: ok=%v err=%v", ok, err)
	}
	stale, err := repo.SUTContexts.ListStale(ctx, time.Now().UTC().Add(-time.Minute), 10)
	if err != nil || len(stale) != 1 || stale[0].ID != 900 {
		t.Fatalf("ListStale = %+v err=%v", stale, err)
	}
	all, err := repo.SUTContexts.ListAll(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAll = %+v err=%v", all, err)
	}

	evidence := execution.Evidence{
		ID: 700, CaseRunID: 500, VersionSUTID: ptrInt64(5001), StepSeq: ptrInt(1),
		Kind: execution.EvidenceScreenshot, ContentRefID: 42, SHA256: testFingerprint,
		SizeBytes: 100, MIME: "image/png", Filename: "shot.png", CreatedAt: time.Now().UTC(),
	}
	if err := repo.Evidence.Insert(ctx, evidence); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
	got, err := repo.Evidence.Get(ctx, 700)
	if err != nil || got.SHA256 != testFingerprint || got.SizeBytes != 100 {
		t.Fatalf("get evidence = %+v err=%v", got, err)
	}
	count, err := repo.Evidence.CountByCaseRun(ctx, 500)
	if err != nil || count != 1 {
		t.Fatalf("CountByCaseRun = %d err=%v", count, err)
	}
	if err := repo.Evidence.InsertRef(ctx, execution.EvidenceRef{
		ID: 800, EvidenceID: 700, OwnerKind: execution.EvidenceOwnerCaseRun, OwnerID: 500, Role: "screenshot",
	}); err != nil {
		t.Fatalf("insert evidence ref: %v", err)
	}
	byKind, hasMore, err := repo.Evidence.ListByVersion(ctx, 100, execution.EvidenceFilter{Kind: execution.EvidenceScreenshot, Limit: 10})
	if err != nil || len(byKind) != 1 || hasMore {
		t.Fatalf("ListByVersion = %+v hasMore=%v err=%v", byKind, hasMore, err)
	}
	otherKind, _, err := repo.Evidence.ListByVersion(ctx, 100, execution.EvidenceFilter{Kind: execution.EvidenceConsole, Limit: 10})
	if err != nil || len(otherKind) != 0 {
		t.Fatalf("filtered evidence = %+v err=%v", otherKind, err)
	}
}

func TestExecutorAndHumanRequest(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	executor := execution.ExecutorInstance{
		ID: 1, Name: "executor-1", Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &now, Version: "1.0", CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Executors.UpsertHeartbeat(ctx, executor); err != nil {
		t.Fatalf("upsert heartbeat: %v", err)
	}
	executor.Capacity = 4
	executor.ActiveContexts = 2
	executor.Status = execution.ExecutorDraining
	if err := repo.Executors.UpsertHeartbeat(ctx, executor); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := repo.Executors.GetByEndpoint(ctx, "http://127.0.0.1:13336")
	if err != nil || got.Capacity != 4 || got.ActiveContexts != 2 || got.Status != execution.ExecutorDraining {
		t.Fatalf("heartbeat upsert = %+v err=%v", got, err)
	}
	if ok, err := repo.Executors.SetStatus(ctx, 1, execution.ExecutorActive); err != nil || !ok {
		t.Fatalf("SetStatus = %v err=%v", ok, err)
	}
	list, err := repo.Executors.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List executors = %+v err=%v", list, err)
	}
	if _, err := repo.Executors.GetByEndpoint(ctx, "http://missing"); err == nil {
		t.Fatal("missing executor must error")
	}

	human := execution.HumanRequest{
		ID: 10, VersionID: 100, CaseRunID: ptrInt64(500), Kind: execution.HumanLoginTakeover,
		Payload: map[string]any{"reason": "2fa"}, Status: execution.HumanOpen,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Humans.Create(ctx, human); err != nil {
		t.Fatalf("create human request: %v", err)
	}
	open, err := repo.Humans.GetOpenByCaseRun(ctx, 500)
	if err != nil || open == nil || open.ID != 10 {
		t.Fatalf("GetOpenByCaseRun = %+v err=%v", open, err)
	}
	if count, err := repo.Humans.CountByCaseRun(ctx, 500); err != nil || count != 1 {
		t.Fatalf("CountByCaseRun = %d err=%v", count, err)
	}
	changed, err := repo.Humans.Transition(ctx, 10, execution.HumanOpen, execution.HumanResolved,
		map[string]any{"code": "246810"}, 7, now)
	if err != nil || !changed {
		t.Fatalf("resolve human request: %v %v", changed, err)
	}
	if changed, err := repo.Humans.Transition(ctx, 10, execution.HumanOpen, execution.HumanCancelled, nil, 7, now); err != nil || changed {
		t.Fatalf("second transition = %v err=%v", changed, err)
	}
	resolved, err := repo.Humans.Get(ctx, 10)
	if err != nil || resolved.Status != execution.HumanResolved || resolved.Response["code"] != "246810" {
		t.Fatalf("resolved human request = %+v err=%v", resolved, err)
	}

	expired := execution.HumanRequest{
		ID: 11, VersionID: 100, Kind: execution.HumanLoginTakeover, Status: execution.HumanOpen,
		ExpiresAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := repo.Humans.Create(ctx, expired); err != nil {
		t.Fatalf("create expiring request: %v", err)
	}
	rows, err := repo.Humans.Expire(ctx, now)
	if err != nil || len(rows) != 1 || rows[0].ID != 11 {
		t.Fatalf("Expire = %+v err=%v", rows, err)
	}
	rows, err = repo.Humans.Expire(ctx, now)
	if err != nil || len(rows) != 0 {
		t.Fatalf("second Expire = %+v err=%v", rows, err)
	}
	page, hasMore, err := repo.Humans.ListByVersion(ctx, 100, "", 10, nil)
	if err != nil || len(page) != 2 || hasMore {
		t.Fatalf("ListByVersion = %+v hasMore=%v err=%v", page, hasMore, err)
	}
}

func ptrInt64(value int64) *int64 { return &value }
func ptrInt(value int) *int       { return &value }
