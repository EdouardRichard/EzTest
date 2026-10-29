package application

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// VisualAssertionTarget is the case context of one visual assertion as read by
// the visual assertion store: the owning case node (for the revision CAS), the
// persisted assertion kind and the current comparison parameters (nil before the
// first PUT).
type VisualAssertionTarget struct {
	NodeID       int64
	NodeRevision int
	Kind         string
	Visual       *domain.VisualAssertion
}

// VisualAssertionStore persists case_visual_assertion rows and owns the node
// revision CAS of the visual parameter write. Implementations must reject a
// stale expectedNodeRevision with a case_revision_conflict domain error and must
// not create parameters for a non-visual assertion.
type VisualAssertionStore interface {
	Get(ctx context.Context, versionID, assertionID int64) (VisualAssertionTarget, error)
	// Save upserts the parameters of assertionID and bumps the owning node
	// revision under the expectedNodeRevision CAS, atomically.
	Save(ctx context.Context, versionID, assertionID int64, expectedNodeRevision int, params domain.VisualAssertionParams, actor int64, now time.Time) (domain.VisualAssertion, error)
	// ApproveBaseline writes baseline_evidence_id, approved_by and approved_at.
	// The baseline is not a graph content change, so the node revision is not
	// bumped.
	ApproveBaseline(ctx context.Context, versionID, assertionID, evidenceID, actor int64, now time.Time) (domain.VisualAssertion, error)
}

// VisualEvidence is one verified evidence artifact read for baseline approval.
// Data holds the already sha256-verified bytes; Path is never exposed to the
// caller.
type VisualEvidence struct {
	ID        int64
	Kind      string
	Mime      string
	SHA256    string
	SizeBytes int64
	Data      []byte
}

// VisualBaselineEvidenceReader reads one evidence artifact for baseline
// approval. Implementations must prove the evidence belongs to versionID and to
// a case run of caseNodeID, and must return a not-found domain error otherwise;
// the returned bytes must be sha256/size verified.
type VisualBaselineEvidenceReader interface {
	ReadForBaseline(ctx context.Context, versionID, caseNodeID, evidenceID int64) (VisualEvidence, error)
}
