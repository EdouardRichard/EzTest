package executionpg

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// EvidenceRepository implements the matching executionapp repository port.
type EvidenceRepository struct{ base }

const evidenceColumns = `id, case_run_id, version_sut_id, step_seq, assertion_seq, kind,
	content_ref_id, sha256, size_bytes, mime, filename, created_by_run_id, created_at`

// Insert writes one immutable evidence row.
func (r *EvidenceRepository) Insert(ctx context.Context, evidence execution.Evidence) error {
	ctx = contextOrBackground(ctx)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO evidence (id, case_run_id, version_sut_id, step_seq, assertion_seq, kind,
			content_ref_id, sha256, size_bytes, mime, filename, created_by_run_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, r.insertID(evidence.ID), evidence.CaseRunID, evidence.VersionSUTID, evidence.StepSeq,
		evidence.AssertionSeq, string(evidence.Kind), evidence.ContentRefID, evidence.SHA256,
		evidence.SizeBytes, evidence.MIME, evidence.Filename, evidence.CreatedByRunID)
	return wrapDB("insert evidence", err)
}

// Get loads one evidence row.
func (r *EvidenceRepository) Get(ctx context.Context, id int64) (execution.Evidence, error) {
	ctx = contextOrBackground(ctx)
	item, err := scanEvidence(r.pool.QueryRow(ctx, `SELECT `+evidenceColumns+` FROM evidence WHERE id = $1`, id))
	if err != nil {
		return execution.Evidence{}, wrapDB("get evidence", err)
	}
	return item, nil
}

// ListByCaseRun lists the evidence of one case_run newest first.
func (r *EvidenceRepository) ListByCaseRun(ctx context.Context, caseRunID int64, limit int) ([]execution.Evidence, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+evidenceColumns+` FROM evidence
		WHERE case_run_id = $1 ORDER BY id DESC LIMIT $2
	`, caseRunID, effectiveLimit(limit))
	if err != nil {
		return nil, wrapDB("list case run evidence", err)
	}
	defer rows.Close()
	items, err := collectEvidence(rows)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListByVersion lists one filtered, descending-ID page of a version's evidence.
func (r *EvidenceRepository) ListByVersion(ctx context.Context, versionID int64, filter execution.EvidenceFilter) ([]execution.Evidence, bool, error) {
	ctx = contextOrBackground(ctx)
	limit := effectiveLimit(filter.Limit)
	kind := ""
	if filter.Kind != "" {
		kind = string(filter.Kind)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+prefixedColumns("evidence", evidenceColumns)+`
		FROM evidence
		JOIN case_run run ON run.id = evidence.case_run_id
		JOIN run_wave wave ON wave.id = run.wave_id
		WHERE wave.version_id = $1
		  AND ($2::BIGINT IS NULL OR evidence.case_run_id = $2)
		  AND ($3::BIGINT IS NULL OR evidence.version_sut_id = $3)
		  AND ($4 = '' OR evidence.kind = $4)
		  AND ($5::BIGINT IS NULL OR evidence.id < $5)
		ORDER BY evidence.id DESC
		LIMIT $6
	`, versionID, filter.CaseRunID, filter.VersionSUTID, kind, filter.BeforeID, limit+1)
	if err != nil {
		return nil, false, wrapDB("list version evidence", err)
	}
	defer rows.Close()
	items, err := collectEvidence(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

// InsertRef links one evidence row to an owner row.
func (r *EvidenceRepository) InsertRef(ctx context.Context, ref execution.EvidenceRef) error {
	ctx = contextOrBackground(ctx)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO evidence_ref (id, evidence_id, owner_kind, owner_id, role)
		VALUES ($1, $2, $3, $4, $5)
	`, r.insertID(ref.ID), ref.EvidenceID, ref.OwnerKind, ref.OwnerID, ref.Role)
	return wrapDB("insert evidence ref", err)
}

// CountByCaseRun counts the evidence rows of one case_run.
func (r *EvidenceRepository) CountByCaseRun(ctx context.Context, caseRunID int64) (int, error) {
	ctx = contextOrBackground(ctx)
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM evidence WHERE case_run_id = $1
	`, caseRunID).Scan(&count); err != nil {
		return 0, wrapDB("count case run evidence", err)
	}
	return count, nil
}

func scanEvidence(row rowScanner) (execution.Evidence, error) {
	var item execution.Evidence
	var kind string
	err := row.Scan(&item.ID, &item.CaseRunID, &item.VersionSUTID, &item.StepSeq, &item.AssertionSeq,
		&kind, &item.ContentRefID, &item.SHA256, &item.SizeBytes, &item.MIME, &item.Filename,
		&item.CreatedByRunID, &item.CreatedAt)
	if err != nil {
		return execution.Evidence{}, err
	}
	item.Kind = execution.EvidenceKind(kind)
	return item, nil
}

func collectEvidence(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]execution.Evidence, error) {
	items := make([]execution.Evidence, 0)
	for rows.Next() {
		item, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
