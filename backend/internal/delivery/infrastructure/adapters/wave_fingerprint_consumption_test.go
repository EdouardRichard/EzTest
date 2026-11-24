package adapters

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// testWaveScopeSnapshot builds a frozen wave scope document whose recorded
// carriers agree with its frozen inputs (so WaveFrozenFingerprint reconstructs
// them), returning the raw JSON plus the environment fingerprint and code
// baseline. It mirrors the planner's freeze through the same canonical hash
// functions, never a hand-written carrier.
func testWaveScopeSnapshot(t *testing.T, versionID int64, suts []executionapp.SUTBinding) (string, string, string) {
	t.Helper()
	binding := executionapp.VersionBinding{VersionID: versionID, SUTs: suts}
	fp, err := executionapp.EnvironmentFingerprint(binding)
	if err != nil {
		t.Fatalf("EnvironmentFingerprint: %v", err)
	}
	baseline, err := executionapp.CodeBaseline(binding)
	if err != nil {
		t.Fatalf("CodeBaseline: %v", err)
	}
	scope := execution.WaveScopeSnapshot{VersionID: versionID, EnvironmentFingerprint: fp, CodeBaseline: baseline}
	for _, sut := range suts {
		ws := execution.WaveScopeSUT{
			SUTID: sut.SUTID, EnvironmentID: sut.EnvironmentID, BaseURL: sut.BaseURL,
			PageIDs: sut.PageIDs, APIIDs: sut.APIIDs, DatabaseIDs: sut.DatabaseIDs,
			AccountRoles: sut.AccountRoles, RepositoryIDs: sut.RepositoryIDs,
		}
		for _, b := range sut.RepositoryBaselines {
			ws.RepositoryBaselines = append(ws.RepositoryBaselines, execution.WaveScopeRepositoryBaseline{RepoID: b.RepoID, CommitSHA: b.Commit})
		}
		scope.SUTs = append(scope.SUTs, ws)
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatalf("marshal scope: %v", err)
	}
	return string(raw), fp, baseline
}

func testSUTBinding() executionapp.SUTBinding {
	commit := strings.Repeat("a", 40)
	return executionapp.SUTBinding{
		SUTID: 70, EnvironmentID: 710, BaseURL: "https://planned.example.com",
		PageIDs: []int64{1, 2}, APIIDs: []int64{3}, DatabaseIDs: []int64{},
		AccountRoles:  []string{"admin"},
		RepositoryIDs: []int64{5},
		RepositoryBaselines: []executionapp.RepositoryBaseline{
			{RepoID: 5, Commit: commit},
		},
	}
}

// newWaveFingerprintFixture builds a one-wave version whose wave carries a valid
// frozen scope snapshot. The member run's intent fingerprint is configurable so
// the carrier-vs-per-run divergence can be exercised.
func newWaveFingerprintFixture(t *testing.T, scopeRaw string, intentFP, intentBaseline string) Deps {
	t.Helper()
	waves := &fakeWaves{waves: []execution.RunWave{
		{ID: 11, VersionID: 7, Status: execution.WaveDone, Trigger: execution.WaveTriggerManual,
			ScopeSnapshot: scopeRaw,
			Counts:        execution.WaveCounts{Total: 1, Passed: 1, BySUT: map[string]execution.SUTCounts{}}},
	}}
	runs := &fakeCaseRuns{runs: []execution.CaseRun{
		{ID: 5001, WaveID: 11, IntentID: 9001, CaseVersionID: 3001, Status: execution.CaseRunPassed,
			Summary: execution.CaseRunSummary{StepTotal: 1, AssertionTotal: 1}},
	}}
	intents := &fakeIntents{byID: map[int64]execution.ExecutionIntent{
		9001: {ID: 9001, VersionID: 7, CaseVersionID: 3001, EnvironmentFingerprint: intentFP, CodeBaseline: intentBaseline},
	}}
	designCases := &fakeDesign{cases: map[int64][]designapp.ExecutableCaseView{7: {
		{CaseID: 201, CaseVersionID: 3001, RequirementID: 101, RequirementVersionID: 1001, Title: "Case A"},
	}}}
	requirements := &fakeRequirements{views: map[int64][]versionapp.RequirementView{7: {
		{ID: 101, CurrentVersionID: 1001, Title: "Requirement A", Status: "clarified"},
	}}}
	versions := &fakeVersions{views: map[int64]versionapp.VersionView{7: {ID: 7, Title: "Version", Status: "running"}}}
	return Deps{
		Versions:         versions,
		Requirements:     requirements,
		Design:           designCases,
		Waves:            waves,
		CaseRuns:         runs,
		Results:          &fakeResults{},
		SUTContexts:      &fakeSUTContexts{rows: map[int64][]execution.CaseRunSUTContext{5001: {{ID: 1, CaseRunID: 5001, SUTID: 70, AccountRole: "admin", Status: execution.SUTContextClosed}}}},
		Intents:          intents,
		WaveFingerprints: NewExecutionWaveFingerprintReader(),
	}
}

// TestWaveFrozenFingerprintConsumed asserts the report wave-level environment
// fingerprint consumes execution.WaveFrozenFingerprint (SC-G6 P15): the wave
// snapshot carries the reconstructed frozen fingerprint and is verifiable. A
// legacy wave (no carrier) is fail-closed at the fingerprint level but surfaces
// as a per-wave "环境不可核验" annotation — the version report still generates
// (not a whole-version block).
func TestWaveFrozenFingerprintConsumed(t *testing.T) {
	sut := testSUTBinding()
	scopeRaw, fp, baseline := testWaveScopeSnapshot(t, 7, []executionapp.SUTBinding{sut})
	deps := newWaveFingerprintFixture(t, scopeRaw, fp, baseline)
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("VersionSnapshot: %v", err)
	}
	if len(snapshot.Waves) != 1 {
		t.Fatalf("waves len = %d, want 1", len(snapshot.Waves))
	}
	wave := snapshot.Waves[0]
	if !wave.EnvironmentVerifiable {
		t.Fatalf("wave environment verifiable = false, want true (valid frozen carrier); reason=%q", wave.EnvironmentUnverifiableReason)
	}
	if wave.EnvironmentFingerprint != fp {
		t.Errorf("wave environment fingerprint = %q, want the WaveFrozenFingerprint result %q", wave.EnvironmentFingerprint, fp)
	}
	if wave.CodeBaseline != baseline {
		t.Errorf("wave code baseline = %q, want %q", wave.CodeBaseline, baseline)
	}
	// The planned environment items are projected for human reading.
	if len(wave.PlannedEnvironment) != 1 || wave.PlannedEnvironment[0].BaseURL != "https://planned.example.com" {
		t.Errorf("wave planned environment = %+v, want base_url https://planned.example.com", wave.PlannedEnvironment)
	}
	// Carrier == per-run actual: no divergence.
	if len(wave.EnvironmentDivergences) != 0 {
		t.Errorf("divergences = %+v, want none when carrier matches the run intent", wave.EnvironmentDivergences)
	}
}

// TestWaveFrozenFingerprintLegacyPerWaveAnnotation pins the legacy-wave human
// behavior (SC-G6 I5): a wave whose frozen snapshot has no carrier is annotated
// "环境不可核验" per-wave, and the version report still generates (the legacy
// wave does not block the whole version report).
func TestWaveFrozenFingerprintLegacyPerWaveAnnotation(t *testing.T) {
	// Legacy wave: empty scope snapshot (no carrier). WaveFrozenFingerprint fails
	// closed; the report annotates the wave rather than blocking the version.
	deps := newWaveFingerprintFixture(t, "{}", strings.Repeat("b", 64), "")
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("the version report must still generate with a legacy wave (per-wave annotation, not a version block): %v", err)
	}
	if len(snapshot.Waves) != 1 {
		t.Fatalf("waves len = %d, want 1", len(snapshot.Waves))
	}
	wave := snapshot.Waves[0]
	if wave.EnvironmentVerifiable {
		t.Fatal("legacy wave environment verifiable = true, want false (no frozen carrier)")
	}
	if !strings.Contains(wave.EnvironmentUnverifiableReason, "环境不可核验") {
		t.Errorf("legacy wave reason = %q, want it to contain 环境不可核验", wave.EnvironmentUnverifiableReason)
	}
}

// TestWaveFrozenFingerprintTamperedCarrierIntegrity pins the fix-round honesty
// guard (art.7.6): a wave whose recorded carrier DISAGREES with its frozen inputs
// is an integrity (tamper/corruption) signal, NOT a legacy wave. The report reason
// must name the carrier-vs-input mismatch and must NOT claim "no carrier/legacy".
// Fail-closed is preserved (verifiable=false, report still generates).
func TestWaveFrozenFingerprintTamperedCarrierIntegrity(t *testing.T) {
	sut := testSUTBinding()
	scopeRaw, fp, baseline := testWaveScopeSnapshot(t, 7, []executionapp.SUTBinding{sut})
	// Tamper: keep the recorded carrier but move a frozen page id, so the
	// reconstruction diverges from the carrier (the F fail-closed integrity gate).
	tampered := strings.Replace(scopeRaw, `"page_ids":[1,2]`, `"page_ids":[1,2,3]`, 1)
	if tampered == scopeRaw {
		t.Fatalf("fixture did not tamper the snapshot: %s", scopeRaw)
	}
	deps := newWaveFingerprintFixture(t, tampered, fp, baseline)
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("the version report must still generate (per-wave annotation, not a block): %v", err)
	}
	wave := snapshot.Waves[0]
	if wave.EnvironmentVerifiable {
		t.Fatal("tampered carrier wave verifiable = true, want false (integrity fail-closed)")
	}
	if !strings.Contains(wave.EnvironmentUnverifiableReason, "冻结载体与冻结输入不一致") {
		t.Errorf("tampered reason = %q, want the carrier-vs-input integrity wording", wave.EnvironmentUnverifiableReason)
	}
	if strings.Contains(wave.EnvironmentUnverifiableReason, "legacy") || strings.Contains(wave.EnvironmentUnverifiableReason, "无载体") {
		t.Errorf("tampered reason = %q, must NOT claim legacy/no-carrier (masks the integrity failure)", wave.EnvironmentUnverifiableReason)
	}
}

// TestWaveFrozenFingerprintMalformedSnapshot pins the parse-failure class: a
// non-decodable scope snapshot is annotated as a parse failure, distinct from both
// legacy (no carrier) and integrity (carrier mismatch).
func TestWaveFrozenFingerprintMalformedSnapshot(t *testing.T) {
	deps := newWaveFingerprintFixture(t, "{not-json", strings.Repeat("b", 64), "")
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("the version report must still generate: %v", err)
	}
	wave := snapshot.Waves[0]
	if wave.EnvironmentVerifiable {
		t.Fatal("malformed snapshot wave verifiable = true, want false")
	}
	if !strings.Contains(wave.EnvironmentUnverifiableReason, "无法解析") {
		t.Errorf("malformed reason = %q, want the parse-failure wording", wave.EnvironmentUnverifiableReason)
	}
	if strings.Contains(wave.EnvironmentUnverifiableReason, "legacy") || strings.Contains(wave.EnvironmentUnverifiableReason, "无载体") {
		t.Errorf("malformed reason = %q, must NOT claim legacy/no-carrier", wave.EnvironmentUnverifiableReason)
	}
}

// TestCarrierDivergenceReadable asserts the carrier-vs-per-run divergence is
// disclosed readably (SC-G6 R-G1/M1): when the wave carrier fingerprint differs
// from the member run's actual intent fingerprint, the report shows planned vs
// actual environment plus a readable difference naming the env dimensions
// (base_url / asset set / account roles / code baseline) — NOT just two sha256
// strings. Divergence is disclosed, not blocked.
func TestCarrierDivergenceReadable(t *testing.T) {
	sut := testSUTBinding()
	scopeRaw, carrierFP, carrierBaseline := testWaveScopeSnapshot(t, 7, []executionapp.SUTBinding{sut})
	// The member run executed under a DIFFERENT environment (intent fp/baseline).
	actualFP := strings.Repeat("c", 64)
	actualBaseline := strings.Repeat("d", 64)
	deps := newWaveFingerprintFixture(t, scopeRaw, actualFP, actualBaseline)
	reader := NewReportDataReader(deps)
	snapshot, err := reader.VersionSnapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("divergence must be disclosed, not block the report: %v", err)
	}
	wave := snapshot.Waves[0]
	if !wave.EnvironmentVerifiable {
		t.Fatalf("wave verifiable = false, want true (the carrier itself is valid)")
	}
	if len(wave.EnvironmentDivergences) != 1 {
		t.Fatalf("divergences len = %d, want 1 (carrier fp != run intent fp)", len(wave.EnvironmentDivergences))
	}
	div := wave.EnvironmentDivergences[0]
	if div.CaseRunID != 5001 {
		t.Errorf("divergence run = %d, want 5001", div.CaseRunID)
	}
	if div.PlannedFingerprint != carrierFP || div.ActualFingerprint != actualFP {
		t.Errorf("divergence planned/actual fp = %q/%q, want %q/%q", div.PlannedFingerprint, div.ActualFingerprint, carrierFP, actualFP)
	}
	if div.PlannedCodeBaseline != carrierBaseline || div.ActualCodeBaseline != actualBaseline {
		t.Errorf("divergence planned/actual baseline = %q/%q, want %q/%q", div.PlannedCodeBaseline, div.ActualCodeBaseline, carrierBaseline, actualBaseline)
	}
	if len(div.Differences) == 0 {
		t.Fatal("divergence Differences is empty; a readable diff is required, not just two hashes")
	}
	joined := strings.Join(div.Differences, "\n")
	// The readable diff names the environment dimensions and the planned config,
	// not merely the two fingerprint hashes.
	for _, want := range []string{"base_url", "https://planned.example.com", "代码基线", "账号角色"} {
		if !strings.Contains(joined, want) {
			t.Errorf("readable diff missing %q; got:\n%s", want, joined)
		}
	}
}
