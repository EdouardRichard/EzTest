package visual

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// AssertionStatus is the visual_assertion.status enum.
type AssertionStatus string

const (
	AssertionActive   AssertionStatus = "active"
	AssertionArchived AssertionStatus = "archived"
)

// Valid reports whether status is a persisted visual_assertion status.
func (s AssertionStatus) Valid() bool {
	return s == AssertionActive || s == AssertionArchived
}

// Assertion is one visual assertion configuration (visual_assertion row). It
// always targets exactly one design case_assertion with kind=visual and one
// version_sut binding; config_revision increments on every effective update.
type Assertion struct {
	ID                int64
	VersionID         int64
	CaseVersionID     int64
	CaseAssertionID   int64
	StepSeq           int
	AssertionSeq      int
	VersionSUTID      int64
	Selector          string
	Region            json.RawMessage
	Threshold         float64
	ViewportWidth     int
	ViewportHeight    int
	DeviceScaleFactor float64
	WaitPolicy        json.RawMessage
	ConfigRevision    int
	Status            AssertionStatus
	CreatedBy         *int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Viewport is the visual comparison viewport in CSS pixels.
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ConfigInput is the PUT /visual/assertions/{caseAssertionId} payload. Nil
// fields keep the current value on update and take the visual.* defaults on
// create.
type ConfigInput struct {
	VersionSUTID      *int64
	Selector          *string
	Region            json.RawMessage
	Threshold         *float64
	Viewport          *Viewport
	DeviceScaleFactor *float64
	WaitPolicy        json.RawMessage
}

// ConfigDefaults carries the visual.default_* settings resolved by the
// application service.
type ConfigDefaults struct {
	Threshold         float64
	ViewportWidth     int
	ViewportHeight    int
	DeviceScaleFactor float64
}

// Apply validates the input against the current assertion and returns the
// normalized next state plus whether anything changed. Identical values are not
// a change: the service must not increment config_revision or supersede the
// approved baseline for them (spec §7.1).
func (a Assertion) Apply(in ConfigInput, defaults ConfigDefaults, existing bool) (Assertion, bool, error) {
	target := a
	if !existing {
		target.Status = AssertionActive
		target.Region = json.RawMessage(`{}`)
		target.WaitPolicy = json.RawMessage(`{}`)
		target.Threshold = defaults.Threshold
		target.ViewportWidth = defaults.ViewportWidth
		target.ViewportHeight = defaults.ViewportHeight
		target.DeviceScaleFactor = defaults.DeviceScaleFactor
	}
	if in.VersionSUTID != nil {
		if *in.VersionSUTID <= 0 {
			return a, false, invalidConfig("version_sut_id", "must be a positive ID")
		}
		target.VersionSUTID = *in.VersionSUTID
	}
	if in.Selector != nil {
		target.Selector = strings.TrimSpace(*in.Selector)
	}
	if len(bytes.TrimSpace(in.Region)) > 0 {
		target.Region = in.Region
	}
	if in.Threshold != nil {
		target.Threshold = *in.Threshold
	}
	if in.Viewport != nil {
		target.ViewportWidth = in.Viewport.Width
		target.ViewportHeight = in.Viewport.Height
	}
	if in.DeviceScaleFactor != nil {
		target.DeviceScaleFactor = *in.DeviceScaleFactor
	}
	if len(bytes.TrimSpace(in.WaitPolicy)) > 0 {
		target.WaitPolicy = in.WaitPolicy
	}

	region, err := NormalizeRegion(target.Region)
	if err != nil {
		return a, false, err
	}
	target.Region = region
	waitPolicy, err := NormalizeWaitPolicy(target.WaitPolicy)
	if err != nil {
		return a, false, err
	}
	target.WaitPolicy = waitPolicy
	if err := target.Validate(); err != nil {
		return a, false, err
	}
	if existing && sameConfig(a, target) {
		return a, false, nil
	}
	return target, true, nil
}

// Validate checks the persisted bounds of one visual assertion.
func (a Assertion) Validate() error {
	if a.VersionSUTID <= 0 {
		return invalidConfig("version_sut_id", "must be a positive ID")
	}
	if a.StepSeq <= 0 || a.AssertionSeq <= 0 {
		return invalidConfig("step_seq", "step_seq and assertion_seq must be positive")
	}
	if err := ValidateSelector(a.Selector); err != nil {
		return err
	}
	if _, err := NormalizeRegion(a.Region); err != nil {
		return err
	}
	if _, err := NormalizeWaitPolicy(a.WaitPolicy); err != nil {
		return err
	}
	if err := ValidateThreshold(a.Threshold); err != nil {
		return err
	}
	if err := ValidateViewport(a.ViewportWidth, a.ViewportHeight); err != nil {
		return err
	}
	if err := ValidateDeviceScaleFactor(a.DeviceScaleFactor); err != nil {
		return err
	}
	if a.Status != "" && !a.Status.Valid() {
		return invalidConfig("status", "unsupported visual assertion status")
	}
	return nil
}

// ValidateSelector bounds the CSS selector by rune count.
func ValidateSelector(selector string) error {
	if len([]rune(selector)) > MaxSelectorRunes {
		return invalidConfig("selector", fmt.Sprintf("must be at most %d characters", MaxSelectorRunes))
	}
	return nil
}

// ValidateThreshold bounds the pixel diff ratio threshold.
func ValidateThreshold(threshold float64) error {
	if math.IsNaN(threshold) || threshold < MinThreshold || threshold > MaxThreshold {
		return invalidConfig("threshold", fmt.Sprintf("must be between %v and %v", MinThreshold, MaxThreshold))
	}
	return nil
}

// ValidateViewport bounds the CSS-pixel viewport.
func ValidateViewport(width, height int) error {
	if width < MinViewportPx || width > MaxViewportPx || height < MinViewportPx || height > MaxViewportPx {
		return invalidConfig("viewport", fmt.Sprintf("width and height must be between %d and %d", MinViewportPx, MaxViewportPx))
	}
	return nil
}

// ValidateDeviceScaleFactor bounds the device-pixel ratio.
func ValidateDeviceScaleFactor(dpr float64) error {
	if math.IsNaN(dpr) || dpr < MinDeviceScaleFactor || dpr > MaxDeviceScaleFactor {
		return invalidConfig("device_scale_factor", fmt.Sprintf("must be between %v and %v", MinDeviceScaleFactor, MaxDeviceScaleFactor))
	}
	return nil
}

// NormalizeRegion canonicalizes the normalized {x,y,width,height} crop. An
// absent/empty region stays `{}` (whole image); coordinates are normalized to
// the 0..1 image and must stay inside it.
func NormalizeRegion(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return nil, invalidConfig("region", "must be a JSON object")
	}
	if len(fields) == 0 {
		return json.RawMessage(`{}`), nil
	}
	for key := range fields {
		switch key {
		case "x", "y", "width", "height":
		default:
			return nil, invalidConfig("region", "unknown region field "+key)
		}
	}
	read := func(key string) (float64, error) {
		value, ok := fields[key]
		if !ok {
			return 0, nil
		}
		var numeric float64
		if err := json.Unmarshal(value, &numeric); err != nil || math.IsNaN(numeric) || math.IsInf(numeric, 0) {
			return 0, invalidConfig("region."+key, "must be a finite number")
		}
		return numeric, nil
	}
	x, err := read("x")
	if err != nil {
		return nil, err
	}
	y, err := read("y")
	if err != nil {
		return nil, err
	}
	width, err := read("width")
	if err != nil {
		return nil, err
	}
	height, err := read("height")
	if err != nil {
		return nil, err
	}
	if x == 0 && y == 0 && width == 0 && height == 0 {
		return json.RawMessage(`{}`), nil
	}
	const epsilon = 1e-9
	if x < 0 || y < 0 || width <= 0 || height <= 0 {
		return nil, invalidConfig("region", "x/y must be non-negative and width/height positive")
	}
	if x+width > 1+epsilon || y+height > 1+epsilon {
		return nil, invalidConfig("region", "must stay inside the normalized 0..1 image")
	}
	encoded, err := json.Marshal(map[string]float64{"x": x, "y": y, "width": width, "height": height})
	if err != nil {
		return nil, invalidConfig("region", "could not encode the region")
	}
	return encoded, nil
}

// NormalizeWaitPolicy canonicalizes the wait policy JSON object. The executor
// applies its own defaults for missing keys; the API only stores the override.
func NormalizeWaitPolicy(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, invalidConfig("wait_policy", "must be a JSON object")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, invalidConfig("wait_policy", "must be a JSON object")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, invalidConfig("wait_policy", "could not encode the wait policy")
	}
	return encoded, nil
}

// FingerprintInput is the canonical environment fingerprint input of spec §7.3.
type FingerprintInput struct {
	ViewportWidth     int
	ViewportHeight    int
	DeviceScaleFactor float64
	Selector          string
	Region            json.RawMessage
	ConfigRevision    int
	BrowserName       string
	BrowserVersion    string
}

type fingerprintPayload struct {
	ViewportWidth     int     `json:"viewport_width"`
	ViewportHeight    int     `json:"viewport_height"`
	DeviceScaleFactor float64 `json:"device_scale_factor"`
	Selector          string  `json:"selector"`
	Region            any     `json:"region"`
	ConfigRevision    int     `json:"config_revision"`
	BrowserName       string  `json:"browser_name"`
	BrowserVersion    string  `json:"browser_version"`
}

// EnvironmentFingerprint computes sha256 over the canonical environment JSON:
// viewport/DPR + selector/region + config revision + browser identity. Region
// key order and whitespace never change the hash.
func EnvironmentFingerprint(in FingerprintInput) (string, error) {
	if in.ViewportWidth <= 0 || in.ViewportHeight <= 0 {
		return "", invalidConfig("viewport", "viewport must be positive")
	}
	if err := ValidateDeviceScaleFactor(in.DeviceScaleFactor); err != nil {
		return "", err
	}
	region, err := NormalizeRegion(in.Region)
	if err != nil {
		return "", err
	}
	var regionValue any = map[string]any{}
	if string(region) != "{}" {
		if err := json.Unmarshal(region, &regionValue); err != nil {
			return "", invalidConfig("region", "could not encode the region")
		}
	}
	encoded, err := json.Marshal(fingerprintPayload{
		ViewportWidth:     in.ViewportWidth,
		ViewportHeight:    in.ViewportHeight,
		DeviceScaleFactor: in.DeviceScaleFactor,
		Selector:          in.Selector,
		Region:            regionValue,
		ConfigRevision:    in.ConfigRevision,
		BrowserName:       in.BrowserName,
		BrowserVersion:    in.BrowserVersion,
	})
	if err != nil {
		return "", invalidConfig("fingerprint", "could not encode the environment fingerprint")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// AssertionFingerprint fingerprints the configured environment of one
// assertion. Baseline approval records it without a browser identity (the
// browser name/version are only known at comparison time).
func AssertionFingerprint(a Assertion, browserName, browserVersion string) (string, error) {
	return EnvironmentFingerprint(FingerprintInput{
		ViewportWidth:     a.ViewportWidth,
		ViewportHeight:    a.ViewportHeight,
		DeviceScaleFactor: a.DeviceScaleFactor,
		Selector:          a.Selector,
		Region:            a.Region,
		ConfigRevision:    a.ConfigRevision,
		BrowserName:       browserName,
		BrowserVersion:    browserVersion,
	})
}

func sameConfig(left, right Assertion) bool {
	if left.VersionSUTID != right.VersionSUTID ||
		left.Selector != right.Selector ||
		left.ViewportWidth != right.ViewportWidth ||
		left.ViewportHeight != right.ViewportHeight ||
		!floatEqual(left.Threshold, right.Threshold) ||
		!floatEqual(left.DeviceScaleFactor, right.DeviceScaleFactor) {
		return false
	}
	leftRegion, leftErr := NormalizeRegion(left.Region)
	rightRegion, rightErr := NormalizeRegion(right.Region)
	if leftErr != nil || rightErr != nil || !bytes.Equal(leftRegion, rightRegion) {
		return false
	}
	leftWait, leftErr := NormalizeWaitPolicy(left.WaitPolicy)
	rightWait, rightErr := NormalizeWaitPolicy(right.WaitPolicy)
	if leftErr != nil || rightErr != nil || !bytes.Equal(leftWait, rightWait) {
		return false
	}
	return true
}

func floatEqual(left, right float64) bool {
	return math.Abs(left-right) <= 1e-9
}

func invalidConfig(field, message string) error {
	if field == "" {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, message)
	}
	return fmt.Errorf("%w: %s: %s", ErrInvalidConfig, field, message)
}
