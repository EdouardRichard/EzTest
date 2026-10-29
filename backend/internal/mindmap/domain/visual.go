package domain

import (
	"fmt"
	"math"
	"time"
)

// Visual assertion bounds and defaults (design spec §9.3). Threshold is the
// fraction of differing pixels allowed; 0.005 means half a percent.
const (
	VisualThresholdMin     = 0.0
	VisualThresholdMax     = 0.1
	VisualThresholdDefault = 0.005

	VisualViewportDefaultWidth     = 1280
	VisualViewportDefaultHeight    = 720
	VisualDeviceScaleFactorDefault = 1.0
	VisualDeviceScaleFactorMin     = 0.5
	VisualDeviceScaleFactorMax     = 4.0
	VisualViewportMaxPixels        = 10000
	VisualMaxSelectorRunes         = 500
)

// VisualRegion is a normalized crop of one screenshot: all values are in the
// 0..1 range and (X+Width, Y+Height) must stay inside the unit square. A nil
// region means the whole page/viewport.
type VisualRegion struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Validate checks the normalized region bounds.
func (r VisualRegion) Validate() error {
	if !finite(r.X) || !finite(r.Y) || !finite(r.Width) || !finite(r.Height) {
		return NewInvalid("region", "invalid_visual_region", "visual region values must be finite numbers")
	}
	if r.X < 0 || r.Y < 0 || r.Width <= 0 || r.Height <= 0 {
		return NewInvalid("region", "invalid_visual_region",
			"visual region x/y must be non-negative and width/height positive")
	}
	if r.X+r.Width > 1+1e-9 || r.Y+r.Height > 1+1e-9 {
		return NewInvalid("region", "invalid_visual_region",
			"visual region must stay inside the normalized 0..1 viewport")
	}
	return nil
}

// VisualAssertion is the comparison configuration of one kind=visual assertion
// (design spec §9.3). BaselineEvidenceID is nil until an administrator approves
// a baseline; approved_by/approved_at are set atomically with it.
type VisualAssertion struct {
	AssertionID        int64
	Selector           string
	Region             *VisualRegion
	Threshold          float64
	BaselineEvidenceID *int64
	ViewportWidth      int
	ViewportHeight     int
	DeviceScaleFactor  float64
	ApprovedBy         *int64
	ApprovedAt         *time.Time
	Revision           int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// HasBaseline reports whether an approved baseline exists.
func (v VisualAssertion) HasBaseline() bool {
	return v.BaselineEvidenceID != nil && *v.BaselineEvidenceID > 0
}

// ExpectedImageSize returns the device-pixel size a baseline screenshot of this
// configuration must have: the viewport scaled by the device scale factor.
func (v VisualAssertion) ExpectedImageSize() (width, height int) {
	width = int(math.Round(float64(v.ViewportWidth) * v.DeviceScaleFactor))
	height = int(math.Round(float64(v.ViewportHeight) * v.DeviceScaleFactor))
	return width, height
}

// VisualAssertionParams is the writable comparison configuration of one visual
// assertion. The zero region means "no crop".
type VisualAssertionParams struct {
	Selector          string
	Region            *VisualRegion
	Threshold         float64
	ViewportWidth     int
	ViewportHeight    int
	DeviceScaleFactor float64
}

// Validate enforces the spec §9.3 bounds: selector at most 500 runes, threshold
// in 0..0.1, positive viewport and a 0.5..4 device scale factor.
func (p VisualAssertionParams) Validate() error {
	if len([]rune(p.Selector)) > VisualMaxSelectorRunes {
		return NewInvalid("selector", "invalid_visual_selector",
			fmt.Sprintf("visual selector must be at most %d characters", VisualMaxSelectorRunes))
	}
	if p.Region != nil {
		if err := p.Region.Validate(); err != nil {
			return err
		}
	}
	if !finite(p.Threshold) || p.Threshold < VisualThresholdMin || p.Threshold > VisualThresholdMax {
		return NewInvalid("threshold", "invalid_visual_threshold",
			fmt.Sprintf("visual threshold must be between %g and %g", VisualThresholdMin, VisualThresholdMax))
	}
	if p.ViewportWidth <= 0 || p.ViewportWidth > VisualViewportMaxPixels ||
		p.ViewportHeight <= 0 || p.ViewportHeight > VisualViewportMaxPixels {
		return NewInvalid("viewport", "invalid_visual_viewport",
			"visual viewport width and height must be positive and at most 10000 pixels")
	}
	if !finite(p.DeviceScaleFactor) ||
		p.DeviceScaleFactor < VisualDeviceScaleFactorMin ||
		p.DeviceScaleFactor > VisualDeviceScaleFactorMax {
		return NewInvalid("deviceScaleFactor", "invalid_visual_device_scale_factor",
			"visual device scale factor must be between 0.5 and 4")
	}
	return nil
}

// finite reports whether value is neither NaN nor an infinity.
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
