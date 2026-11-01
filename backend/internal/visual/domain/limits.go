package visual

// Visual configuration limits (spec §3.5/§4.5/§7.1). The DB CHECK constraints
// mirror these bounds; the application validates before writing so the client
// gets a 422 instead of a driver error.
const (
	// DefaultThreshold is visual.default_threshold (spec §4.5).
	DefaultThreshold = 0.005
	MinThreshold     = 0
	MaxThreshold     = 0.1

	DefaultViewportWidth  = 1280
	DefaultViewportHeight = 720
	MinViewportPx         = 1
	MaxViewportPx         = 10000

	DefaultDeviceScaleFactor = 1.0
	MinDeviceScaleFactor     = 0.5
	MaxDeviceScaleFactor     = 4.0

	// MaxSelectorRunes bounds the CSS selector (spec §4.5: ≤500 runes).
	MaxSelectorRunes = 500
	// MaxMismatchBoxes bounds one visual_diff.mismatch_boxes array.
	MaxMismatchBoxes = 50
	// MaxReasonChars bounds visual_diff.reason / assertion_result.reason.
	MaxReasonChars = 2000

	// Pagination bounds of the visual list endpoints.
	DefaultListLimit = 20
	MaxListLimit     = 100
)
