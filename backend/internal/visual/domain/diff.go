package visual

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// Diff is one persisted visual comparison (visual_diff row). It is append-only:
// bytes live in the execution content ledger and only evidence IDs/ratios are
// stored here.
type Diff struct {
	ID                int64
	VisualAssertionID int64
	CaseRunID         int64
	AssertionResultID int64
	BaselineID        *int64
	ActualEvidenceID  int64
	DiffEvidenceID    *int64
	DiffRatio         float64
	Threshold         float64
	Classification    Classification
	Reason            string
	EnvironmentMatch  bool
	Width             int
	Height            int
	MismatchBoxes     json.RawMessage
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// MismatchBox is one coarse differing rectangle of the diff image.
type MismatchBox struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// DiffFilter narrows one visual diff page. Limit and BeforeID implement the
// shared descending-ID cursor convention.
type DiffFilter struct {
	CaseRunID      *int64
	CaseVersionID  *int64
	Classification Classification
	BeforeID       *int64
	Limit          int
}

// Validate checks the list filter bounds.
func (f DiffFilter) Validate() error {
	if f.Classification != "" && !f.Classification.Valid() {
		return fmt.Errorf("%w: classification: unsupported classification", ErrInvalidDiff)
	}
	if f.Limit < 0 || f.Limit > MaxListLimit {
		return fmt.Errorf("%w: limit: must be between 0 and %d", ErrInvalidDiff, MaxListLimit)
	}
	if f.BeforeID != nil && *f.BeforeID < 0 {
		return fmt.Errorf("%w: cursor: must not be negative", ErrInvalidDiff)
	}
	return nil
}

// NormalizedLimit clamps the page size to the contract bounds.
func (f DiffFilter) NormalizedLimit() int {
	if f.Limit <= 0 {
		return DefaultListLimit
	}
	if f.Limit > MaxListLimit {
		return MaxListLimit
	}
	return f.Limit
}

// RecordDiffInput is the input of DiffService.Record (written by the execution
// visual assert integration).
type RecordDiffInput struct {
	VersionID         int64
	VisualAssertionID int64
	CaseRunID         int64
	AssertionResultID int64
	BaselineID        *int64
	ActualEvidenceID  int64
	DiffEvidenceID    *int64
	DiffRatio         float64
	Threshold         float64
	Classification    Classification
	Reason            string
	EnvironmentMatch  bool
	Width             int
	Height            int
	MismatchBoxes     json.RawMessage
}

// Validate checks one comparison record before it is persisted. A missing
// baseline is inconclusive; an environment mismatch can never be failed, and a
// diff image is only allowed when a real comparison ran.
func (in RecordDiffInput) Validate() error {
	if in.VersionID <= 0 {
		return fmt.Errorf("%w: version_id: must be a positive ID", ErrInvalidDiff)
	}
	if in.VisualAssertionID <= 0 || in.CaseRunID <= 0 || in.AssertionResultID <= 0 || in.ActualEvidenceID <= 0 {
		return fmt.Errorf("%w: ids: visual_assertion_id, case_run_id, assertion_result_id and actual_evidence_id are required", ErrInvalidDiff)
	}
	if in.BaselineID != nil && *in.BaselineID <= 0 {
		return fmt.Errorf("%w: baseline_id: must be a positive ID", ErrInvalidDiff)
	}
	if in.DiffEvidenceID != nil && *in.DiffEvidenceID <= 0 {
		return fmt.Errorf("%w: diff_evidence_id: must be a positive ID", ErrInvalidDiff)
	}
	if math.IsNaN(in.DiffRatio) || in.DiffRatio < 0 || in.DiffRatio > 1 {
		return fmt.Errorf("%w: diff_ratio: must be between 0 and 1", ErrInvalidDiff)
	}
	if err := ValidateThreshold(in.Threshold); err != nil {
		return err
	}
	if !in.Classification.Valid() {
		return fmt.Errorf("%w: classification: unsupported classification", ErrInvalidDiff)
	}
	if !in.EnvironmentMatch && in.Classification == ClassificationFailed {
		return fmt.Errorf("%w: classification: an environment mismatch cannot fail the application", ErrInvalidDiff)
	}
	if len([]rune(in.Reason)) > MaxReasonChars {
		return fmt.Errorf("%w: reason: must be at most %d characters", ErrInvalidDiff, MaxReasonChars)
	}
	if _, err := ValidateMismatchBoxes(in.MismatchBoxes); err != nil {
		return err
	}
	return nil
}

// ValidateMismatchBoxes parses and bounds the visual_diff.mismatch_boxes JSONB
// array (≤50 boxes, non-negative coordinates, positive size).
func ValidateMismatchBoxes(raw json.RawMessage) ([]MismatchBox, error) {
	trimmed := []byte(raw)
	if len(trimmed) == 0 {
		return []MismatchBox{}, nil
	}
	var boxes []MismatchBox
	if err := json.Unmarshal(trimmed, &boxes); err != nil {
		return nil, fmt.Errorf("%w: mismatch_boxes: must be a JSON array", ErrInvalidDiff)
	}
	if len(boxes) > MaxMismatchBoxes {
		return nil, fmt.Errorf("%w: mismatch_boxes: at most %d boxes are allowed", ErrInvalidDiff, MaxMismatchBoxes)
	}
	for _, box := range boxes {
		if box.X < 0 || box.Y < 0 || box.Width <= 0 || box.Height <= 0 {
			return nil, fmt.Errorf("%w: mismatch_boxes: boxes need non-negative x/y and positive width/height", ErrInvalidDiff)
		}
	}
	return boxes, nil
}

// NormalizeMismatchBoxes returns the canonical JSON of the box array.
func NormalizeMismatchBoxes(raw json.RawMessage) (json.RawMessage, error) {
	boxes, err := ValidateMismatchBoxes(raw)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(boxes)
	if err != nil {
		return nil, fmt.Errorf("%w: mismatch_boxes: could not encode boxes", ErrInvalidDiff)
	}
	return encoded, nil
}

// Percentile returns the linear-interpolated percentile of values (0..1).
func Percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if p <= 0 {
		return minValue(values)
	}
	if p >= 1 {
		return maxValue(values)
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	position := float64(len(sorted)-1) * p
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}

// Ratio returns numerator/denominator, or nil when the denominator is zero so
// the API renders a nullable ratio instead of a fabricated 0.
func Ratio(numerator, denominator int) *float64 {
	if denominator <= 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

func minValue(values []float64) float64 {
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func maxValue(values []float64) float64 {
	maximum := values[0]
	for _, value := range values[1:] {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}
