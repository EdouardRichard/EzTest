package visual_test

import (
	"encoding/json"
	"math"
	"testing"

	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

func TestClassifyFixedOrder(t *testing.T) {
	cases := []struct {
		name        string
		envMatch    bool
		hasBaseline bool
		diffRatio   float64
		threshold   float64
		want        visual.Classification
	}{
		{"no baseline wins over any diff", true, false, 0.9, 0.01, visual.ClassificationInconclusive},
		{"environment mismatch wins over diff", false, true, 0.9, 0.01, visual.ClassificationInconclusive},
		{"diff above threshold fails", true, true, 0.02, 0.01, visual.ClassificationFailed},
		{"diff equal to threshold passes", true, true, 0.01, 0.01, visual.ClassificationPassed},
		{"diff below threshold passes", true, true, 0, 0.01, visual.ClassificationPassed},
		{"zero threshold requires identical pixels", true, true, 0.000001, 0, visual.ClassificationFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := visual.Classify(tc.envMatch, tc.hasBaseline, tc.diffRatio, tc.threshold)
			if got != tc.want {
				t.Fatalf("Classify(%v, %v, %v, %v) = %s, want %s",
					tc.envMatch, tc.hasBaseline, tc.diffRatio, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestClassificationValidAndReasons(t *testing.T) {
	for _, class := range []visual.Classification{
		visual.ClassificationPassed, visual.ClassificationFailed,
		visual.ClassificationInconclusive, visual.ClassificationBlocked,
	} {
		if !class.Valid() {
			t.Fatalf("%s must be valid", class)
		}
	}
	if visual.Classification("bogus").Valid() {
		t.Fatal("bogus classification must be invalid")
	}
	if got := visual.ClassificationReason(visual.ClassificationPassed); got != visual.ReasonVisualMatch {
		t.Fatalf("passed reason = %q", got)
	}
	if got := visual.ClassificationReason(visual.ClassificationFailed); got != visual.ReasonVisualMismatch {
		t.Fatalf("failed reason = %q", got)
	}
	if got := visual.InconclusiveReason(false, true); got != visual.ReasonMissingBaseline {
		t.Fatalf("missing baseline reason = %q", got)
	}
	if got := visual.InconclusiveReason(true, false); got != visual.ReasonEnvironmentMismatch {
		t.Fatalf("environment reason = %q", got)
	}
}

func TestConfigInputApplyDefaults(t *testing.T) {
	defaults := visual.ConfigDefaults{
		Threshold:         visual.DefaultThreshold,
		ViewportWidth:     visual.DefaultViewportWidth,
		ViewportHeight:    visual.DefaultViewportHeight,
		DeviceScaleFactor: visual.DefaultDeviceScaleFactor,
	}
	assertion := visual.Assertion{VersionID: 1, CaseVersionID: 2, CaseAssertionID: 3, StepSeq: 1, AssertionSeq: 1, VersionSUTID: 4}
	updated, changed, err := assertion.Apply(visual.ConfigInput{}, defaults, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !changed {
		t.Fatal("creating the first config must count as a change")
	}
	if updated.Threshold != visual.DefaultThreshold {
		t.Fatalf("threshold = %v", updated.Threshold)
	}
	if updated.ViewportWidth != 1280 || updated.ViewportHeight != 720 {
		t.Fatalf("viewport = %dx%d", updated.ViewportWidth, updated.ViewportHeight)
	}
	if updated.DeviceScaleFactor != 1 {
		t.Fatalf("dpr = %v", updated.DeviceScaleFactor)
	}
	if string(updated.Region) != "{}" {
		t.Fatalf("region = %s", updated.Region)
	}
	if string(updated.WaitPolicy) != "{}" {
		t.Fatalf("wait policy = %s", updated.WaitPolicy)
	}
	if updated.Status != visual.AssertionActive {
		t.Fatalf("status = %q", updated.Status)
	}
}

func TestConfigInputApplyDetectsUnchangedValues(t *testing.T) {
	defaults := visual.ConfigDefaults{
		Threshold: 0.005, ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1,
	}
	assertion := visual.Assertion{
		VersionID: 1, CaseVersionID: 2, CaseAssertionID: 3, StepSeq: 1, AssertionSeq: 1, VersionSUTID: 4,
		Selector: "#chart", Region: json.RawMessage(`{"x":0.1,"y":0.2,"width":0.5,"height":0.3}`),
		Threshold: 0.01, ViewportWidth: 800, ViewportHeight: 600, DeviceScaleFactor: 2,
		WaitPolicy: json.RawMessage(`{"network_idle":false}`), ConfigRevision: 3, Status: visual.AssertionActive,
	}
	selector := " #chart "
	region := json.RawMessage(`{"height":0.3,"width":0.5,"x":0.1,"y":0.2}`)
	threshold := 0.01
	viewport := &visual.Viewport{Width: 800, Height: 600}
	dpr := 2.0
	wait := json.RawMessage(`{"network_idle":false}`)
	updated, changed, err := assertion.Apply(visual.ConfigInput{
		Selector: &selector, Region: region, Threshold: &threshold,
		Viewport: viewport, DeviceScaleFactor: &dpr, WaitPolicy: wait,
	}, defaults, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if changed {
		t.Fatalf("identical normalized config must not change: %+v", updated)
	}
	if updated.ConfigRevision != 3 {
		t.Fatalf("revision = %d", updated.ConfigRevision)
	}

	newThreshold := 0.02
	_, changed, err = assertion.Apply(visual.ConfigInput{Threshold: &newThreshold}, defaults, true)
	if err != nil {
		t.Fatalf("Apply threshold: %v", err)
	}
	if !changed {
		t.Fatal("a changed threshold must count as a change")
	}
}

func TestConfigValidationRejectsInvalidValues(t *testing.T) {
	defaults := visual.ConfigDefaults{Threshold: 0.005, ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1}
	base := visual.Assertion{VersionID: 1, CaseVersionID: 2, CaseAssertionID: 3, StepSeq: 1, AssertionSeq: 1, VersionSUTID: 4}
	cases := []struct {
		name string
		in   visual.ConfigInput
	}{
		{"selector too long", visual.ConfigInput{Selector: strPtr(repeat("x", 501))}},
		{"region not object", visual.ConfigInput{Region: json.RawMessage(`[]`)}},
		{"region outside", visual.ConfigInput{Region: json.RawMessage(`{"x":0.8,"y":0,"width":0.5,"height":0.5}`)}},
		{"region negative", visual.ConfigInput{Region: json.RawMessage(`{"x":-0.1,"y":0,"width":0.5,"height":0.5}`)}},
		{"threshold high", visual.ConfigInput{Threshold: ptr(0.11)}},
		{"threshold low", visual.ConfigInput{Threshold: ptr(-0.01)}},
		{"viewport width zero", visual.ConfigInput{Viewport: &visual.Viewport{Width: 0, Height: 720}}},
		{"viewport too tall", visual.ConfigInput{Viewport: &visual.Viewport{Width: 1280, Height: 10001}}},
		{"dpr low", visual.ConfigInput{DeviceScaleFactor: ptr(0.4)}},
		{"dpr high", visual.ConfigInput{DeviceScaleFactor: ptr(4.1)}},
		{"wait policy not object", visual.ConfigInput{WaitPolicy: json.RawMessage(`"fast"`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := base.Apply(tc.in, defaults, false); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestEnvironmentFingerprintIsCanonical(t *testing.T) {
	input := visual.FingerprintInput{
		ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1,
		Selector: "#chart", Region: json.RawMessage(`{"x":0.1,"y":0,"width":0.5,"height":0.5}`),
		ConfigRevision: 2, BrowserName: "chromium", BrowserVersion: "120.0.1",
	}
	first, err := visual.EnvironmentFingerprint(input)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if len(first) != 64 || !isHex(first) {
		t.Fatalf("fingerprint = %q", first)
	}
	// Region key order and irrelevant whitespace must not change the hash.
	reordered := input
	reordered.Region = json.RawMessage(`{"height":0.5, "width":0.5, "x":0.1, "y":0}`)
	second, err := visual.EnvironmentFingerprint(reordered)
	if err != nil || second != first {
		t.Fatalf("region key order changed the fingerprint: %q vs %q err=%v", first, second, err)
	}
	revisionChanged := input
	revisionChanged.ConfigRevision = 3
	if got, _ := visual.EnvironmentFingerprint(revisionChanged); got == first {
		t.Fatal("config revision must change the fingerprint")
	}
	browserChanged := input
	browserChanged.BrowserVersion = "121.0.0"
	if got, _ := visual.EnvironmentFingerprint(browserChanged); got == first {
		t.Fatal("browser version must change the fingerprint")
	}
}

func TestAssertionFingerprintMatchesConfig(t *testing.T) {
	assertion := visual.Assertion{
		VersionID: 1, CaseVersionID: 2, CaseAssertionID: 3, StepSeq: 4, AssertionSeq: 5, VersionSUTID: 6,
		Selector: "#chart", Region: json.RawMessage(`{}`), Threshold: 0.01,
		ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1, ConfigRevision: 7,
		Status: visual.AssertionActive,
	}
	got, err := visual.AssertionFingerprint(assertion, "", "")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	want, err := visual.EnvironmentFingerprint(visual.FingerprintInput{
		ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1,
		Selector: "#chart", Region: assertion.Region, ConfigRevision: 7,
	})
	if err != nil {
		t.Fatalf("want fingerprint: %v", err)
	}
	if got != want {
		t.Fatalf("assertion fingerprint = %q, want %q", got, want)
	}
}

func TestValidateMismatchBoxes(t *testing.T) {
	boxes, err := visual.ValidateMismatchBoxes(json.RawMessage(`[{"x":0,"y":0,"width":32,"height":32}]`))
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes=%v err=%v", boxes, err)
	}
	if _, err := visual.ValidateMismatchBoxes(json.RawMessage(`{"x":0}`)); err == nil {
		t.Fatal("an object is not a mismatch box array")
	}
	tooMany := make([]visual.MismatchBox, visual.MaxMismatchBoxes+1)
	raw, _ := json.Marshal(tooMany)
	if _, err := visual.ValidateMismatchBoxes(raw); err == nil {
		t.Fatal("more than 50 boxes must be rejected")
	}
	if _, err := visual.ValidateMismatchBoxes(json.RawMessage(`[{"x":-1,"y":0,"width":32,"height":32}]`)); err == nil {
		t.Fatal("negative coordinates must be rejected")
	}
}

func TestPercentile(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := visual.Percentile(values, 0.5); math.Abs(got-5.5) > 1e-9 {
		t.Fatalf("p50 = %v", got)
	}
	if got := visual.Percentile(values, 0.95); math.Abs(got-9.55) > 1e-9 {
		t.Fatalf("p95 = %v", got)
	}
	if got := visual.Percentile(nil, 0.5); got != 0 {
		t.Fatalf("empty p50 = %v", got)
	}
	if got := visual.Ratio(0, 0); got != nil {
		t.Fatalf("zero denominator ratio = %v, want nil", *got)
	}
	if got := visual.Ratio(1, 4); got == nil || *got != 0.25 {
		t.Fatalf("ratio = %v", got)
	}
}

func TestClassifyBlockedIsOnlyExplicit(t *testing.T) {
	// Classify never returns blocked: blocked is set by the execution error path.
	if got := visual.Classify(false, false, 0, 0.01); got != visual.ClassificationInconclusive {
		t.Fatalf("classify = %s", got)
	}
}

func strPtr(value string) *string { return &value }
func ptr(value float64) *float64  { return &value }

func repeat(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}

func isHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
