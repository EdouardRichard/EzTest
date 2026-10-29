package domain

import "time"

// Reference kinds of a case source, mirrored from the case_source.ref_kind
// CHECK constraint.
const (
	RefRequirement = "requirement"
	RefAppModel    = "app_model"
	RefAIRun       = "ai_run"
	RefManual      = "manual"
)

// Source confidence levels, mirrored from the case_source.confidence CHECK
// constraint.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// CaseSource ties a case node to the requirement, app model element, AI run or
// manual note that justifies it. A case must keep at least one requirement
// source (spec §6.2). IDs are application-generated Snowflakes.
type CaseSource struct {
	ID         int64
	VersionID  int64
	NodeID     int64
	RefKind    string
	RefID      *int64
	RefKey     string
	Source     string
	Confidence string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
