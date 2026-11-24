package executionapp

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// EvidenceRef is the read projection of one evidence_ref ownership link joined
// with its immutable evidence row (v0.2.4-G V-0019, SC-G9). It composes the two
// domain facts instead of copying their fields: Ref is the link (owner kind/id
// and role), Evidence is the content-addressed metadata (sha256/size/kind) the
// report reverse-lookup renders. Evidence content bytes never cross this port —
// only the metadata the execution context already stores (constitution art.4.1).
type EvidenceRef struct {
	Ref      execution.EvidenceRef
	Evidence execution.Evidence
}

// EvidenceRefReader is the read-only reverse-lookup port over the evidence_ref
// fact table: owner_kind/owner_id -> the evidence bound to that owner. It is the
// production read side of evidence_ref (write-only until v0.2.4-G, V-0019),
// consumed cross-context by the delivery report read model (T8/T9). ownerKind
// values are the execution domain EvidenceOwner* constants; the adapter binds
// them as query parameters, never inline SQL literals (single source of truth,
// constitution art.5). This is the evidence_ref TABLE read side — distinct from
// the requirement_closure.evidence_refs / version_fact.evidence_refs columns,
// which have their own readers.
type EvidenceRefReader interface {
	// ListByOwner returns every evidence_ref of one owner in append order
	// (evidence_ref.id ascending), each joined with its evidence metadata. An
	// owner with no links returns an empty, non-nil slice (never an error).
	ListByOwner(ctx context.Context, ownerKind string, ownerID int64) ([]EvidenceRef, error)
}

// IntentReader is the read-only port that fetches ONE execution intent by ID,
// including its frozen environment_fingerprint and code_baseline (v0.2.4-G
// per-run actual fingerprint, SC-G5/G6). ListActive is insufficient: a report
// needs the specific intent a case_run executed under (case_run.IntentID), whose
// fingerprint was fixed at intent creation (F semantics) and must be read back
// as stored — never recomputed or live-read from the current environment, and
// never substituted with another intent's boundary. Terminal intents are
// included (a settled run's intent is no longer active).
type IntentReader interface {
	GetByID(ctx context.Context, intentID int64) (execution.ExecutionIntent, error)
}
