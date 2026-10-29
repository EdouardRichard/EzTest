// Command evalvisual evaluates the visual-assertion ground truth (design spec
// §9.3) through the production execution-domain classifier.
//
// It exists so scripts/eval.ps1 can consume eval/corpus/visual.json and
// eval/ground-truth/visual.json without duplicating the four-state mapping in
// JavaScript: the script runs this command (Go toolchain is already part of the
// evaluation preflight) and records visual_classification_accuracy as a detail
// metric next to the six §9.1 metrics. The command needs no database, executor
// or LLM, so -DryRun can evaluate it too.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// MetricName is the design spec §9.3 visual accuracy metric.
const MetricName = "visual_classification_accuracy"

// visualCorpus is the subset of eval/corpus/visual.json this command consumes.
type visualCorpus struct {
	CorpusVersion string `json:"corpusVersion"`
	Scenario      struct {
		Assertion struct {
			Kind      string  `json:"kind"`
			Selector  string  `json:"selector"`
			Threshold float64 `json:"threshold"`
			Viewport  struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"viewport"`
			DeviceScaleFactor float64 `json:"deviceScaleFactor"`
		} `json:"assertion"`
		Baseline struct {
			EvidenceKind string `json:"evidenceKind"`
			Mime         string `json:"mime"`
			Approval     string `json:"approval"`
			ImageSize    struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"imageSize"`
		} `json:"baseline"`
		Evidence struct {
			ActualKind string `json:"actualKind"`
			DiffKind   string `json:"diffKind"`
			Mime       string `json:"mime"`
		} `json:"evidence"`
	} `json:"scenario"`
}

// visualTruth is eval/ground-truth/visual.json.
type visualTruth struct {
	CorpusVersion string  `json:"corpusVersion"`
	Metric        string  `json:"metric"`
	Threshold     float64 `json:"threshold"`
	Cases         []struct {
		Name                 string  `json:"name"`
		HasBaseline          bool    `json:"hasBaseline"`
		EnvironmentMatches   bool    `json:"environmentMatches"`
		DiffRatio            float64 `json:"diffRatio"`
		Threshold            float64 `json:"threshold"`
		ExpectedVerdict      string  `json:"expectedVerdict"`
		ExpectedFailureClass string  `json:"expectedFailureClass"`
		ExpectedReason       string  `json:"expectedReason"`
	} `json:"cases"`
}

// ScenarioEcho proves the corpus scenario was consumed and validated.
type ScenarioEcho struct {
	Selector          string  `json:"selector"`
	Threshold         float64 `json:"threshold"`
	ViewportWidth     int     `json:"viewportWidth"`
	ViewportHeight    int     `json:"viewportHeight"`
	DeviceScaleFactor float64 `json:"deviceScaleFactor"`
	BaselineKind      string  `json:"baselineKind"`
	BaselineMime      string  `json:"baselineMime"`
	ActualKind        string  `json:"actualKind"`
	DiffKind          string  `json:"diffKind"`
}

// CaseResult is one evaluated ground-truth case.
type CaseResult struct {
	Name                 string `json:"name"`
	Verdict              string `json:"verdict"`
	FailureClass         string `json:"failureClass"`
	Reason               string `json:"reason"`
	ExpectedVerdict      string `json:"expectedVerdict"`
	ExpectedFailureClass string `json:"expectedFailureClass"`
	ExpectedReason       string `json:"expectedReason"`
	Match                bool   `json:"match"`
}

// Result is the JSON document scripts/eval.ps1 consumes.
type Result struct {
	Metric    string       `json:"metric"`
	Threshold float64      `json:"threshold"`
	Accuracy  float64      `json:"accuracy"`
	Correct   int          `json:"correct"`
	Total     int          `json:"total"`
	Passed    bool         `json:"passed"`
	Scenario  ScenarioEcho `json:"scenario"`
	Cases     []CaseResult `json:"cases"`
}

func main() {
	corpusPath := flag.String("corpus", "eval/corpus/visual.json", "visual corpus JSON path")
	truthPath := flag.String("ground-truth", "eval/ground-truth/visual.json", "visual ground-truth JSON path")
	flag.Parse()

	result, err := evaluate(*corpusPath, *truthPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "evalvisual:", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "evalvisual: encode result:", err)
		os.Exit(1)
	}
}

// evaluate loads both eval artifacts, validates their freeze contract and
// replays every ground-truth case through domain.ClassifyVisual.
func evaluate(corpusPath, truthPath string) (Result, error) {
	var corpus visualCorpus
	if err := readJSON(corpusPath, &corpus); err != nil {
		return Result{}, fmt.Errorf("read visual corpus: %w", err)
	}
	var truth visualTruth
	if err := readJSON(truthPath, &truth); err != nil {
		return Result{}, fmt.Errorf("read visual ground truth: %w", err)
	}
	if strings.TrimSpace(corpus.CorpusVersion) == "" {
		return Result{}, errors.New("visual corpus has no corpusVersion")
	}
	if corpus.CorpusVersion != truth.CorpusVersion {
		return Result{}, fmt.Errorf("visual corpusVersion %q does not match ground truth %q",
			corpus.CorpusVersion, truth.CorpusVersion)
	}
	if truth.Metric != MetricName {
		return Result{}, fmt.Errorf("visual ground truth metric %q is not %q", truth.Metric, MetricName)
	}
	if truth.Threshold <= 0 || truth.Threshold > 1 {
		return Result{}, fmt.Errorf("visual ground truth threshold %v is outside (0,1]", truth.Threshold)
	}
	scenario, err := validateScenario(corpus)
	if err != nil {
		return Result{}, err
	}
	if len(truth.Cases) == 0 {
		return Result{}, errors.New("visual ground truth declares no cases")
	}

	result := Result{
		Metric:    MetricName,
		Threshold: truth.Threshold,
		Total:     len(truth.Cases),
		Scenario:  scenario,
		Cases:     make([]CaseResult, 0, len(truth.Cases)),
	}
	for _, testCase := range truth.Cases {
		if err := validateTruthCase(testCase.ExpectedVerdict, testCase.ExpectedFailureClass,
			testCase.ExpectedReason); err != nil {
			return Result{}, fmt.Errorf("ground-truth case %q: %w", testCase.Name, err)
		}
		verdict, class, reason := domain.ClassifyVisual(
			testCase.HasBaseline, testCase.EnvironmentMatches, testCase.DiffRatio, testCase.Threshold)
		evaluated := CaseResult{
			Name:                 testCase.Name,
			Verdict:              string(verdict),
			FailureClass:         string(class),
			Reason:               reason,
			ExpectedVerdict:      testCase.ExpectedVerdict,
			ExpectedFailureClass: testCase.ExpectedFailureClass,
			ExpectedReason:       testCase.ExpectedReason,
		}
		evaluated.Match = evaluated.Verdict == evaluated.ExpectedVerdict &&
			evaluated.FailureClass == evaluated.ExpectedFailureClass &&
			evaluated.Reason == evaluated.ExpectedReason
		if evaluated.Match {
			result.Correct++
		}
		result.Cases = append(result.Cases, evaluated)
	}
	result.Accuracy = float64(result.Correct) / float64(result.Total)
	// The tolerance absorbs binary floating error around the exact 1.00 gate.
	result.Passed = result.Accuracy+1e-9 >= truth.Threshold
	return result, nil
}

// validateScenario enforces the frozen visual scenario shape so a corpus edit
// cannot silently produce a meaningless metric.
func validateScenario(corpus visualCorpus) (ScenarioEcho, error) {
	assertion := corpus.Scenario.Assertion
	if !strings.EqualFold(strings.TrimSpace(assertion.Kind), "visual") {
		return ScenarioEcho{}, fmt.Errorf("visual scenario assertion kind %q is not visual", assertion.Kind)
	}
	if assertion.Threshold < 0 || assertion.Threshold > 0.1 {
		return ScenarioEcho{}, fmt.Errorf("visual scenario threshold %v is outside 0..0.1", assertion.Threshold)
	}
	if assertion.Viewport.Width <= 0 || assertion.Viewport.Width > 10000 ||
		assertion.Viewport.Height <= 0 || assertion.Viewport.Height > 10000 {
		return ScenarioEcho{}, fmt.Errorf("visual scenario viewport %dx%d is invalid",
			assertion.Viewport.Width, assertion.Viewport.Height)
	}
	if assertion.DeviceScaleFactor < 0.5 || assertion.DeviceScaleFactor > 4 {
		return ScenarioEcho{}, fmt.Errorf("visual scenario deviceScaleFactor %v is outside 0.5..4",
			assertion.DeviceScaleFactor)
	}
	baseline := corpus.Scenario.Baseline
	kind := strings.ToLower(strings.TrimSpace(baseline.EvidenceKind))
	if kind != "screenshot" && kind != "visual_baseline" {
		return ScenarioEcho{}, fmt.Errorf("visual scenario baseline evidenceKind %q is not screenshot or visual_baseline",
			baseline.EvidenceKind)
	}
	if strings.ToLower(strings.TrimSpace(baseline.Mime)) != "image/png" {
		return ScenarioEcho{}, fmt.Errorf("visual scenario baseline mime %q is not image/png", baseline.Mime)
	}
	if baseline.ImageSize.Width <= 0 || baseline.ImageSize.Height <= 0 {
		return ScenarioEcho{}, fmt.Errorf("visual scenario baseline imageSize %dx%d is invalid",
			baseline.ImageSize.Width, baseline.ImageSize.Height)
	}
	evidence := corpus.Scenario.Evidence
	if evidence.ActualKind != "visual_actual" || evidence.DiffKind != "visual_diff" ||
		strings.ToLower(strings.TrimSpace(evidence.Mime)) != "image/png" {
		return ScenarioEcho{}, fmt.Errorf("visual scenario evidence kinds %q/%q (%s) are invalid",
			evidence.ActualKind, evidence.DiffKind, evidence.Mime)
	}
	return ScenarioEcho{
		Selector:          assertion.Selector,
		Threshold:         assertion.Threshold,
		ViewportWidth:     assertion.Viewport.Width,
		ViewportHeight:    assertion.Viewport.Height,
		DeviceScaleFactor: assertion.DeviceScaleFactor,
		BaselineKind:      baseline.EvidenceKind,
		BaselineMime:      baseline.Mime,
		ActualKind:        evidence.ActualKind,
		DiffKind:          evidence.DiffKind,
	}, nil
}

// validateTruthCase enforces the §9.3 verdict/class matrix of one expected row.
func validateTruthCase(expectedVerdict, expectedFailureClass, expectedReason string) error {
	if strings.TrimSpace(expectedReason) == "" {
		return errors.New("expectedReason is empty")
	}
	switch strings.ToLower(strings.TrimSpace(expectedVerdict)) {
	case "passed":
		if expectedFailureClass != "" {
			return errors.New("a passed case must not carry a failure class")
		}
	case "failed":
		if expectedFailureClass != "application" {
			return errors.New("a failed case requires failure class application")
		}
	case "inconclusive":
		if expectedFailureClass != "case" && expectedFailureClass != "environment" {
			return errors.New("an inconclusive visual case requires failure class case or environment")
		}
	default:
		return fmt.Errorf("expected verdict %q is not a visual assertion verdict", expectedVerdict)
	}
	return nil
}

// readJSON decodes one eval artifact, rejecting trailing content.
func readJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON content")
		}
		return err
	}
	return nil
}
