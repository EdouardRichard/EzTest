package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEvalFile writes one JSON document below a temp dir.
func writeEvalFile(t *testing.T, dir, name, document string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

const evalVisualCorpus = `{
  "corpusVersion": "2026-10-29.1",
  "scenario": {
    "assertion": {
      "kind": "visual",
      "selector": "#receipt",
      "region": {"x": 0, "y": 0, "width": 1, "height": 1},
      "threshold": 0.01,
      "viewport": {"width": 1280, "height": 720},
      "deviceScaleFactor": 1
    },
    "baseline": {"evidenceKind": "visual_baseline", "mime": "image/png", "approval": "admin", "imageSize": {"width": 1280, "height": 720}},
    "evidence": {"actualKind": "visual_actual", "diffKind": "visual_diff", "mime": "image/png"}
  }
}`

const evalVisualTruth = `{
  "corpusVersion": "2026-10-29.1",
  "metric": "visual_classification_accuracy",
  "threshold": 1.0,
  "cases": [
    {"name": "missing baseline", "hasBaseline": false, "environmentMatches": true, "diffRatio": 0, "threshold": 0.01, "expectedVerdict": "inconclusive", "expectedFailureClass": "case", "expectedReason": "missing_visual_baseline"},
    {"name": "environment mismatch", "hasBaseline": true, "environmentMatches": false, "diffRatio": 0.5, "threshold": 0.01, "expectedVerdict": "inconclusive", "expectedFailureClass": "environment", "expectedReason": "visual_environment_mismatch"},
    {"name": "diff above threshold", "hasBaseline": true, "environmentMatches": true, "diffRatio": 0.02, "threshold": 0.01, "expectedVerdict": "failed", "expectedFailureClass": "application", "expectedReason": "visual_mismatch"},
    {"name": "diff at threshold", "hasBaseline": true, "environmentMatches": true, "diffRatio": 0.01, "threshold": 0.01, "expectedVerdict": "passed", "expectedFailureClass": "", "expectedReason": "visual_match"}
  ]
}`

// TestEvaluateMatchesGroundTruth pins the eval helper: every ground-truth case
// is replayed through the production domain.ClassifyVisual and scored.
func TestEvaluateMatchesGroundTruth(t *testing.T) {
	dir := t.TempDir()
	corpusPath := writeEvalFile(t, dir, "visual.json", evalVisualCorpus)
	truthPath := writeEvalFile(t, dir, "visual-truth.json", evalVisualTruth)

	result, err := evaluate(corpusPath, truthPath)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Metric != "visual_classification_accuracy" || result.Total != 4 || result.Correct != 4 {
		t.Fatalf("result = %+v", result)
	}
	if result.Accuracy != 1 || !result.Passed {
		t.Fatalf("accuracy/passed = %v/%v", result.Accuracy, result.Passed)
	}
	if result.Scenario.Selector != "#receipt" || result.Scenario.Threshold != 0.01 ||
		result.Scenario.ViewportWidth != 1280 || result.Scenario.ViewportHeight != 720 {
		t.Fatalf("scenario echo = %+v", result.Scenario)
	}
}

// TestEvaluateDetectsClassificationDrift proves the helper fails the gate when
// a ground-truth expectation does not match the production classifier.
func TestEvaluateDetectsClassificationDrift(t *testing.T) {
	dir := t.TempDir()
	corpusPath := writeEvalFile(t, dir, "visual.json", evalVisualCorpus)
	truthPath := writeEvalFile(t, dir, "visual-truth.json",
		`{"corpusVersion":"2026-10-29.1","metric":"visual_classification_accuracy","threshold":1.0,"cases":[
			{"name":"drift","hasBaseline":true,"environmentMatches":true,"diffRatio":0.02,"threshold":0.01,
			 "expectedVerdict":"passed","expectedFailureClass":"","expectedReason":"visual_match"}]}`)

	result, err := evaluate(corpusPath, truthPath)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Correct != 0 || result.Passed || result.Accuracy != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// TestEvaluateRejectsBrokenInputs pins the corpus validation: version drift, a
// non-visual scenario and an unsupported baseline artifact are errors.
func TestEvaluateRejectsBrokenInputs(t *testing.T) {
	dir := t.TempDir()
	corpusPath := writeEvalFile(t, dir, "visual.json", evalVisualCorpus)
	truthPath := writeEvalFile(t, dir, "visual-truth.json", evalVisualTruth)

	versionDrift := writeEvalFile(t, dir, "drift.json",
		strings.Replace(evalVisualTruth, "2026-10-29.1", "2026-10-30.1", 1))
	if _, err := evaluate(corpusPath, versionDrift); err == nil {
		t.Fatal("corpusVersion drift was accepted")
	}

	badScenario := writeEvalFile(t, dir, "bad-scenario.json",
		strings.Replace(evalVisualCorpus, `"evidenceKind": "visual_baseline"`, `"evidenceKind": "download"`, 1))
	if _, err := evaluate(badScenario, truthPath); err == nil {
		t.Fatal("a download baseline was accepted")
	}

	badTruth := writeEvalFile(t, dir, "bad-truth.json",
		`{"corpusVersion":"2026-10-29.1","metric":"visual_classification_accuracy","threshold":1.0,"cases":[]}`)
	if _, err := evaluate(corpusPath, badTruth); err == nil {
		t.Fatal("an empty ground truth was accepted")
	}
}

// TestResultMarshalIsStable guards the script contract: the helper writes the
// documented JSON keys the eval script parses.
func TestResultMarshalIsStable(t *testing.T) {
	encoded, err := json.Marshal(Result{Metric: "visual_classification_accuracy", Total: 1, Correct: 1, Accuracy: 1, Passed: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"metric"`, `"accuracy"`, `"correct"`, `"total"`, `"passed"`, `"cases"`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("result JSON %s is missing %s", encoded, key)
		}
	}
}
