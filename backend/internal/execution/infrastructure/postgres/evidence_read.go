package executionpg

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ListByOwner is the production read side of the evidence_ref fact table
// (v0.2.4-G V-0019, SC-G9): it reverse-looks-up the evidence bound to one owner
// (case_run / step_result / assertion_result / human_request) and joins each
// link with its immutable evidence metadata. InsertRef wrote these links; until
// now nothing read them back. The owner_kind is bound as a query parameter (the
// caller passes the execution domain EvidenceOwner* constants), never an inline
// SQL literal, so the kind vocabulary stays single-sourced (constitution art.5).
//
// Rows are returned in append order (evidence_ref.id ascending); an owner with
// no links yields an empty, non-nil slice. The join reads only evidence metadata
// — content bytes stay in the content ledger (constitution art.4.1).
func (r *EvidenceRepository) ListByOwner(ctx context.Context, ownerKind string, ownerID int64) ([]executionapp.EvidenceRef, error) {
	// An unknown owner_kind would silently match zero rows and read as "no
	// evidence bound" (七.6). Reject it at the entry with a typed field error;
	// the vocabulary authority is the execution domain (ValidEvidenceOwnerKind).
	if !execution.ValidEvidenceOwnerKind(ownerKind) {
		return nil, platform.Invalid("owner_kind", "must be a persisted evidence_ref owner kind (case_run/step_result/assertion_result/human_request)")
	}
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+prefixedColumns("evidence_ref", evidenceRefColumns)+`, `+prefixedColumns("evidence", evidenceColumns)+`
		FROM evidence_ref
		JOIN evidence ON evidence.id = evidence_ref.evidence_id
		WHERE evidence_ref.owner_kind = $1 AND evidence_ref.owner_id = $2
		ORDER BY evidence_ref.id ASC
	`, ownerKind, ownerID)
	if err != nil {
		return nil, wrapDB("list evidence refs by owner", err)
	}
	defer rows.Close()
	items := make([]executionapp.EvidenceRef, 0)
	for rows.Next() {
		item, err := scanEvidenceRef(rows)
		if err != nil {
			return nil, wrapDB("scan evidence ref by owner", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list evidence refs by owner", err)
	}
	return items, nil
}

// evidenceRefColumns is the evidence_ref column list in scan order. The evidence
// side reuses evidenceColumns so the join's column order has one authority.
const evidenceRefColumns = `id, evidence_id, owner_kind, owner_id, role, created_at`

// scanEvidenceRef scans one joined evidence_ref + evidence row. The evidence
// destinations come from the SAME evidenceScanDests authority as scanEvidence
// (both driven by evidenceColumns), so the joined read cannot drift from the
// single-table read (H-G4 T4).
func scanEvidenceRef(row rowScanner) (executionapp.EvidenceRef, error) {
	var ref execution.EvidenceRef
	var ev execution.Evidence
	var kind string
	dests := append([]any{
		&ref.ID, &ref.EvidenceID, &ref.OwnerKind, &ref.OwnerID, &ref.Role, &ref.CreatedAt,
	}, evidenceScanDests(&ev, &kind)...)
	if err := row.Scan(dests...); err != nil {
		return executionapp.EvidenceRef{}, err
	}
	ev.Kind = execution.EvidenceKind(kind)
	return executionapp.EvidenceRef{Ref: ref, Evidence: ev}, nil
}

// The evidence store satisfies the cross-context read port the delivery report
// read model consumes (T8/T9).
var _ executionapp.EvidenceRefReader = (*EvidenceRepository)(nil)
