package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// CaseSnapshotNode is the case node projection inside a snapshot.
type CaseSnapshotNode struct {
	ID            int64          `json:"id,string"`
	Title         string         `json:"title"`
	Priority      string         `json:"priority"`
	Preconditions string         `json:"preconditions"`
	TestData      map[string]any `json:"test_data"`
}

// CaseSnapshotStep is the step projection inside a snapshot.
type CaseSnapshotStep struct {
	Seq      int            `json:"seq"`
	Action   string         `json:"action"`
	Expected string         `json:"expected"`
	Data     map[string]any `json:"data"`
}

// CaseSnapshotAssertion is the assertion projection inside a snapshot.
type CaseSnapshotAssertion struct {
	ID         int64  `json:"id,string"`
	StepSeq    int    `json:"step_seq"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Expression string `json:"expression"`
	Expected   string `json:"expected"`
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
