package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// visualPNG is a byte string with a valid PNG signature. The recorder never
// decodes it (the executor does); the evidence service only checks the magic.
var visualPNG = []byte("\x89PNG\r\n\x1a\nbaseline-and-actual-pixels")

// visualAssertSnapshot is a one-step case with a single kind=visual assertion.
// baselineID nil models an assertion that was never approved.
func visualAssertSnapshot(baselineID *int64) domain.CaseSnapshot {
	return domain.CaseSnapshot{
		Node:  domain.CaseSnapshotNode{ID: 10, Title: "visual case", Priority: "P0"},
		Steps: []domain.CaseSnapshotStep{{Seq: 1, Action: "open receipt", Expected: "receipt visible"}},
		Assertions: []domain.CaseSnapshotAssertion{{
			ID: 31, StepSeq: 1, Kind: "visual", Target: "#receipt", Expected: "match",
			Visual: &domain.CaseSnapshotVisual{
				Selector: "#receipt", Threshold: 0.01,
				BaselineEvidenceID: baselineID,
				ViewportWidth:      1280, ViewportHeight: 720, DeviceScaleFactor: 1,
			},
		}},
		GraphHash: testBaselineHash,
	}
}

// visualComparatorFake is an in-memory VisualComparator.
type visualComparatorFake struct {
	result  VisualCompareResult
	err     error
	calls   int
	ref     ContextRef
	request VisualCompareRequest
}

func (f *visualComparatorFake) CompareVisual(_ context.Context, ref ContextRef, req VisualCompareRequest) (VisualCompareResult, error) {
	f.calls++
	f.ref = ref
	f.request = req
	return f.result, f.err
}

// visualBaselineReaderFake is an in-memory VisualBaselineReader.
type visualBaselineReaderFake struct {
	baseline VisualBaseline
	err      error
	calls    int
	version  int64
	evidence int64
}

func (f *visualBaselineReaderFake) ReadVisualBaseline(_ context.Context, versionID, evidenceID int64) (VisualBaseline, error) {
	f.calls++
	f.version = versionID
	f.evidence = evidenceID
	if f.err != nil {
		return VisualBaseline{}, f.err
	}
	return f.baseline, nil
}

// visualTestWorld wires a recorder and the visual ports. Capture uses a real
// EvidenceService over the in-memory repositories so the persisted evidence rows
// (sha256/mime/size) are exercised.
func visualTestWorld(t *testing.T) (*CaseRecorder, *recWorld, *recEvidenceStore, *visualComparatorFake, *visualBaselineReaderFake, VisualAssertOptions) {
	t.Helper()
	recorder, world := newRecorderTest(t)
	world.ids.next = 1000
	store := &recEvidenceStore{}
	capture := NewEvidenceService(world.evidence, world.refs, world.batches, store, world.ids)
	comparator := &visualComparatorFake{result: VisualCompareResult{
		EnvironmentMatch: true,
		ActualPNG:        visualPNG,
		ActualWidth:      1280, ActualHeight: 720,
	}}
	reader := &visualBaselineReaderFake{baseline: VisualBaseline{
		EvidenceID: 501, Kind: "visual_baseline", Mime: "image/png", Data: visualPNG,
	}}
	opts := VisualAssertOptions{
		Comparator:       comparator,
		Context:          ContextRef{ID: "ctx-1"},
		Baselines:        reader,
		Capture:          capture,
		MaxBaselineBytes: DefaultVisualBaselineMaxBytes,
		MaxArtifactBytes: DefaultEvidenceMaxBytes,
	}
	return recorder, world, store, comparator, reader, opts
}

// baselineID returns an approved baseline evidence id.
func baselineID(value int64) *int64 { return &value }

// recordVisualStep records the snapshot's only step.
func recordVisualStep(t *testing.T, recorder *CaseRecorder, caseRunID int64) {
	t.Helper()
	if _, err := recorder.RecordStep(context.Background(), caseRunID, StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "done",
	}); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}
}

// visualResultOf returns the single recorded assertion result of the run.
func visualResultOf(t *testing.T, world *recWorld, caseRunID int64) domain.AssertionResult {
	t.Helper()
	results := world.assertions.rows[caseRunID]
	if len(results) != 1 {
		t.Fatalf("assertion results = %+v, want one", results)
	}
	return results[0]
}

// visualEvidenceOf returns the captured evidence rows of one kind.
func visualEvidenceOf(t *testing.T, world *recWorld, caseRunID int64, kind string) []domain.Evidence {
	t.Helper()
	rows, err := world.evidence.ListByCaseRun(context.Background(), caseRunID)
	if err != nil {
		t.Fatalf("ListByCaseRun: %v", err)
	}
	out := make([]domain.Evidence, 0, len(rows))
	for _, row := range rows {
		if row.Kind == kind {
			out = append(out, row)
		}
	}
	return out
}

// TestVisualAssertMissingBaselineRecordsNotRun pins §9.3: an assertion without
// an approved baseline is inconclusive(case, missing_visual_baseline); it never
// opens the browser, never compares pixels and the recorded result lets the
// agent submit the prescribed finish.
func TestVisualAssertMissingBaselineRecordsNotRun(t *testing.T) {
	recorder, world, _, comparator, reader, opts := visualTestWorld(t)
	run := world.seedRunningRun(visualAssertSnapshot(nil))
	recordVisualStep(t, recorder, run.ID)

	outcome, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	wantToolError(t, err, "missing_visual_baseline", "inconclusive(case)")
	if outcome.Verdict != "not_run" || outcome.Classification != string(domain.VerdictInconclusive) ||
		outcome.FailureClass != string(domain.FailureClassCase) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if comparator.calls != 0 || reader.calls != 0 {
		t.Fatalf("comparator/baseline calls = %d/%d, want 0/0", comparator.calls, reader.calls)
	}
	result := visualResultOf(t, world, run.ID)
	if result.Kind != "visual" || result.Verdict != "not_run" || result.FailureReason != "missing_visual_baseline" {
		t.Fatalf("assertion result = %+v", result)
	}
	if rows := visualEvidenceOf(t, world, run.ID, "visual_actual"); len(rows) != 0 {
		t.Fatalf("missing baseline captured actual evidence: %+v", rows)
	}

	// The prescribed finish is admissible: the not_run result satisfies the
	// finish gate and inconclusive(case) needs no evidence.
	if _, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{
			Verdict: domain.VerdictInconclusive, FailureClass: failureClassPointer(domain.FailureClassCase),
			Reason: "missing_visual_baseline",
		},
	}); err != nil {
		t.Fatalf("FinishCase inconclusive(case): %v", err)
	}
}

// TestVisualAssertEnvironmentMismatchIsInconclusiveEnvironment pins the §9.3
// environment fingerprint rule: a live viewport/DPR mismatch is recorded as
// not_run with visual_environment_mismatch, captures the actual screenshot only,
// and admits the inconclusive(environment) finish.
func TestVisualAssertEnvironmentMismatchIsInconclusiveEnvironment(t *testing.T) {
	recorder, world, _, comparator, _, opts := visualTestWorld(t)
	comparator.result = VisualCompareResult{
		EnvironmentMatch: false, ActualPNG: visualPNG,
		ActualWidth: 1280, ActualHeight: 720, ActualDeviceScaleFactor: 2,
	}
	run := world.seedRunningRun(visualAssertSnapshot(baselineID(501)))
	recordVisualStep(t, recorder, run.ID)

	outcome, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	wantToolError(t, err, "visual_environment_mismatch", "inconclusive(environment)")
	if outcome.Verdict != "not_run" ||
		outcome.Classification != string(domain.VerdictInconclusive) ||
		outcome.FailureClass != string(domain.FailureClassEnvironment) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if comparator.calls != 1 {
		t.Fatalf("comparator calls = %d, want 1", comparator.calls)
	}
	result := visualResultOf(t, world, run.ID)
	if result.Verdict != "not_run" || result.FailureReason != "visual_environment_mismatch" {
		t.Fatalf("assertion result = %+v", result)
	}
	if rows := visualEvidenceOf(t, world, run.ID, "visual_actual"); len(rows) != 1 {
		t.Fatalf("environment mismatch actual evidence = %+v, want 1", rows)
	}
	if rows := visualEvidenceOf(t, world, run.ID, "visual_diff"); len(rows) != 0 {
		t.Fatalf("environment mismatch captured a diff: %+v", rows)
	}

	if _, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{
			Verdict: domain.VerdictInconclusive, FailureClass: failureClassPointer(domain.FailureClassEnvironment),
			Reason: "visual_environment_mismatch",
		},
	}); err != nil {
		t.Fatalf("FinishCase inconclusive(environment): %v", err)
	}
}

// TestVisualAssertDiffAboveThresholdFails pins the only application-failing
// outcome of §9.3 and the visual_actual/visual_diff evidence pair.
func TestVisualAssertDiffAboveThresholdFails(t *testing.T) {
	recorder, world, _, comparator, _, opts := visualTestWorld(t)
	comparator.result = VisualCompareResult{
		EnvironmentMatch: true, ActualPNG: visualPNG, DiffPNG: []byte("\x89PNG\r\n\x1a\ndiff"),
		DiffRatio: 0.02, Width: 1280, Height: 720, ActualWidth: 1280, ActualHeight: 720,
	}
	run := world.seedRunningRun(visualAssertSnapshot(baselineID(501)))
	recordVisualStep(t, recorder, run.ID)

	outcome, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	if err != nil {
		t.Fatalf("VisualAssert: %v", err)
	}
	if outcome.Verdict != "failed" || outcome.Classification != string(domain.VerdictFailed) ||
		outcome.FailureClass != string(domain.FailureClassApplication) ||
		outcome.DiffRatio != 0.02 || outcome.Threshold != 0.01 || outcome.Reason != "visual_mismatch" {
		t.Fatalf("outcome = %+v", outcome)
	}
	result := visualResultOf(t, world, run.ID)
	if result.Verdict != "failed" || result.FailureReason != "visual_mismatch" {
		t.Fatalf("assertion result = %+v", result)
	}

	actuals := visualEvidenceOf(t, world, run.ID, "visual_actual")
	diffs := visualEvidenceOf(t, world, run.ID, "visual_diff")
	if len(actuals) != 1 || len(diffs) != 1 {
		t.Fatalf("evidence actual/diff = %d/%d, want 1/1", len(actuals), len(diffs))
	}
	actual := actuals[0]
	sum := sha256.Sum256(visualPNG)
	if actual.Mime != "image/png" || actual.SizeBytes != int64(len(visualPNG)) ||
		!strings.EqualFold(actual.SHA256, hex.EncodeToString(sum[:])) {
		t.Fatalf("actual evidence metadata = %+v", actual)
	}
	if actual.Metadata["assertion_id"] != "31" || metadataInt(actual.Metadata["width"]) != 1280 ||
		metadataInt(actual.Metadata["height"]) != 720 ||
		metadataInt(actual.Metadata["assertion_result_id"]) != int(result.ID) {
		t.Fatalf("actual evidence metadata = %+v", actual.Metadata)
	}

	if _, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{
			Verdict: domain.VerdictFailed, FailureClass: failureClassPointer(domain.FailureClassApplication),
			Reason: "visual_mismatch",
		},
	}); err != nil {
		t.Fatalf("FinishCase failed(application): %v", err)
	}
}

// TestVisualAssertWithinThresholdPasses pins the pass boundary: diff at or below
// the threshold records a passed assertion and admits a passed finish.
func TestVisualAssertWithinThresholdPasses(t *testing.T) {
	recorder, world, _, comparator, reader, opts := visualTestWorld(t)
	comparator.result = VisualCompareResult{
		EnvironmentMatch: true, ActualPNG: visualPNG, DiffPNG: []byte("\x89PNG\r\n\x1a\ndiff"),
		DiffRatio: 0.01, Width: 1280, Height: 720, ActualWidth: 1280, ActualHeight: 720,
	}
	run := world.seedRunningRun(visualAssertSnapshot(baselineID(501)))
	recordVisualStep(t, recorder, run.ID)

	outcome, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	if err != nil {
		t.Fatalf("VisualAssert: %v", err)
	}
	if outcome.Verdict != "passed" || outcome.Classification != string(domain.VerdictPassed) ||
		outcome.FailureClass != "" || outcome.Reason != "visual_match" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if reader.calls != 1 || reader.version != schedVersionID || reader.evidence != 501 {
		t.Fatalf("baseline read = calls %d version %d evidence %d", reader.calls, reader.version, reader.evidence)
	}
	if comparator.request.Selector != "#receipt" || comparator.request.Threshold != 0.01 ||
		comparator.request.Viewport.Width != 1280 || comparator.request.Viewport.Height != 720 ||
		comparator.request.DeviceScaleFactor != 1 ||
		string(comparator.request.Baseline) != string(visualPNG) {
		t.Fatalf("comparison request = %+v", comparator.request)
	}
	result := visualResultOf(t, world, run.ID)
	if result.Verdict != "passed" || result.FailureReason != "" {
		t.Fatalf("assertion result = %+v", result)
	}

	if _, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
	}); err != nil {
		t.Fatalf("FinishCase passed: %v", err)
	}
}

// TestVisualAssertContextErrorRecordsNotRun pins the fail-closed context path:
// when the browser context cannot be created, the assertion is still submitted
// (not_run with the executor code) so the agent can finish blocked(environment).
func TestVisualAssertContextErrorRecordsNotRun(t *testing.T) {
	recorder, world, _, comparator, reader, opts := visualTestWorld(t)
	opts.ContextError = NewBrowserError("executor_unavailable", "executor is down")
	run := world.seedRunningRun(visualAssertSnapshot(baselineID(501)))
	recordVisualStep(t, recorder, run.ID)

	outcome, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	var browserErr *BrowserError
	if !errors.As(err, &browserErr) || browserErr.Code != "executor_unavailable" {
		t.Fatalf("error = %v, want the typed executor_unavailable BrowserError", err)
	}
	if outcome.Verdict != "not_run" || outcome.FailureReason != "executor_unavailable" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if comparator.calls != 0 || reader.calls != 0 {
		t.Fatalf("comparator/baseline calls = %d/%d, want 0/0", comparator.calls, reader.calls)
	}
	if result := visualResultOf(t, world, run.ID); result.Verdict != "not_run" ||
		result.FailureReason != "executor_unavailable" {
		t.Fatalf("assertion result = %+v", result)
	}
	if _, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{
			Verdict: domain.VerdictBlocked, FailureClass: failureClassPointer(domain.FailureClassEnvironment),
			Reason: "executor_unavailable",
		},
	}); err != nil {
		t.Fatalf("FinishCase blocked(environment): %v", err)
	}
}

// TestVisualAssertIsIdempotent pins the §9.3 idempotency rule: a repeated call
// replays the recorded outcome instead of comparing again.
func TestVisualAssertIsIdempotent(t *testing.T) {
	recorder, world, _, comparator, reader, opts := visualTestWorld(t)
	run := world.seedRunningRun(visualAssertSnapshot(baselineID(501)))

	first, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	if err != nil {
		t.Fatalf("first VisualAssert: %v", err)
	}
	second, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	if err != nil {
		t.Fatalf("second VisualAssert: %v", err)
	}
	if comparator.calls != 1 || reader.calls != 1 {
		t.Fatalf("comparator/baseline calls = %d/%d, want 1/1", comparator.calls, reader.calls)
	}
	if !second.Repeated || second.Verdict != first.Verdict ||
		second.ActualEvidenceID != first.ActualEvidenceID ||
		second.DiffEvidenceID != first.DiffEvidenceID {
		t.Fatalf("replayed outcome = %+v, first = %+v", second, first)
	}
	if results := world.assertions.rows[run.ID]; len(results) != 1 {
		t.Fatalf("assertion results = %d, want 1", len(results))
	}
}

// TestVisualAssertRejectsNonVisualAssertion keeps the tool scoped to kind=visual.
func TestVisualAssertRejectsNonVisualAssertion(t *testing.T) {
	recorder, world, _, comparator, _, opts := visualTestWorld(t)
	snapshot := visualAssertSnapshot(baselineID(501))
	snapshot.Assertions[0].Kind = "ui"
	run := world.seedRunningRun(snapshot)

	_, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	wantToolError(t, err, "invalid_arguments", "kind")
	if comparator.calls != 0 {
		t.Fatalf("comparator calls = %d, want 0", comparator.calls)
	}
	if results := world.assertions.rows[run.ID]; len(results) != 0 {
		t.Fatalf("rejected assertion wrote a result: %+v", results)
	}
}

// TestVisualAssertUnsupportedComparatorFailsClosed ensures a browser adapter
// that cannot compare visuals never fakes a pass.
func TestVisualAssertUnsupportedComparatorFailsClosed(t *testing.T) {
	recorder, world, _, _, _, opts := visualTestWorld(t)
	opts.Comparator = nil
	run := world.seedRunningRun(visualAssertSnapshot(baselineID(501)))

	outcome, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	wantToolError(t, err, "unsupported_assertion_kind", "inconclusive(case)")
	if outcome.Verdict != "not_run" || outcome.FailureClass != string(domain.FailureClassCase) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if result := visualResultOf(t, world, run.ID); result.FailureReason != "unsupported_assertion_kind" {
		t.Fatalf("assertion result = %+v", result)
	}
}

// failingDiffCapture delegates to the real EvidenceService but fails the
// visual_diff capture, modelling a storage failure after the visual_actual
// screenshot was already committed.
type failingDiffCapture struct {
	inner EvidenceCapturePort
}

func (f failingDiffCapture) Capture(ctx context.Context, in CaptureInput) (domain.Evidence, error) {
	if in.Kind == visualDiffEvidenceKind {
		return domain.Evidence{}, errors.New("evidence store is full")
	}
	return f.inner.Capture(ctx, in)
}

// TestVisualAssertDiffCaptureFailureKeepsEvidenceResultIdsConsistent pins fix
// round 1 finding 6: when the diff capture fails after the actual screenshot was
// persisted, the recorded not_run result must reuse the assertion_result id the
// actual evidence metadata points at, and the actual evidence must be referenced
// from that result. Otherwise the evidence would reference a result id that was
// never stored.
func TestVisualAssertDiffCaptureFailureKeepsEvidenceResultIdsConsistent(t *testing.T) {
	recorder, world, store, comparator, _, opts := visualTestWorld(t)
	comparator.result = VisualCompareResult{
		EnvironmentMatch: true, ActualPNG: visualPNG, DiffPNG: []byte("\x89PNG\r\n\x1a\ndiff"),
		DiffRatio: 0.02, Width: 1280, Height: 720, ActualWidth: 1280, ActualHeight: 720,
	}
	opts.Capture = failingDiffCapture{inner: NewEvidenceService(world.evidence, world.refs, world.batches, store, world.ids)}
	run := world.seedRunningRun(visualAssertSnapshot(baselineID(501)))
	recordVisualStep(t, recorder, run.ID)

	outcome, err := recorder.VisualAssert(context.Background(), run.ID, 31, opts)
	wantToolError(t, err, "internal", "visual_evidence_capture_failed")
	if outcome.Verdict != "not_run" || outcome.FailureReason != "visual_evidence_capture_failed" {
		t.Fatalf("outcome = %+v", outcome)
	}

	result := visualResultOf(t, world, run.ID)
	if result.Verdict != "not_run" || result.FailureReason != "visual_evidence_capture_failed" {
		t.Fatalf("assertion result = %+v", result)
	}
	actuals := visualEvidenceOf(t, world, run.ID, "visual_actual")
	if len(actuals) != 1 {
		t.Fatalf("visual_actual evidence = %d, want 1", len(actuals))
	}
	referencedResultID := metadataInt(actuals[0].Metadata["assertion_result_id"])
	if referencedResultID != int(result.ID) {
		t.Fatalf("actual evidence references assertion_result %d, persisted result is %d",
			referencedResultID, result.ID)
	}
	if outcome.ActualEvidenceID != actuals[0].ID {
		t.Fatalf("outcome actual evidence = %d, persisted %d", outcome.ActualEvidenceID, actuals[0].ID)
	}
	if diffs := visualEvidenceOf(t, world, run.ID, "visual_diff"); len(diffs) != 0 {
		t.Fatalf("failed diff capture wrote evidence: %+v", diffs)
	}

	// The actual screenshot is an artifact of the recorded result, so the
	// evidence ref must exist even on the diff-failure path.
	foundRef := false
	for _, ref := range world.refs.all() {
		if ref.EvidenceID == actuals[0].ID && ref.RefKind == "assertion_result" && ref.RefID == result.ID {
			foundRef = true
		}
	}
	if !foundRef {
		t.Fatalf("no assertion_result ref for evidence %d -> result %d", actuals[0].ID, result.ID)
	}
}

// TestReadVisualBaselineKinds pins the execution-side baseline reader: only a
// screenshot/visual_baseline PNG is comparable; any other artifact is rejected.
func TestReadVisualBaselineKinds(t *testing.T) {
	for _, kind := range []string{"screenshot", "visual_baseline"} {
		recorder, world := newRecorderTest(t)
		_ = recorder
		store := &recEvidenceStore{}
		service := NewEvidenceService(world.evidence, world.refs, world.batches, store, world.ids)
		evidence := seedVisualEvidence(t, world, store, kind, visualPNG)

		baseline, err := service.ReadVisualBaseline(context.Background(), schedVersionID, evidence.ID)
		if err != nil {
			t.Fatalf("kind %q: ReadVisualBaseline: %v", kind, err)
		}
		if baseline.Kind != kind || string(baseline.Data) != string(visualPNG) || baseline.EvidenceID != evidence.ID {
			t.Fatalf("kind %q: baseline = %+v", kind, baseline)
		}
	}

	_, world := newRecorderTest(t)
	store := &recEvidenceStore{}
	service := NewEvidenceService(world.evidence, world.refs, world.batches, store, world.ids)
	evidence := seedVisualEvidence(t, world, store, "download", visualPNG)
	if _, err := service.ReadVisualBaseline(context.Background(), schedVersionID, evidence.ID); err == nil {
		t.Fatal("a download artifact was accepted as a visual baseline")
	}

	corrupt := seedVisualEvidence(t, world, store, "screenshot", []byte("not a png"))
	if _, err := service.ReadVisualBaseline(context.Background(), schedVersionID, corrupt.ID); err == nil {
		t.Fatal("a non-PNG artifact was accepted as a visual baseline")
	}
}

// seedVisualEvidence writes one artifact through the evidence store and records
// its metadata row (the EvidenceService.Open read path verifies sha256/size).
func seedVisualEvidence(t *testing.T, world *recWorld, store *recEvidenceStore, kind string, data []byte) domain.Evidence {
	t.Helper()
	path := fmt.Sprintf("55/2/%s-%d.png", kind, len(data))
	storedPath, sum, size, err := store.Write(context.Background(), path, data)
	if err != nil {
		t.Fatalf("store.Write: %v", err)
	}
	caseRunID := int64(2)
	return world.evidence.seed(domain.Evidence{
		BatchID:   schedBatchID,
		CaseRunID: &caseRunID,
		Kind:      kind,
		Path:      storedPath,
		SHA256:    sum,
		SizeBytes: size,
		Mime:      "image/png",
	})
}
