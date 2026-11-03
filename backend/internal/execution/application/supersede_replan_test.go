package executionapp

import (
	"context"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// TestSupersededConfirmedCaseIsReplanned pins the Task 106 / PO E2E F4 fix on
// the planning path: the execute.batch selection names logical cases 1300 and
// 1301; before the wave is created case 1301 is re-confirmed as revision 2
// (case version 302) and the selected version 301 leaves the executable set.
// The planner must not drop the selected case: it retires the stale intent and
// auto-enqueues the replacement confirmed revision into the same wave.
func TestSupersededConfirmedCaseIsReplanned(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5002)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)
	f.registry.executor = activeExecutor(f)

	selection := []int64{1300, 1301}
	if _, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, selection); err != nil {
		t.Fatalf("EnsureInitialIntentsForCases: %v", err)
	}
	oldIntent := intentForCaseVersion(t, f, 301)

	// A late design/requirement change superseded the confirmed version 301 and
	// the same logical case 1301 is again current+confirmed as version 302.
	delete(f.cases.cases, 301)
	replacement := task6Snapshot(302, 5002)
	replacement.CaseID = 1301
	f.cases.cases[302] = replacement
	f.cases.replacements[301] = 302

	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	wave, err := f.waves.GetActive(ctx, 100)
	if err != nil || wave == nil {
		t.Fatalf("active wave = %+v err=%v", wave, err)
	}
	runs, err := f.runs.ListAllByWave(ctx, wave.ID)
	if err != nil {
		t.Fatalf("ListAllByWave: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("wave runs = %d (%+v), want 2: the replacement confirmed case must not be dropped", len(runs), runs)
	}
	byVersion := map[int64]execution.CaseRun{}
	for _, run := range runs {
		byVersion[run.CaseVersionID] = run
	}
	if _, ok := byVersion[301]; ok {
		t.Fatalf("superseded case version 301 entered the wave: %+v", runs)
	}
	if _, ok := byVersion[302]; !ok {
		t.Fatalf("replacement case version 302 missing from the wave: %+v", runs)
	}

	// The stale intent is retired, the replacement intent is active and no
	// selected logical case is silently lost.
	stale, err := f.intents.Get(ctx, oldIntent.ID)
	if err != nil {
		t.Fatalf("get stale intent: %v", err)
	}
	if stale.Status != execution.IntentSuperseded {
		t.Fatalf("stale intent status = %q, want superseded", stale.Status)
	}
	if got := intentForCaseVersion(t, f, 302); got.Status.Terminal() || !got.Status.Active() {
		t.Fatalf("replacement intent = %+v, want an active queued initial intent", got)
	}

	// The supersede is surfaced with its reason and replacement, never a bare
	// silent drop.
	payload := findEventPayload(t, f, EventIntentSuperseded, "case_version_id", platformid.Format(301))
	if payload["reason"] != SupersedeReasonCaseVersion {
		t.Fatalf("superseded event payload = %#v, want reason %q", payload, SupersedeReasonCaseVersion)
	}
	if payload["replacement_case_version_id"] != platformid.Format(302) {
		t.Fatalf("superseded event payload = %#v, want replacement 302", payload)
	}
	if payload["replanned"] != true {
		t.Fatalf("superseded event payload = %#v, want replanned=true", payload)
	}
}

// TestSupersedeIsSurfaced pins the mid-wave path: a planned pending case_run
// whose confirmed case version is superseded after the wave was created must be
// retired with a visible reason and its replacement re-queued into the next
// wave automatically. This is the PO E2E 6/12 drop: the run list showed only a
// bare superseded state with no reason and no replacement ever ran.
func TestSupersedeIsSurfaced(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	f.cases.cases[301] = task6Snapshot(301, 5002)
	f.versions.bindings[100] = task6Binding(100, "running", 5001, 5002)
	// A single global slot keeps case 301's run pending after the wave is
	// planned and case 300 has taken the slot.
	f.scheduler.quota = domainQuota(1, 2, 1)
	f.registry.executor = activeExecutor(f)

	if _, err := f.intent.EnsureInitialIntentsForCases(ctx, 100, []int64{1300, 1301}); err != nil {
		t.Fatalf("EnsureInitialIntentsForCases: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("first ReconcileVersion: %v", err)
	}
	firstWave, err := f.waves.GetActive(ctx, 100)
	if err != nil || firstWave == nil {
		t.Fatalf("first active wave = %+v err=%v", firstWave, err)
	}
	pendingRun := runForCaseVersion(t, f, firstWave.ID, 301)
	if pendingRun.Status != execution.CaseRunPending {
		t.Fatalf("case version 301 run = %q, want pending before the supersede", pendingRun.Status)
	}

	// The confirmed version 301 is superseded mid-wave and the same logical case
	// is re-confirmed as revision 2 (version 302).
	delete(f.cases.cases, 301)
	replacement := task6Snapshot(302, 5002)
	replacement.CaseID = 1301
	f.cases.cases[302] = replacement
	f.cases.replacements[301] = 302

	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("second ReconcileVersion: %v", err)
	}
	retired, err := f.runs.Get(ctx, pendingRun.ID)
	if err != nil {
		t.Fatalf("get retired run: %v", err)
	}
	if retired.Status != execution.CaseRunSuperseded {
		t.Fatalf("retired run status = %q, want superseded (%+v)", retired.Status, retired)
	}
	if retired.Summary.VerdictReason != SupersedeReasonCaseVersion || retired.Summary.TerminalCause != SupersedeReasonCaseVersion {
		t.Fatalf("retired run surface = %+v, want %q in verdict_reason/terminal_cause", retired.Summary, SupersedeReasonCaseVersion)
	}
	payload := findEventPayload(t, f, EventCaseRunFinished, "case_run_id", platformid.Format(pendingRun.ID))
	if payload["status"] != string(execution.CaseRunSuperseded) ||
		payload["reason"] != SupersedeReasonCaseVersion ||
		payload["replacement_case_version_id"] != platformid.Format(302) ||
		payload["replanned"] != true {
		t.Fatalf("case_run.finished payload = %#v, want superseded reason/replacement/replanned", payload)
	}

	// The replacement is auto-requeued: once the first wave is terminal the next
	// tick plans a new wave containing it without any manual batch dispatch.
	finishRunningRuns(t, f, firstWave.ID)
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("complete first wave: %v", err)
	}
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("plan replacement wave: %v", err)
	}
	nextWave, err := f.waves.GetActive(ctx, 100)
	if err != nil || nextWave == nil {
		t.Fatalf("replacement active wave = %+v err=%v", nextWave, err)
	}
	if nextWave.ID == firstWave.ID {
		t.Fatalf("replacement wave reused the superseded wave %d", firstWave.ID)
	}
	runForCaseVersion(t, f, nextWave.ID, 302)
}

func intentForCaseVersion(t *testing.T, f *task6Fixture, caseVersionID int64) execution.ExecutionIntent {
	t.Helper()
	intents, _, err := f.intents.List(context.Background(), 100, "", 50, nil)
	if err != nil {
		t.Fatalf("list intents: %v", err)
	}
	for _, intent := range intents {
		if intent.CaseVersionID == caseVersionID {
			return intent
		}
	}
	t.Fatalf("no intent for case version %d (intents %+v)", caseVersionID, intents)
	return execution.ExecutionIntent{}
}

func runForCaseVersion(t *testing.T, f *task6Fixture, waveID, caseVersionID int64) execution.CaseRun {
	t.Helper()
	runs, err := f.runs.ListAllByWave(context.Background(), waveID)
	if err != nil {
		t.Fatalf("ListAllByWave(%d): %v", waveID, err)
	}
	for _, run := range runs {
		if run.CaseVersionID == caseVersionID {
			return run
		}
	}
	t.Fatalf("no case run for case version %d in wave %d (%+v)", caseVersionID, waveID, runs)
	return execution.CaseRun{}
}

func findEventPayload(t *testing.T, f *task6Fixture, eventType, key, value string) map[string]any {
	t.Helper()
	f.events.mu.Lock()
	defer f.events.mu.Unlock()
	for _, event := range f.events.events {
		if event.Type != eventType {
			continue
		}
		if event.Payload[key] == value {
			return event.Payload
		}
	}
	t.Fatalf("no %s event with %s=%s (events %v)", eventType, key, value, f.events.types())
	return nil
}
