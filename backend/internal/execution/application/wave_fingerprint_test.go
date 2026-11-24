package executionapp

// SC-F7 (P4) scoped freeze: the wave-frozen SUT catalog snapshot is the
// fingerprint source for WAVE-LEVEL consumers only. Intent creation and the
// replay drift gate keep the LIVE read — freezing either would kill catalog
// change detection (SC-F3) or turn the same-fingerprint replay gate into a
// tautology. These three tests pin the scope boundary from both sides.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestWaveFrozenFingerprintStableInWave drives the REAL planner: a wave freezes
// the effective scope (including base_url) and carries the fingerprint computed
// from it. A mid-wave catalog change must NOT alter the in-flight wave's
// fingerprint; the next wave freezes the new one. The wave-level consumer
// (WaveFrozenFingerprint) reconstructs from the frozen snapshot, never live.
func TestWaveFrozenFingerprintStableInWave(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	binding := task6Binding(100, "running", 5001)
	binding.SUTs[0].PageIDs = []int64{10}
	binding.SUTs[0].BaseURL = "http://127.0.0.1:13335"
	f.versions.bindings[100] = binding

	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	f.registry.executor = nil
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion wave1: %v", err)
	}
	wave1, err := f.waves.GetActive(ctx, 100)
	if err != nil || wave1 == nil {
		t.Fatalf("active wave1 = %+v err=%v", wave1, err)
	}

	fp1, bl1, err := WaveFrozenFingerprint(*wave1)
	if err != nil {
		t.Fatalf("WaveFrozenFingerprint(wave1): %v", err)
	}
	if len(fp1) != 64 {
		t.Fatalf("wave1 frozen fingerprint = %q, want 64 hex", fp1)
	}
	// The RunWave carrier is populated and agrees with the reconstruction.
	if wave1.EnvironmentFingerprint != fp1 || wave1.CodeBaseline != bl1 {
		t.Fatalf("wave carriers = (%q,%q), want the reconstructed (%q,%q)",
			wave1.EnvironmentFingerprint, wave1.CodeBaseline, fp1, bl1)
	}
	// The frozen snapshot carries base_url so the fingerprint input is rebuildable.
	if !strings.Contains(wave1.ScopeSnapshot, `"base_url":"http://127.0.0.1:13335"`) {
		t.Fatalf("wave1 snapshot lacks base_url: %s", wave1.ScopeSnapshot)
	}

	// Mid-wave catalog change: a newly approved page AND a moved base_url.
	changed := binding
	changed.SUTs = append([]SUTBinding(nil), binding.SUTs...)
	changed.SUTs[0].PageIDs = []int64{10, 11}
	changed.SUTs[0].BaseURL = "http://127.0.0.1:14000"
	f.versions.bindings[100] = changed

	// The in-flight wave's frozen fingerprint is unchanged after the catalog move.
	reloaded, err := f.waves.Get(ctx, wave1.ID)
	if err != nil {
		t.Fatalf("reload wave1: %v", err)
	}
	fp1Again, bl1Again, err := WaveFrozenFingerprint(reloaded)
	if err != nil {
		t.Fatalf("WaveFrozenFingerprint(reloaded wave1): %v", err)
	}
	if fp1Again != fp1 || bl1Again != bl1 {
		t.Fatalf("mid-wave catalog change altered the in-wave fingerprint: (%q,%q) -> (%q,%q)",
			fp1, bl1, fp1Again, bl1Again)
	}

	// The change is real: the LIVE fingerprint diverges from the frozen one.
	liveFP, err := EnvironmentFingerprint(changed)
	if err != nil {
		t.Fatalf("live fingerprint: %v", err)
	}
	if liveFP == fp1 {
		t.Fatal("the catalog change must change the live fingerprint (fixture is not discriminating)")
	}

	// The next wave freezes the new fingerprint.
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents after change: %v", err)
	}
	markWaveTerminal(f, wave1.ID)
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion wave2: %v", err)
	}
	wave2, err := f.waves.GetActive(ctx, 100)
	if err != nil || wave2 == nil || wave2.ID == wave1.ID {
		t.Fatalf("active wave2 = %+v err=%v", wave2, err)
	}
	fp2, _, err := WaveFrozenFingerprint(*wave2)
	if err != nil {
		t.Fatalf("WaveFrozenFingerprint(wave2): %v", err)
	}
	if fp2 == fp1 {
		t.Fatal("the next wave must freeze a new fingerprint after the catalog change")
	}
	if fp2 != liveFP {
		t.Fatalf("wave2 frozen fingerprint = %q, want the live %q", fp2, liveFP)
	}
}

// TestWaveFrozenFingerprintRejectsLegacyAndTamperedSnapshots pins fail-closed
// reconstruction: a wave without a frozen carrier (legacy "{}") is a typed
// error, never a silent live-read fallback; a snapshot whose recorded carrier
// disagrees with its frozen inputs is rejected (the derived value must rebuild).
func TestWaveFrozenFingerprintRejectsLegacyAndTamperedSnapshots(t *testing.T) {
	if _, _, err := WaveFrozenFingerprint(execution.RunWave{ScopeSnapshot: "{}"}); err == nil {
		t.Fatal("a legacy wave without a frozen fingerprint carrier must be a typed error")
	}

	binding := task6Binding(100, "running", 5001)
	binding.SUTs[0].PageIDs = []int64{10}
	raw, err := buildWaveScopeSnapshot(binding, time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("buildWaveScopeSnapshot: %v", err)
	}
	// Tamper: keep the recorded carrier but move a frozen page id. The
	// reconstruction must diverge from the carrier and fail closed.
	tampered := strings.Replace(raw, `"page_ids":[10]`, `"page_ids":[10,11]`, 1)
	if tampered == raw {
		t.Fatalf("fixture did not tamper the snapshot: %s", raw)
	}
	if _, _, err := WaveFrozenFingerprint(execution.RunWave{ScopeSnapshot: tampered}); err == nil {
		t.Fatal("a snapshot whose carrier disagrees with its frozen inputs must be rejected")
	}
}

// TestWaveFrozenFingerprintMalformedKeepsFieldDetail pins the H-G4 T9 minor: the
// malformed-snapshot re-wrap keeps the domain parser's field detail
// (platform.Invalid("scope_snapshot", ...)) instead of dropping it. The code
// stays the classifiable WaveFrozenSnapshotMalformedCode, but a consumer reading
// the field detail still learns WHICH input was malformed (constitution 七.6/七.7).
func TestWaveFrozenFingerprintMalformedKeepsFieldDetail(t *testing.T) {
	_, _, err := WaveFrozenFingerprint(execution.RunWave{ScopeSnapshot: "{not-json"})
	if err == nil {
		t.Fatal("a malformed scope snapshot must be a typed error")
	}
	typed := platform.AsError(err)
	if typed.Status != http.StatusBadRequest || typed.Code != WaveFrozenSnapshotMalformedCode {
		t.Fatalf("malformed snapshot error = %d/%s, want 400/%s", typed.Status, typed.Code, WaveFrozenSnapshotMalformedCode)
	}
	if typed.Details["field"] != "scope_snapshot" {
		t.Fatalf("malformed snapshot field detail = %v, want scope_snapshot (the domain parser's field must survive the re-wrap)", typed.Details["field"])
	}
}

// TestIntentCreationLiveReadDetectsChange pins that intent creation keeps the
// LIVE catalog read: it precedes any wave, and the live read is precisely the
// change-detection mechanism (catalog change -> new fingerprint -> new intent ->
// next wave). Freezing intent creation would reuse the old fingerprint and the
// new intent would never appear.
func TestIntentCreationLiveReadDetectsChange(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	binding := task6Binding(100, "running", 5001)
	binding.SUTs[0].PageIDs = []int64{10}
	f.versions.bindings[100] = binding

	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents: %v", err)
	}
	first, err := f.intents.ListActive(ctx, 100)
	if err != nil || len(first) != 1 {
		t.Fatalf("first scan intents = %+v err=%v", first, err)
	}
	fp1 := first[0].EnvironmentFingerprint

	// Enrich the catalog (SC-F3: a registered asset widens the effective scope).
	changed := binding
	changed.SUTs = append([]SUTBinding(nil), binding.SUTs...)
	changed.SUTs[0].PageIDs = []int64{10, 11}
	f.versions.bindings[100] = changed
	liveFP, err := EnvironmentFingerprint(changed)
	if err != nil {
		t.Fatalf("live fingerprint: %v", err)
	}
	if liveFP == fp1 {
		t.Fatal("the catalog change must change the live fingerprint (fixture is not discriminating)")
	}

	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents after change: %v", err)
	}
	active, err := f.intents.ListActive(ctx, 100)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	detected := false
	for _, intent := range active {
		if intent.EnvironmentFingerprint == liveFP {
			detected = true
		}
	}
	if !detected {
		t.Fatalf("intent creation must live-read the catalog and create an intent with the new fingerprint %q; active=%+v",
			liveFP, active)
	}
}

// TestReplayDriftGateLive pins that the replay same-fingerprint gate recomputes
// the CURRENT (live) fingerprint and rejects drift with a typed 409. A wave
// frozen at the original scope still carries the intent's fingerprint, so a gate
// that compared against the frozen wave value would pass — a tautology. The gate
// must read live, not the frozen snapshot.
func TestReplayDriftGateLive(t *testing.T) {
	ctx := context.Background()
	binding := task6Binding(10, "running", 7)
	binding.SUTs[0].BaseURL = "http://127.0.0.1:13335"
	fp, err := EnvironmentFingerprint(binding)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	baseline, err := CodeBaseline(binding)
	if err != nil {
		t.Fatalf("code baseline: %v", err)
	}
	intent := execution.ExecutionIntent{EnvironmentFingerprint: fp, CodeBaseline: baseline}
	versions := &fakeVersionReader{bindings: map[int64]VersionBinding{10: binding}}
	s := &CaseRunner{versions: versions}

	// Same live fingerprint -> the gate passes.
	if err := s.requireReplayFingerprint(ctx, 10, intent); err != nil {
		t.Fatalf("same fingerprint must pass the gate: %v", err)
	}

	// A wave frozen at the ORIGINAL scope still carries the intent's fingerprint.
	raw, err := buildWaveScopeSnapshot(binding, time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("buildWaveScopeSnapshot: %v", err)
	}
	frozenFP, _, err := WaveFrozenFingerprint(execution.RunWave{VersionID: 10, ScopeSnapshot: raw})
	if err != nil {
		t.Fatalf("WaveFrozenFingerprint: %v", err)
	}
	if frozenFP != fp {
		t.Fatalf("frozen wave fingerprint = %q, want the original %q (fixture precondition)", frozenFP, fp)
	}

	// The LIVE catalog drifts. The gate must reject even though the frozen wave
	// value still equals the intent's fingerprint.
	changed := binding
	changed.SUTs = append([]SUTBinding(nil), binding.SUTs...)
	changed.SUTs[0].PageIDs = []int64{99}
	versions.bindings[10] = changed
	err = s.requireReplayFingerprint(ctx, 10, intent)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "replay_fingerprint_changed" || apiErr.Status != http.StatusConflict {
		t.Fatalf("the drift gate must recompute the CURRENT fingerprint and reject drift typed 409, got %v", err)
	}
}
