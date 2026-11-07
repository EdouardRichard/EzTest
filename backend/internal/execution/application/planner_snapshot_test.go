package executionapp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestBuildWaveScopeSnapshotFreezesAndNormalizesScope pins R-C5-1/DB-19: the
// wave scope document is deterministic (ordered ids) and carries the effective
// binding sets, so a rerun of the same wave observes the same frozen scope.
func TestBuildWaveScopeSnapshotFreezesAndNormalizesScope(t *testing.T) {
	repoID := int64(77)
	binding := VersionBinding{
		VersionID: 5,
		SUTs: []VersionSUTBinding{{
			VersionSUTID:  12,
			SUTID:         2,
			EnvironmentID: 3,
			PageIDs:       []int64{9, 7},
			APIIDs:        []int64{6},
			DatabaseIDs:   []int64{8},
			AccountRoles:  []string{"author", "admin"},
			CodeRepoID:    &repoID,
		}},
	}
	frozenAt := time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC)
	first, err := buildWaveScopeSnapshot(binding, frozenAt)
	if err != nil {
		t.Fatalf("buildWaveScopeSnapshot: %v", err)
	}
	for _, needle := range []string{
		`"version_id":5`,
		`"frozen_at":"2026-11-05T01:02:03Z"`,
		`"version_sut_id":12`,
		`"page_ids":[7,9]`,
		`"api_ids":[6]`,
		`"database_ids":[8]`,
		`"account_roles":["admin","author"]`,
		`"repository_ids":[77]`,
	} {
		if !strings.Contains(first, needle) {
			t.Fatalf("snapshot %s does not contain %s", first, needle)
		}
	}
	reordered := binding
	reordered.SUTs = []VersionSUTBinding{binding.SUTs[0]}
	reordered.SUTs[0].PageIDs = []int64{7, 9}
	reordered.SUTs[0].AccountRoles = []string{"admin", "author"}
	second, err := buildWaveScopeSnapshot(reordered, frozenAt)
	if err != nil {
		t.Fatalf("buildWaveScopeSnapshot reordered: %v", err)
	}
	if first != second {
		t.Fatalf("scope snapshot is not deterministic:\nfirst=%s\nsecond=%s", first, second)
	}
}

// TestBuildWaveScopeSnapshotCarriesEveryEffectiveRepository pins R-EX-4/DB-19:
// repository_ids is the multi-repository set of the binding, deduplicated and
// ascending, not a single CodeRepoID. A binding without the new set keeps the
// legacy single-repository projection.
func TestBuildWaveScopeSnapshotCarriesEveryEffectiveRepository(t *testing.T) {
	frozenAt := time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC)
	repoID := int64(77)
	binding := VersionBinding{
		VersionID: 5,
		SUTs: []VersionSUTBinding{{
			VersionSUTID:  12,
			SUTID:         2,
			EnvironmentID: 3,
			RepositoryIDs: []int64{9, 7, 9, 0},
			CodeRepoID:    &repoID,
		}},
	}
	raw, err := buildWaveScopeSnapshot(binding, frozenAt)
	if err != nil {
		t.Fatalf("buildWaveScopeSnapshot: %v", err)
	}
	payload := decodeWaveScopeSnapshot(t, raw)
	got := payload.VersionSUTs[0].RepositoryIDs
	t.Logf("multi-repo repository_ids = %v", got)
	if len(got) != 2 || got[0] != 7 || got[1] != 9 {
		t.Fatalf("repository_ids = %v, want the sorted unique multi-repo set [7 9]", got)
	}

	// Legacy data has no RepositoryIDs: fall back to the single CodeRepoID.
	binding.SUTs[0].RepositoryIDs = nil
	raw, err = buildWaveScopeSnapshot(binding, frozenAt)
	if err != nil {
		t.Fatalf("buildWaveScopeSnapshot fallback: %v", err)
	}
	payload = decodeWaveScopeSnapshot(t, raw)
	got = payload.VersionSUTs[0].RepositoryIDs
	if len(got) != 1 || got[0] != 77 {
		t.Fatalf("legacy repository_ids = %v, want the single CodeRepoID [77]", got)
	}
}

// TestSnapshotFreezesAndNextWaveSeesNew pins the next-wave semantics of R-EX-4:
// a newly approved resource (and a changed repository set) never mutates the
// current wave's frozen snapshot; it only appears in the next wave. Every wave
// creation publishes exactly one version.scope.snapshot_created event.
func TestSnapshotFreezesAndNextWaveSeesNew(t *testing.T) {
	f := newTask6Fixture(t)
	ctx := context.Background()
	f.cases.cases[300] = task6Snapshot(300, 5001)
	binding := task6Binding(100, "running", 5001)
	binding.SUTs[0].PageIDs = []int64{10}
	binding.SUTs[0].RepositoryIDs = []int64{9, 7}
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
	frozen1 := decodeWaveScopeSnapshot(t, wave1.ScopeSnapshot)
	if got := frozen1.VersionSUTs[0].PageIDs; len(got) != 1 || got[0] != 10 {
		t.Fatalf("wave1 page_ids = %v, want [10]", got)
	}
	if got := frozen1.VersionSUTs[0].RepositoryIDs; len(got) != 2 || got[0] != 7 || got[1] != 9 {
		t.Fatalf("wave1 repository_ids = %v, want the two effective repos [7 9]", got)
	}

	// The version binding now exposes one more approved page. The current wave
	// must keep its frozen snapshot when reloaded.
	binding.SUTs[0].PageIDs = []int64{10, 11}
	binding.SUTs[0].RepositoryIDs = []int64{7}
	f.versions.bindings[100] = binding
	reloaded, err := f.waves.Get(ctx, wave1.ID)
	if err != nil {
		t.Fatalf("reload wave1: %v", err)
	}
	if reloaded.ScopeSnapshot != wave1.ScopeSnapshot {
		t.Fatalf("wave1 snapshot changed after the binding update:\nbefore=%s\nafter=%s",
			wave1.ScopeSnapshot, reloaded.ScopeSnapshot)
	}

	// The changed effective scope produces a new initial intent; wave2 reads it
	// and freezes the new page/repository sets.
	if _, err := f.intent.EnsureInitialIntents(ctx, 100); err != nil {
		t.Fatalf("EnsureInitialIntents after approval: %v", err)
	}
	markWaveTerminal(f, wave1.ID)
	if err := f.scheduler.ReconcileVersion(ctx, 100); err != nil {
		t.Fatalf("ReconcileVersion wave2: %v", err)
	}
	wave2, err := f.waves.GetActive(ctx, 100)
	if err != nil || wave2 == nil {
		t.Fatalf("active wave2 = %+v err=%v", wave2, err)
	}
	if wave2.ID == wave1.ID {
		t.Fatalf("wave2 reused wave1 id %d", wave1.ID)
	}
	next := decodeWaveScopeSnapshot(t, wave2.ScopeSnapshot)
	if got := next.VersionSUTs[0].PageIDs; len(got) != 2 || got[0] != 10 || got[1] != 11 {
		t.Fatalf("wave2 page_ids = %v, want the newly approved [10 11]", got)
	}
	if got := next.VersionSUTs[0].RepositoryIDs; len(got) != 1 || got[0] != 7 {
		t.Fatalf("wave2 repository_ids = %v, want [7]", got)
	}
	// The frozen wave1 snapshot is still unchanged after wave2 was planned.
	reloaded, err = f.waves.Get(ctx, wave1.ID)
	if err != nil {
		t.Fatalf("reload wave1 after wave2: %v", err)
	}
	if reloaded.ScopeSnapshot != wave1.ScopeSnapshot {
		t.Fatal("wave1 snapshot changed when wave2 was planned")
	}

	t.Logf("wave1 snapshot = %s", wave1.ScopeSnapshot)
	t.Logf("wave2 snapshot = %s", wave2.ScopeSnapshot)
	events := snapshotCreatedEvents(f)
	for _, event := range events {
		t.Logf("event type=%s channel=%s payload=%v", event.Type, event.Channel, event.Payload)
	}
	if len(events) != 2 {
		t.Fatalf("version.scope.snapshot_created events = %d, want one per wave (%d)", len(events), 2)
	}
	waveIDs := map[string]bool{}
	for _, event := range events {
		if event.Channel != "version:100" {
			t.Fatalf("event channel = %q, want version:100", event.Channel)
		}
		if event.Payload["version_id"] != "100" {
			t.Fatalf("event version_id = %v, want the decimal string 100", event.Payload["version_id"])
		}
		frozenAt, _ := event.Payload["frozen_at"].(string)
		if _, err := time.Parse(time.RFC3339, frozenAt); err != nil {
			t.Fatalf("event frozen_at = %q, want an RFC3339 timestamp: %v", frozenAt, err)
		}
		waveID, _ := event.Payload["wave_id"].(string)
		if waveID == "" {
			t.Fatal("event wave_id is empty")
		}
		waveIDs[waveID] = true
	}
	if !waveIDs[formatTestID(wave1.ID)] || !waveIDs[formatTestID(wave2.ID)] {
		t.Fatalf("event wave ids = %v, want both %d and %d", waveIDs, wave1.ID, wave2.ID)
	}
}

// decodeWaveScopeSnapshot decodes the frozen scope document.
func decodeWaveScopeSnapshot(t *testing.T, raw string) waveScopeSnapshotPayload {
	t.Helper()
	var payload waveScopeSnapshotPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode scope snapshot %q: %v", raw, err)
	}
	return payload
}

// markWaveTerminal makes a wave non-active so the next planning tick can start
// the next wave.
func markWaveTerminal(f *task6Fixture, waveID int64) {
	f.waves.mu.Lock()
	defer f.waves.mu.Unlock()
	wave := f.waves.byID[waveID]
	wave.Status = execution.WaveDone
	f.waves.byID[waveID] = wave
}

// snapshotCreatedEvents returns the version.scope.snapshot_created events
// published so far.
func snapshotCreatedEvents(f *task6Fixture) []eventingapp.Event {
	f.events.mu.Lock()
	defer f.events.mu.Unlock()
	events := make([]eventingapp.Event, 0)
	for _, event := range f.events.events {
		if event.Type == EventVersionScopeSnapshotCreated {
			events = append(events, event)
		}
	}
	return events
}

func formatTestID(id int64) string {
	return strconv.FormatInt(id, 10)
}
