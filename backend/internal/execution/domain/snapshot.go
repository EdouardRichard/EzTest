package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// CaseSnapshot is the immutable per-case execution snapshot captured when a
// batch is created. It is stored as case_run.case_snapshot JSONB and must never
// be rebuilt from the mindmap after the batch exists.
type CaseSnapshot struct {
	Node       CaseSnapshotNode        `json:"node"`
	Steps      []CaseSnapshotStep      `json:"steps"`
	Assertions []CaseSnapshotAssertion `json:"assertions"`
	Sources    []CaseSnapshotSource    `json:"sources"`
	GraphHash  string                  `json:"graph_hash"`
	CapturedAt time.Time               `json:"captured_at"`
}

// CaseSnapshotNode is the case node projection inside a snapshot. Origin is the
// provenance of the case in this graph: a frozen baseline case or an AI
// supplement (spec §5.3). A P6 historical snapshot without the field reads as
// baseline.
type CaseSnapshotNode struct {
	ID            int64          `json:"id,string"`
	Origin        CaseSource     `json:"origin"`
	Title         string         `json:"title"`
	Priority      string         `json:"priority"`
	Preconditions string         `json:"preconditions"`
	TestData      map[string]any `json:"test_data"`
}

// EffectiveOrigin returns the case origin, treating a missing value as baseline
// so P6 historical snapshots stay readable (spec §4.2.6).
func (n CaseSnapshotNode) EffectiveOrigin() CaseSource {
	return ParseCaseSource(string(n.Origin))
}

// CaseSnapshotStep is the step projection inside a snapshot.
type CaseSnapshotStep struct {
	Seq      int            `json:"seq"`
	Action   string         `json:"action"`
	Expected string         `json:"expected"`
	Data     map[string]any `json:"data"`
}

// CaseSnapshotVisual is the frozen visual comparison configuration of a
// kind=visual assertion (design spec §9.3). A nil baseline_evidence_id makes the
// assertion inconclusive(case, missing_visual_baseline) at execution time.
type CaseSnapshotVisual struct {
	Selector           string          `json:"selector"`
	Region             *SnapshotRegion `json:"region,omitempty"`
	Threshold          float64         `json:"threshold"`
	BaselineEvidenceID *int64          `json:"baseline_evidence_id,omitempty,string"`
	ViewportWidth      int             `json:"viewport_width"`
	ViewportHeight     int             `json:"viewport_height"`
	DeviceScaleFactor  float64         `json:"device_scale_factor"`
}

// SnapshotRegion is the normalized screenshot crop of a visual assertion.
type SnapshotRegion struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// CaseSnapshotAssertion is the assertion projection inside a snapshot.
type CaseSnapshotAssertion struct {
	ID         int64               `json:"id,string"`
	StepSeq    int                 `json:"step_seq"`
	Kind       string              `json:"kind"`
	Target     string              `json:"target"`
	Expression string              `json:"expression"`
	Expected   string              `json:"expected"`
	Visual     *CaseSnapshotVisual `json:"visual,omitempty"`
}

// CaseSnapshotSource is the requirement provenance of a case snapshot.
type CaseSnapshotSource struct {
	RequirementID int64 `json:"requirement_id,string"`
}

// CanonicalCaseHash returns the lowercase hex SHA-256 of the snapshot's
// canonical JSON encoding. Struct fields are encoded in declaration order and
// map keys in sorted order, so the same snapshot always produces the same
// hash. A marshal failure (for example a non-JSON value in TestData or Data)
// yields the empty string rather than a fabricated digest.
func CanonicalCaseHash(s CaseSnapshot) string {
	canonical, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// workingGraphCase is one case of the canonical working-graph document hashed by
// WorkingGraphHash.
type workingGraphCase struct {
	CaseNodeID int64  `json:"case_node_id"`
	CaseHash   string `json:"case_hash"`
}

// workingGraph is the canonical working-graph document hashed by
// WorkingGraphHash.
type workingGraph struct {
	VersionID int64              `json:"version_id"`
	Cases     []workingGraphCase `json:"cases"`
}

// WorkingGraphHash returns the lowercase hex SHA-256 of the current working
// graph (spec §5.3): the sorted per-case content hashes of every case snapshot
// the graph contains. The snapshot's captured_at and graph_hash are metadata,
// not graph content, so they are excluded; origin is content, so a supplement
// hashes differently from an identical baseline case. Cases are ordered by node
// id, which makes the digest independent of the caller's selection order. An
// empty graph or an unhashable case yields the empty string rather than a
// fabricated digest.
func WorkingGraphHash(versionID int64, snapshots []CaseSnapshot) string {
	if len(snapshots) == 0 {
		return ""
	}
	cases := make([]workingGraphCase, 0, len(snapshots))
	for _, snapshot := range snapshots {
		normalized := snapshot
		normalized.CapturedAt = time.Time{}
		normalized.GraphHash = ""
		caseHash := CanonicalCaseHash(normalized)
		if caseHash == "" {
			return ""
		}
		cases = append(cases, workingGraphCase{
			CaseNodeID: snapshot.Node.ID,
			CaseHash:   caseHash,
		})
	}
	sort.Slice(cases, func(i, j int) bool {
		if cases[i].CaseNodeID != cases[j].CaseNodeID {
			return cases[i].CaseNodeID < cases[j].CaseNodeID
		}
		return cases[i].CaseHash < cases[j].CaseHash
	})
	canonical, err := json.Marshal(workingGraph{VersionID: versionID, Cases: cases})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
