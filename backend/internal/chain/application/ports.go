// Package application holds the P8 exploration-chain read model: the
// cross-context provider ports it consumes and the aggregation service that
// turns their per-layer snapshots into one deterministic graph.
//
// This file is the single home of the chain cross-context port declarations. It
// contains value types and interfaces only: no pgx, HTTP or other-context
// imports. Adapters live in each context's infrastructure/chain package and are
// wired by the composition root (Task 7), so the chain context never imports
// another bounded context.
package application

import (
	"context"
	"fmt"
	"time"
)

// Node kinds rendered by the exploration chain (spec §5.1). The node id is
// "<kind>:<domainId>" so a node can always be resolved without a second lookup.
const (
	NodeKindRequirement = "requirement"
	NodeKindCase        = "case"
	NodeKindResult      = "result"
	NodeKindIssue       = "issue"
)

// Edge kinds rendered by the exploration chain (spec §5.1).
const (
	EdgeKindRequirementCase = "requirement_case"
	EdgeKindCaseResult      = "case_result"
	EdgeKindResultIssue     = "result_issue"
	EdgeKindCaseIssue       = "case_issue"
	EdgeKindImpactCase      = "impact_case"
)

// ReasonInvalidArgument is the stable details.reason of the chain context's 400
// invalid_argument errors (spec §5.1).
const ReasonInvalidArgument = "invalid_argument"

// ErrInvalidArgument reports a rejected query field. Field names the offending
// request parameter; Min and Max carry the accepted bounds when the rejection is
// a range violation (zero means "not applicable"). The HTTP layer renders them
// as details{field,min,max,reason}.
type ErrInvalidArgument struct {
	Field   string
	Reason  string
	Min     int
	Max     int
	Message string
}

// Error implements error.
func (e *ErrInvalidArgument) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Field != "" {
		return "invalid " + e.Field
	}
	return ReasonInvalidArgument
}

// ErrNotFound reports a missing version or a batch that does not belong to the
// addressed version (spec §5.1: both are 404). Resource is "version" or
// "batch".
type ErrNotFound struct {
	Resource string
	ID       int64
	Message  string
}

// Error implements error.
func (e *ErrNotFound) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Resource != "" {
		return fmt.Sprintf("%s %d not found", e.Resource, e.ID)
	}
	return "not found"
}

// ---------------------------------------------------------------------------
// Provider DTOs
// ---------------------------------------------------------------------------

// BatchRow is the version provider's resolved batch. A zero ID means the
// version exists but has no batch yet.
type BatchRow struct {
	ID      int64
	BatchNo int
	Kind    string
	Status  string
}

// RequirementRow is one requirement of the explored version, including the
// declaration-order fields used to sort the root layer.
type RequirementRow struct {
	ID                int64
	Title             string
	Status            string
	Priority          string
	SortOrder         int
	OpenQuestionCount int
}

// CaseRow is one case node of the version's mindmap. RequirementIDs are the
// requirement sources attached to the case (case_source ref_kind=requirement);
// the service unions them with coverage.matrix.
type CaseRow struct {
	ID             int64
	Title          string
	Status         string
	Source         string
	Priority       string
	RequirementIDs []int64
	StepCount      int
	AssertionCount int
	SortOrder      int
}

// CoverageCell is one row of the requirement coverage matrix: the requirement
// and the case nodes covering it (mindmap domain BuildCoverage).
type CoverageCell struct {
	RequirementID int64
	CaseIDs       []int64
}

// ResultRow is the latest execution attempt of one case node in the selected
// batch scope. Providers may return every attempt; the service keeps the newest
// (attempt, then id) per case.
type ResultRow struct {
	ID            int64
	CaseNodeID    int64
	BatchID       int64
	Attempt       int
	Status        string
	DurationMs    int64
	FailureClass  string
	Summary       string
	EvidenceCount int
}

// IssueRow is one delivery issue. CaseRunID and CaseNodeID are nil for
// requirement-gap or otherwise unlinked issues.
type IssueRow struct {
	ID            int64
	Title         string
	Severity      string
	Status        string
	Category      string
	Confidence    string
	CaseRunID     *int64
	CaseNodeID    *int64
	EvidenceCount int
	CodeRefCount  int
	CreatedAt     time.Time
}

// ImpactRow is one candidate of the latest P4 code analysis. The service keeps
// requirement candidates that are selected or of high/medium confidence and
// turns them into impact_case edges (spec §5.1).
type ImpactRow struct {
	SubjectKind string
	SubjectID   int64
	Reason      string
	Confidence  string
	Selected    bool
}

// ---------------------------------------------------------------------------
// Provider ports
// ---------------------------------------------------------------------------

// VersionProvider supplies the version/batch resolution and the requirement
// layer (spec §5.2 queries 1–2). Requirements returns the complete layer of the
// version: the service applies the query filters and truncation itself, so an
// adapter must not pre-filter by the request query.
type VersionProvider interface {
	// GetBatch resolves the selected batch of versionID. A nil batchID means
	// "the most recent batch"; the returned BatchRow is zero when the version
	// exists but has no batch. A missing version, and an explicit batchID that
	// does not belong to versionID, return ErrNotFound.
	GetBatch(ctx context.Context, versionID int64, batchID *int64) (BatchRow, error)
	Requirements(ctx context.Context, versionID int64) ([]RequirementRow, error)
}

// MindmapProvider supplies the case layer and the requirement coverage matrix
// (spec §5.2 queries 3–4/9). Both methods return the complete version layer.
type MindmapProvider interface {
	Cases(ctx context.Context, versionID int64) ([]CaseRow, error)
	Coverage(ctx context.Context, versionID int64) ([]CoverageCell, error)
}

// ExecutionProvider supplies the result layer (spec §5.2 query 6). A nil
// batchID means "every batch of the version"; otherwise rows are restricted to
// that batch. Adapters may return one row per attempt or already-latest rows:
// the service deterministically keeps the newest attempt per case.
type ExecutionProvider interface {
	LatestResults(ctx context.Context, versionID int64, batchID *int64) ([]ResultRow, error)
}

// DeliveryProvider supplies the issue layer (spec §5.2 query 8). A nil batchID
// means "every batch of the version"; otherwise issues are restricted to that
// batch. IssueStatus/IssueSeverity are applied by the service, not the adapter.
type DeliveryProvider interface {
	Issues(ctx context.Context, versionID int64, batchID *int64) ([]IssueRow, error)
}

// CodeIntelProvider supplies the latest P4 impact candidates (spec §5.2 query
// 10). It returns an empty slice when the version has no analysis.
type CodeIntelProvider interface {
	Impact(ctx context.Context, versionID int64) ([]ImpactRow, error)
}

// Providers groups the five layer providers. Version, Mindmap and Execution are
// required; Delivery and CodeIntel are optional and degrade to an empty
// issue/impact layer when nil.
type Providers struct {
	Version   VersionProvider
	Mindmap   MindmapProvider
	Execution ExecutionProvider
	Delivery  DeliveryProvider
	CodeIntel CodeIntelProvider
}
