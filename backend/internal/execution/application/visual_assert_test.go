package executionapp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

type fakeVisualPort struct {
	mu          sync.Mutex
	config      executionapp.VisualAssertConfig
	configErr   error
	baseline    executionapp.VisualBaselineRef
	hasBaseline bool
	diffs       map[int64]executionapp.VisualDiffRecord
	recordCalls []executionapp.VisualDiffInput
	recordErr   error
}

func (f *fakeVisualPort) Config(context.Context, int64, int64) (executionapp.VisualAssertConfig, error) {
	return f.config, f.configErr
}

func (f *fakeVisualPort) ActiveBaseline(context.Context, int64) (executionapp.VisualBaselineRef, bool, error) {
	return f.baseline, f.hasBaseline, nil
}

func (f *fakeVisualPort) RecordDiff(_ context.Context, in executionapp.VisualDiffInput) (executionapp.VisualDiffRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return executionapp.VisualDiffRecord{}, f.recordErr
	}
	f.recordCalls = append(f.recordCalls, in)
	if f.diffs == nil {
		f.diffs = map[int64]executionapp.VisualDiffRecord{}
	}
	record := executionapp.VisualDiffRecord{
		ID: 9001, AssertionResultID: in.AssertionResultID, VisualAssertionID: in.VisualAssertionID,
		BaselineID: in.BaselineID, ActualEvidenceID: in.ActualEvidenceID,
		DiffEvidenceID: in.DiffEvidenceID, DiffRatio: in.DiffRatio, Threshold: in.Threshold,
		Classification: in.Classification, Reason: in.Reason,
		EnvironmentMatch: in.EnvironmentMatch, Width: in.Width, Height: in.Height,
		MismatchBoxes: in.MismatchBoxes,
	}
	f.diffs[in.AssertionResultID] = record
	return record, nil
}

func (f *fakeVisualPort) DiffForAssertionResult(_ context.Context, assertionResultID int64) (executionapp.VisualDiffRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.diffs[assertionResultID]
	return record, ok, nil
}

func testVisualPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func visualArtifact(kind string, data []byte) executionapp.BrowserArtifact {
	return executionapp.BrowserArtifact{
		Kind: kind, MIME: "image/png", Base64: base64.StdEncoding.EncodeToString(data),
		SHA256: shaHex(data), Bytes: len(data),
	}
}

func visualCaseFixture(t *testing.T) (*caseRunnerFixture, executionapp.VisualAssertConfig) {
	t.Helper()
	fixture := newCaseRunnerFixture(t)
	fixture.cases.snapshot.Assertions = []execution.CaseAssertion{{
		StepSeq: 1, AssertionSeq: 2, Kind: "visual", Expected: "页面与基线一致", TargetVersionSUTID: 7,
	}}
	fixture.visual.config = executionapp.VisualAssertConfig{
		ID: 6001, VersionID: 10, CaseVersionID: 41, CaseAssertionID: 9001,
		StepSeq: 1, AssertionSeq: 2, VersionSUTID: 7,
		Region: json.RawMessage(`{}`), Threshold: 0.005,
		ViewportWidth: 100, ViewportHeight: 100, DeviceScaleFactor: 1,
		WaitPolicy: json.RawMessage(`{}`), ConfigRevision: 1,
	}
	return fixture, fixture.visual.config
}

func visualBrowserResult(t *testing.T, actual, diff []byte, environmentMatch bool, ratio float64) executionapp.BrowserActionResult {
	t.Helper()
	artifacts := []executionapp.BrowserArtifact{visualArtifact("actual_png", actual)}
	if diff != nil {
		artifacts = append(artifacts, visualArtifact("diff_png", diff))
	}
	payload, _ := json.Marshal(map[string]any{
		"diff_ratio":        ratio,
		"width":             100,
		"height":            100,
		"environment_match": environmentMatch,
		"mismatch_boxes":    []any{},
	})
	return executionapp.BrowserActionResult{OK: true, Result: payload, Artifacts: artifacts}
}

func passVisualStep(t *testing.T, fixture *caseRunnerFixture) {
	t.Helper()
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{Seq: 1, Status: "passed"}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
}

func evidenceKinds(fixture *caseRunnerFixture) []execution.EvidenceKind {
	fixture.evidence.mu.Lock()
	defer fixture.evidence.mu.Unlock()
	kinds := make([]execution.EvidenceKind, 0, len(fixture.evidence.rows))
	for _, row := range fixture.evidence.rows {
		kinds = append(kinds, row.Kind)
	}
	return kinds
}

func TestVisualAssertWithoutBaselineIsInconclusive(t *testing.T) {
	fixture, cfg := visualCaseFixture(t)
	prepareCaseRun(t, fixture)
	passVisualStep(t, fixture)
	actual := testVisualPNG(t, 100, 100)
	fixture.browser.result = visualBrowserResult(t, actual, nil, true, 0)

	out, err := fixture.runner.VisualAssert(context.Background(), p3Scope(), cfg.ID)
	if err != nil {
		t.Fatalf("VisualAssert: %v", err)
	}
	result := out.(map[string]any)
	if result["classification"] != "inconclusive" || result["reason"] != "missing_visual_baseline" {
		t.Fatalf("result = %+v", result)
	}
	kinds := evidenceKinds(fixture)
	if len(kinds) != 1 || kinds[0] != execution.EvidenceVisualActual {
		t.Fatalf("evidence kinds = %v", kinds)
	}
	recorded := fixture.results.assertions
	if len(recorded) != 1 {
		t.Fatalf("assertion results = %d", len(recorded))
	}
	for _, row := range recorded {
		if row.Kind != "visual" || row.Verdict != execution.AssertionInconclusive || row.Reason != "missing_visual_baseline" {
			t.Fatalf("assertion result = %+v", row)
		}
	}
	if len(fixture.visual.recordCalls) != 1 {
		t.Fatalf("diff records = %d", len(fixture.visual.recordCalls))
	}
	diff := fixture.visual.recordCalls[0]
	if diff.BaselineID != nil || diff.Classification != "inconclusive" || diff.Reason != "missing_visual_baseline" {
		t.Fatalf("diff record = %+v", diff)
	}
	if diff.DiffEvidenceID != nil {
		t.Fatalf("no-baseline run must not store a diff image")
	}

	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive || finished.Summary.VerdictReason != "missing_visual_baseline" {
		t.Fatalf("case_run = %s reason=%s", finished.Status, finished.Summary.VerdictReason)
	}
	if finished.Summary.VisualInconclusive != 1 || finished.Summary.VisualSkipped != 0 {
		t.Fatalf("visual summary = %+v", finished.Summary)
	}
}

func TestVisualAssertWithMatchingBaselinePasses(t *testing.T) {
	fixture, cfg := visualCaseFixture(t)
	prepareCaseRun(t, fixture)
	passVisualStep(t, fixture)
	actual := testVisualPNG(t, 100, 100)
	diffImage := testVisualPNG(t, 100, 100)
	fixture.content.open = actual
	fixture.visual.hasBaseline = true
	fixture.visual.baseline = executionapp.VisualBaselineRef{
		ID: 5001, EvidenceID: 4001, SHA256: shaHex(actual), SizeBytes: int64(len(actual)),
		Width: 100, Height: 100, ConfigRevision: 1,
	}
	fixture.browser.result = visualBrowserResult(t, actual, diffImage, true, 0)

	out, err := fixture.runner.VisualAssert(context.Background(), p3Scope(), cfg.ID)
	if err != nil {
		t.Fatalf("VisualAssert: %v", err)
	}
	result := out.(map[string]any)
	if result["classification"] != "passed" || result["reason"] != "visual_match" {
		t.Fatalf("result = %+v", result)
	}
	kinds := evidenceKinds(fixture)
	if len(kinds) != 2 || kinds[0] != execution.EvidenceVisualActual || kinds[1] != execution.EvidenceVisualDiff {
		t.Fatalf("evidence kinds = %v", kinds)
	}
	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunPassed || finished.Summary.VisualPassed != 1 {
		t.Fatalf("case_run = %s summary=%+v", finished.Status, finished.Summary)
	}
}

func TestVisualAssertMismatchFailsApplication(t *testing.T) {
	fixture, cfg := visualCaseFixture(t)
	prepareCaseRun(t, fixture)
	passVisualStep(t, fixture)
	baseline := testVisualPNG(t, 100, 100)
	actual := testVisualPNG(t, 100, 100)
	fixture.content.open = baseline
	fixture.visual.hasBaseline = true
	fixture.visual.baseline = executionapp.VisualBaselineRef{
		ID: 5001, EvidenceID: 4001, SHA256: shaHex(baseline), SizeBytes: int64(len(baseline)),
		Width: 100, Height: 100, ConfigRevision: 1,
	}
	fixture.browser.result = visualBrowserResult(t, actual, testVisualPNG(t, 100, 100), true, 0.02)

	out, err := fixture.runner.VisualAssert(context.Background(), p3Scope(), cfg.ID)
	if err != nil {
		t.Fatalf("VisualAssert: %v", err)
	}
	result := out.(map[string]any)
	if result["classification"] != "failed" || result["reason"] != "visual_mismatch" {
		t.Fatalf("result = %+v", result)
	}
	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunFailed || finished.FailureClass != execution.FailureClassApplication {
		t.Fatalf("case_run = %s/%s", finished.Status, finished.FailureClass)
	}
	if finished.Summary.VisualFailed != 1 {
		t.Fatalf("visual failed = %d", finished.Summary.VisualFailed)
	}
}

func TestVisualAssertEnvironmentMismatchIsInconclusive(t *testing.T) {
	fixture, cfg := visualCaseFixture(t)
	prepareCaseRun(t, fixture)
	passVisualStep(t, fixture)
	baseline := testVisualPNG(t, 100, 100)
	fixture.content.open = baseline
	fixture.visual.hasBaseline = true
	fixture.visual.baseline = executionapp.VisualBaselineRef{
		ID: 5001, EvidenceID: 4001, SHA256: shaHex(baseline), SizeBytes: int64(len(baseline)),
		Width: 100, Height: 100, ConfigRevision: 1,
	}
	fixture.browser.result = visualBrowserResult(t, testVisualPNG(t, 100, 100), nil, false, 0)

	out, err := fixture.runner.VisualAssert(context.Background(), p3Scope(), cfg.ID)
	if err != nil {
		t.Fatalf("VisualAssert: %v", err)
	}
	result := out.(map[string]any)
	if result["classification"] != "inconclusive" || result["reason"] != "visual_environment_mismatch" {
		t.Fatalf("result = %+v", result)
	}
	if len(fixture.visual.recordCalls) != 1 || fixture.visual.recordCalls[0].EnvironmentMatch {
		t.Fatalf("diff record = %+v", fixture.visual.recordCalls)
	}
	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive || finished.FailureClass != execution.FailureClassEnvironment {
		t.Fatalf("case_run = %s/%s", finished.Status, finished.FailureClass)
	}
}

func TestVisualAssertReplayIsIdempotent(t *testing.T) {
	fixture, cfg := visualCaseFixture(t)
	prepareCaseRun(t, fixture)
	passVisualStep(t, fixture)
	actual := testVisualPNG(t, 100, 100)
	fixture.browser.result = visualBrowserResult(t, actual, nil, true, 0)
	scope := p3Scope()

	first, err := fixture.runner.VisualAssert(context.Background(), scope, cfg.ID)
	if err != nil {
		t.Fatalf("first VisualAssert: %v", err)
	}
	second, err := fixture.runner.VisualAssert(context.Background(), scope, cfg.ID)
	if err != nil {
		t.Fatalf("replay VisualAssert: %v", err)
	}
	if second.(map[string]any)["replayed"] != true {
		t.Fatalf("second call was not a replay: %+v", second)
	}
	if first.(map[string]any)["assertion_result_id"] != second.(map[string]any)["assertion_result_id"] {
		t.Fatalf("replay returned another assertion_result: %+v vs %+v", first, second)
	}
	if len(fixture.browser.actions) != 1 {
		t.Fatalf("executor actions = %v", fixture.browser.actions)
	}
	if len(fixture.visual.recordCalls) != 1 {
		t.Fatalf("diff records = %d", len(fixture.visual.recordCalls))
	}
	if len(fixture.evidence.rows) != 1 {
		t.Fatalf("evidence rows = %d", len(fixture.evidence.rows))
	}
	if len(fixture.results.assertions) != 1 {
		t.Fatalf("assertion results = %d", len(fixture.results.assertions))
	}
}

func TestVisualAssertionNotRunBlocksFalsePass(t *testing.T) {
	fixture, _ := visualCaseFixture(t)
	prepareCaseRun(t, fixture)
	passVisualStep(t, fixture)

	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunPassed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive || finished.Summary.VerdictReason != "visual_assertion_not_run" {
		t.Fatalf("case_run = %s reason=%s", finished.Status, finished.Summary.VerdictReason)
	}
	if finished.Summary.VisualSkipped != 0 || finished.Summary.VisualInconclusive != 1 {
		t.Fatalf("summary = %+v", finished.Summary)
	}
}
